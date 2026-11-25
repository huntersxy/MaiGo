package test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"MaiGo/internal/platform_io"
	"MaiGo/internal/platform_io/drivers/qq_official"
	"MaiGo/internal/platform_io/drivers/snowluma"
	"MaiGo/internal/plugin_runtime/capabilities"
	"MaiGo/internal/plugin_runtime/host"
	"MaiGo/internal/plugin_runtime/hostports"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestNativeGatewayReconnectPreservesPluginSending(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	var qqConnections, oneBotSends atomic.Int32
	closeFirstQQ := make(chan struct{})
	qqServer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/app/getAppAccessToken":
			fmt.Fprint(writer, `{"access_token":"local-token","expires_in":7200}`)
		case "/gateway":
			fmt.Fprintf(writer, `{"url":%q}`, "wss://"+request.Host+"/ws")
		case "/ws":
			conn, err := websocket.Accept(writer, request, nil)
			if err != nil {
				return
			}
			defer conn.CloseNow()
			if qqConnections.Add(1) == 1 {
				if err := conn.Write(ctx, websocket.MessageText, []byte(`{"op":0,"t":"READY","s":1,"d":{"session_id":"session","user":{"id":"official","username":"Bot"}}}`)); err != nil {
					return
				}
				select {
				case <-closeFirstQQ:
				case <-ctx.Done():
				}
				return
			}
			if err := conn.Write(ctx, websocket.MessageText, []byte(`{"op":0,"t":"RESUMED","s":2,"d":{}}`)); err == nil {
				_, _, _ = conn.Read(ctx)
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	defer qqServer.Close()
	original := http.DefaultTransport
	testTransport := qqServer.Client().Transport.(*http.Transport).Clone()
	http.DefaultTransport = testTransport
	defer func() { http.DefaultTransport = original }()

	oneBot := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, err := websocket.Accept(writer, request, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			var action struct {
				Name   string `json:"action"`
				Echo   string `json:"echo"`
				Params struct {
					GroupID     int64  `json:"group_id"`
					MessageID   any    `json:"message_id"`
					MessageType string `json:"message_type"`
					Message     []struct {
						Type string            `json:"type"`
						Data map[string]string `json:"data"`
					} `json:"message"`
				} `json:"params"`
			}
			if err := wsjson.Read(ctx, conn, &action); err != nil {
				return
			}
			var data map[string]any
			switch action.Name {
			case "get_login_info":
				data = map[string]any{"user_id": 10001}
			case "get_version_info":
				data = map[string]any{"app_name": "SnowLuma"}
			case "send_msg":
				if action.Params.GroupID != 12345 || action.Params.MessageType != "group" || len(action.Params.Message) != 2 ||
					action.Params.Message[0].Type != "at" || action.Params.Message[0].Data["qq"] != "23456" ||
					action.Params.Message[1].Type != "text" || action.Params.Message[1].Data["text"] != "partner reply" {
					t.Errorf("invalid plugin outbound message: %+v", action.Params)
					return
				}
				oneBotSends.Add(1)
				data = map[string]any{"message_id": 9876}
			case "get_msg":
				if fmt.Sprint(action.Params.MessageID) != "9876" {
					t.Errorf("queried wrong delivery receipt: %+v", action.Params)
					return
				}
				data = map[string]any{"message_type": "group", "group_id": 12345, "message_id": 9876, "message_seq": 42}
			default:
				t.Errorf("unexpected OneBot action: %s", action.Name)
				return
			}
			if err := wsjson.Write(ctx, conn, map[string]any{"status": "ok", "retcode": 0, "data": data, "echo": action.Echo}); err != nil {
				return
			}
		}
	}))
	defer oneBot.Close()
	server, port, err := net.SplitHostPort(oneBot.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	t.Setenv("MAIBOT_INSTANCE_DIR", root)
	pluginHost := host.NewHost()
	pluginHost.SetInstanceRoot(root)
	manager, err := platformio.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop(ctx)
	pluginHost.SetMessageGatewayPort(hostports.NewGateway(manager, nil))
	if _, err := host.RegisterHostCapabilities(pluginHost, host.HostCapabilityDeps{}); err != nil {
		t.Fatal(err)
	}
	native := platformio.DefaultNativeRuntime()
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if err := pluginHost.Stop(cleanupCtx); err != nil {
			t.Error(err)
		}
	}()
	for _, spec := range []struct {
		registration platformio.NativeRegistration
		config       string
	}{
		{
			platformio.NativeRegistration{PluginID: snowluma.PluginID, DirectoryName: snowluma.DirectoryName, DefaultConfig: snowluma.DefaultConfigMap(), Factory: snowluma.NewFactoryStandalone()},
			fmt.Sprintf("[plugin]\nenabled = true\n[client]\nserver = %q\nport = %s\n", server, port),
		},
		{
			platformio.NativeRegistration{PluginID: qq_official.PluginID, DirectoryName: qq_official.DirectoryName, DefaultConfig: qq_official.DefaultConfigMap(), Factory: qq_official.NewFactoryStandalone()},
			fmt.Sprintf("[plugin]\nenabled = true\n[qq_official]\napp_id = 'local-app'\napp_secret = 'local-secret'\napi_base_url = %q\n[advanced]\nreconnect_delay_sec = 1\n", qqServer.URL),
		},
	} {
		registration := spec.registration
		registration.ConfigRoot = filepath.Join(root, "config", "builtin_plugins", registration.DirectoryName)
		if err := os.MkdirAll(registration.ConfigRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(registration.ConfigRoot, "config.toml"), []byte(spec.config), 0o600); err != nil {
			t.Fatal(err)
		}
		previous, existed := native.Get(registration.PluginID)
		t.Cleanup(func() {
			native.Unregister(registration.PluginID)
			if existed {
				if err := native.Register(previous); err != nil {
					t.Error(err)
				}
			}
		})
		if err := native.Register(registration); err != nil {
			t.Fatal(err)
		}
		if err := pluginHost.StartNativeRuntimeIfEnabled(ctx, registration.PluginID); err != nil {
			t.Fatal(err)
		}
	}
	waitReady := func(pluginID, gateway, account string, ready func() bool) {
		t.Helper()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			state, _ := pluginHost.MessageGatewayState(pluginID, gateway)
			route := platformio.RouteKey{Platform: "qq", AccountID: account}
			if ready() && state.Ready && state.AccountID == account &&
				manager.HasReceiveBinding(route, platformio.BuildPluginGatewayDriverID(pluginID, gateway)) && len(manager.ResolveSendAdapters(route)) == 1 {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatalf("gateway %s did not become ready: %+v", pluginID, state)
			case <-ticker.C:
			}
		}
	}
	waitReady(snowluma.PluginID, snowluma.GatewayName, "10001", func() bool { return true })
	waitReady(qq_official.PluginID, qq_official.GatewayName, "official", func() bool { return qqConnections.Load() == 1 })
	close(closeFirstQQ)
	waitReady(qq_official.PluginID, qq_official.GatewayName, "official", func() bool { return qqConnections.Load() == 2 })

	const sendAPI = "adapter.napcat.message.send_msg"
	result, err := pluginHost.InvokeCapability(ctx, "onelxzy.partner-game", capabilities.CapAPICall, map[string]any{
		"api_name": sendAPI,
		"args": map[string]any{"params": map[string]any{
			"message_type": "group", "group_id": int64(12345), "message": []any{
				map[string]any{"type": "at", "data": map[string]any{"qq": "23456"}},
				map[string]any{"type": "text", "data": map[string]any{"text": "partner reply"}},
			},
		}},
	})
	payload, _ := result.(map[string]any)
	action, _ := payload["result"].(map[string]any)
	receipt, _ := action["data"].(map[string]any)
	if err != nil || payload["success"] != true || receipt["message_id"] != int64(9876) || oneBotSends.Load() != 1 {
		t.Fatalf("plugin send did not return a real receipt after reconnect: %#v, %v", result, err)
	}
	if err := pluginHost.NativeTeardown(ctx, snowluma.PluginID); err != nil {
		t.Fatal(err)
	}
	if native.IsStarted(snowluma.PluginID) || manager.GetDriver(platformio.BuildPluginGatewayDriverID(snowluma.PluginID, snowluma.GatewayName)) != nil ||
		pluginHost.APIRegistry().GetAPI(snowluma.PluginID, sendAPI, "", false) != nil {
		t.Fatal("teardown left a native connection, driver or API registered")
	}
}

package snowluma

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"image"
	"image/png"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"MaiGo/internal/plugin_runtime/capabilities"
)

type imageInboundSink chan map[string]any

func (s imageInboundSink) ReceiveGatewayMessage(ctx context.Context, _ capabilities.PluginGatewayInboundRequest, message map[string]any) (bool, map[string]any, error) {
	select {
	case s <- message:
		return true, nil, nil
	case <-ctx.Done():
		return false, nil, ctx.Err()
	}
}

func TestPrivateImageRecoversBytesThroughGetImage(t *testing.T) {
	// PNG 不压缩时，回包会超过 coder/websocket 默认的 32 KiB 上限。
	picture := image.NewNRGBA(image.Rect(0, 0, 128, 128))
	_, _ = rand.New(rand.NewSource(1)).Read(picture.Pix)
	var raw bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.NoCompression}
	if err := encoder.Encode(&raw, picture); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(raw.Bytes())
	sum := sha256.Sum256(raw.Bytes())
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/expired-image" {
			http.NotFound(w, req)
			return
		}
		conn, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		write := func(data map[string]any) bool { return wsjson.Write(ctx, conn, data) == nil }
		for {
			var action map[string]any
			if err := wsjson.Read(ctx, conn, &action); err != nil {
				return
			}
			var data map[string]any
			switch action["action"] {
			case "get_login_info":
				data = map[string]any{"user_id": "200"}
			case "get_version_info":
				data = map[string]any{"app_name": "SnowLuma"}
			case "get_image":
				if asString(asMap(action["params"])["file"]) != "picture-id" {
					t.Errorf("get_image received unexpected params %v", action["params"])
					return
				}
				data = map[string]any{"file": "base64://" + encoded}
			default:
				t.Errorf("unexpected OneBot action %v", action["action"])
				return
			}
			if !write(map[string]any{"status": "ok", "retcode": 0, "echo": action["echo"], "data": data}) {
				return
			}
			if action["action"] == "get_version_info" {
				if !write(map[string]any{
					"post_type": "message", "message_type": "private", "message_id": "1", "user_id": "100", "self_id": "200",
					"message": []any{map[string]any{"type": "image", "data": map[string]any{
						"file_id": "picture-id", "url": "http://" + req.Host + "/expired-image", "sub_type": 0,
					}}},
				}) {
					return
				}
			}
		}
	}))
	t.Cleanup(server.Close)
	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	settings := DefaultSettings()
	settings.Plugin.Enabled = true
	settings.Client.Server = host
	settings.Client.Port = port
	sink := make(imageInboundSink, 1)
	adapter := NewAdapter(settings, &HostHooks{Inbound: sink})
	if err := adapter.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Stop(context.Background()) })
	select {
	case message := <-sink:
		segments := segmentItems(message["raw_message"])
		if message["chat_type"] != "private" || message["is_picture"] != true || len(segments) != 1 {
			t.Fatal("expected an image-only private message")
		}
		segment := asMap(segments[0])
		if segment["type"] != "image" || segment["hash"] != hex.EncodeToString(sum[:]) || segment["binary_data_base64"] != encoded {
			t.Fatal("the original image bytes did not reach the host")
		}
	case <-ctx.Done():
		t.Fatal("private image did not reach the host")
	}
}

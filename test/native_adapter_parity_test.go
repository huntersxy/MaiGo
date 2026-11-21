package test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	platformio "MaiGo/internal/platform_io"
	"MaiGo/internal/platform_io/drivers/qq_official"
	"MaiGo/internal/platform_io/drivers/telegram"
	"MaiGo/internal/platform_io/drivers/weixin_clawbot"
	"MaiGo/internal/plugin_runtime/capabilities"
)

func TestNativeAdapterWireTypes(t *testing.T) {
	value, err := platformio.DecodeWireObject([]byte(`{"data":[{"id":9007199254740993,"float":1.0,"b":false,"n":null,"opaque":18446744073709551615}]}`))
	if err != nil {
		t.Fatal(err)
	}
	row := value["data"].([]any)[0].(map[string]any)
	if row["id"] != int64(9007199254740993) || row["float"] != float64(1) || row["b"] != false ||
		row["n"] != nil || row["opaque"] != json.Number("18446744073709551615") {
		t.Fatalf("JSON wire types changed: %#v", row)
	}
	for _, malformed := range []string{"null", "[]", "{} 42"} {
		if _, err := platformio.DecodeWireObject([]byte(malformed)); err == nil {
			t.Errorf("accepted invalid object %q", malformed)
		}
	}
}

func TestNativeAdapterSDKConfig(t *testing.T) {
	settings, err := telegram.ParseSettings(map[string]any{
		"plugin":   map[string]any{"enabled": int64(1)},
		"telegram": map[string]any{"poll_timeout_sec": " 15.0 ", "reconnect_delay_sec": 5},
	})
	if err != nil || !settings.Plugin.Enabled || settings.Telegram.PollTimeoutSec != 15 {
		t.Fatalf("Pydantic conversion mismatch: %+v, %v", settings, err)
	}
	if _, err := telegram.ParseSettings(map[string]any{"telegram": map[string]any{"poll_timeout_sec": 12.5}}); err == nil {
		t.Fatal("Telegram silently truncated a fractional integer")
	}
	if _, err := weixin_clawbot.ParseSettings(map[string]any{"weixin": map[string]any{"allowed_users": []any{"alice", 42}}}); err == nil {
		t.Fatal("Weixin silently dropped a user ID with the wrong type")
	}
	if _, err := qq_official.ParseSettings(map[string]any{"qq_official": map[string]any{"app_id": int64(9007199254740993)}}); err == nil {
		t.Fatal("QQ accepted a numeric AppID in a string field")
	}
	if _, err := qq_official.ParseSettings(map[string]any{"plugin": map[string]any{"enabled": "enabled"}}); err == nil {
		t.Fatal("QQ accepted a boolean string rejected by Pydantic")
	}
}

func TestNativeAdapterQQInboundParity(t *testing.T) {
	raw := `{"id":9007199254740993,"group_openid":"group","group_name":"示例群","author":{"member_openid":"sender"},"content":"前<@peer>后","timestamp":"2026-11-21T00:00:00Z","mentions":[{"id":"peer","nickname":"同伴"}],"msg_elements":[{"type":103,"at":{"openid":"bot"}}]}`
	payload, err := platformio.DecodeWireObject([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	got, _, _, err := qq_official.DecodeMessage("GROUP_MESSAGE_CREATE", payload, "bot", "麦麦", "1", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["message_id"] != "9007199254740993" || got["timestamp"] != "1795219200.0" || got["is_at"] != false ||
		got["processed_plain_text"] != "前@同伴 后" {
		t.Fatalf("QQ envelope did not match the Python codec: %#v", got)
	}
	segments := got["raw_message"].([]map[string]any)
	if len(segments) != 3 || segments[0]["data"] != "前" || segments[1]["type"] != "at" || segments[2]["data"] != "后" {
		t.Fatalf("mention reordered text: %#v", segments)
	}
}

func TestNativeAdapterTelegramInboundParity(t *testing.T) {
	raw := `{"chat":{"id":-9007199254740993,"type":"supergroup","title":"群","is_forum":true},"from":{"id":4,"first_name":"发送者"},"message_id":5,"date":6,"is_topic_message":true,"message_thread_id":7,"text":"😀 @bot @peer","entities":[{"type":"mention","offset":8,"length":5},{"type":"mention","offset":3,"length":4}]}`
	payload, err := platformio.DecodeWireObject([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	msg, err := telegram.DecodeMessage(payload, "3", "bot", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if msg["message_id"] != "-9007199254740993:5" || msg["is_mentioned"] != true {
		t.Fatalf("ID or mention lost: %#v", msg)
	}
	info := msg["message_info"].(map[string]any)
	group := info["group_info"].(map[string]any)
	if group["group_id"] != "-9007199254740993:7" {
		t.Fatalf("wrong topic ID: %#v", group)
	}
	parts := msg["raw_message"].([]map[string]any)
	if len(parts) != 4 || parts[1]["data"].(map[string]any)["target_user_id"] != "3" {
		t.Fatalf("UTF-16 mention ordering mismatch: %#v", parts)
	}
	payload["text"], payload["caption"], payload["entities"] = "", "备用说明", []any{}
	if msg, err := telegram.DecodeMessage(payload, "3", "bot", nil, ""); err != nil || msg != nil {
		t.Fatalf("present empty text must take priority: %v, %v", msg, err)
	}
}

func TestNativeAdapterTelegramBotAPI(t *testing.T) {
	var requests atomic.Int64
	stopPoll := make(chan struct{})
	outbound := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			fmt.Fprint(w, `{"ok":true,"result":{"id":9007199254740993,"username":"demo"}}`)
		case strings.HasSuffix(r.URL.Path, "/getWebhookInfo"):
			fmt.Fprint(w, `{"ok":true,"result":{"url":""}}`)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			if requests.Add(1) == 1 {
				fmt.Fprint(w, `{"ok":true,"result":[{"update_id":100,"message":{"chat":{"id":-41,"title":"群","type":"supergroup"},"from":{"id":6},"message_id":7,"date":8,"photo":[{"file_id":"small","width":1,"height":2},{"file_id":"big","width":4,"height":3}]}}]}`)
				return
			}
			<-stopPoll
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["file_id"] != "big" {
				t.Errorf("getFile body = %#v, %v", body, err)
			}
			fmt.Fprint(w, `{"ok":true,"result":{"file_path":"photo/photo.png","file_size":3}}`)
		case strings.HasSuffix(r.URL.Path, "/photo/photo.png"):
			fmt.Fprint(w, "png")
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			outbound <- body
			fmt.Fprint(w, `{"ok":true,"result":{"chat":{"id":-41},"message_id":9007199254740993}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer close(stopPoll)

	inbound := make(chan map[string]any, 1)
	settings := telegram.DefaultSettings()
	settings.Plugin.Enabled = true
	settings.Telegram.BotToken = "1:local_test"
	settings.Telegram.APIBaseURL = server.URL
	dir := t.TempDir()
	adapter := telegram.NewAdapter(settings, &telegram.HostHooks{
		DataDir: func() string { return dir },
		Inbound: nativeAdapterSink(func(message map[string]any) error {
			inbound <- message
			return nil
		}),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := adapter.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer adapter.Stop(ctx)
	select {
	case message := <-inbound:
		parts := message["raw_message"].([]map[string]any)
		if message["account_id"] != "9007199254740993" || len(parts) != 1 || parts[0]["type"] != "image" || parts[0]["binary_data_base64"] != "cG5n" {
			t.Fatalf("image ingress or account ID broken: %#v", message)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	out := platformio.OutboundMessageEnvelope{
		TargetID: "-41", ExternalReplyToMessageID: "-41:7",
		Segments: []platformio.Segment{{Kind: platformio.SegmentText, Text: "😀 "}, {Kind: platformio.SegmentAt, AtUserID: "42", Text: "人"}},
	}
	route := platformio.RouteKey{Platform: "telegram", AccountID: "9007199254740993"}
	for _, explicit := range []bool{false, true} {
		if explicit {
			out.Segments = append(out.Segments, platformio.Segment{Kind: platformio.SegmentReply, Extra: map[string]string{"id": "-41:7"}})
		}
		receipt, err := adapter.Send(ctx, out, route)
		if err != nil || receipt.ExternalMessageID != "-41:9007199254740993" {
			t.Fatalf("send failed: %+v, %v", receipt, err)
		}
		payload := <-outbound
		_, quoted := payload["reply_parameters"]
		if quoted != explicit || payload["text"] != "😀 @人 " {
			t.Fatalf("quote/text parity (explicit %v): %#v", explicit, payload)
		}
		entity := payload["entities"].([]any)[0].(map[string]any)
		if entity["type"] != "text_mention" || entity["offset"] != float64(3) {
			t.Fatalf("multipart mention is invalid: %#v", entity)
		}
	}
}

type nativeAdapterSink func(map[string]any) error

func (sink nativeAdapterSink) ReceiveGatewayMessage(_ context.Context, _ capabilities.PluginGatewayInboundRequest, msg map[string]any) (bool, map[string]any, error) {
	err := sink(msg)
	return err == nil, nil, err
}

type nativeAPIHandler = func(context.Context, string, string, map[string]any) (any, error)
type nativeAdapterAPIs map[string]nativeAPIHandler

func (api nativeAdapterAPIs) SetAPILocalHandler(name, _ string, _ map[string]any, f nativeAPIHandler) bool {
	api[name] = f
	return true
}
func (api nativeAdapterAPIs) RemoveAPIsByPlugin(string) int { return 0 }

func TestNativeAdapterWeixinILink(t *testing.T) {
	if got := weixin_clawbot.MessageID("账号", map[string]any{"from_user_id": "你", "z": []any{1.0, true, nil}}); got != "账号:你:b2ff1804e11c328529d7ff6f832d41889a70ce89fff0ed37abf19c73e199e8ea" {
		t.Fatalf("Python JSON fallback hash differs: %s", got)
	}
	var calls atomic.Int64
	stopPoll := make(chan struct{})
	wire := make(chan map[string]any, 3)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ilink/bot/get_bot_qrcode":
			fmt.Fprint(w, `{"qrcode":"code","qrcode_img_content":"https://example.test/login"}`)
		case "/ilink/bot/get_qrcode_status":
			fmt.Fprint(w, `{"status":"confirmed","ilink_bot_id":"bot","ilink_user_id":"peer","bot_token":"token"}`)
		case "/ilink/bot/getupdates":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if calls.Add(1) <= 2 {
				if body["get_updates_buf"] != "" {
					t.Errorf("cursor advanced before acceptance: %#v", body)
				}
				fmt.Fprint(w, `{"ret":0,"msgs":[{"message_type":1,"from_user_id":"peer","message_id":9007199254740993,"create_time_ms":1795219200123,"context_token":"context","item_list":[{"type":2,"image_item":{"media":{"encrypt_query_param":"in"}}},{"type":3,"voice_item":{"text":"语音转写"}}]}],"get_updates_buf":"after"}`)
				return
			}
			<-stopPoll
		case "/c2c/download":
			fmt.Fprint(w, "png")
		case "/ilink/bot/getuploadurl":
			fmt.Fprint(w, `{"ret":0,"upload_param":"upload"}`)
		case "/c2c/upload":
			w.Header().Set("x-encrypted-param", "out")
			fmt.Fprint(w, `{}`)
		case "/ilink/bot/sendmessage":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			wire <- body["msg"].(map[string]any)
			fmt.Fprint(w, `{"ret":0}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer close(stopPoll)

	original := http.DefaultTransport
	testTransport := server.Client().Transport.(*http.Transport).Clone()
	testTransport.TLSClientConfig.ServerName = server.Certificate().DNSNames[0]
	dialer := &net.Dialer{}
	testTransport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, server.Listener.Addr().String())
	}
	http.DefaultTransport = testTransport
	defer func() { http.DefaultTransport = original }()

	var attempts atomic.Int64
	inbound := make(chan map[string]any, 1)
	settings := weixin_clawbot.DefaultSettings()
	settings.Plugin.Enabled = true
	settings.Weixin.RetrySeconds = 1
	api := nativeAdapterAPIs{}
	dir := t.TempDir()
	adapter := weixin_clawbot.NewOrchestrator(settings, &weixin_clawbot.HostHooks{
		DataDir: func() string { return dir },
		APIs:    api,
		Inbound: nativeAdapterSink(func(message map[string]any) error {
			if attempts.Add(1) == 1 {
				return errors.New("local ingress temporarily unavailable")
			}
			inbound <- message
			return nil
		}),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := adapter.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer adapter.Stop(ctx)
	if _, err := api["remove_account"](ctx, "", "", map[string]any{"slot": 0.5}); err == nil {
		t.Fatal("fractional slot was accepted")
	}
	if _, err := api["add_account"](ctx, "", "", map[string]any{"remark": "自己的号"}); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-inbound:
		parts := message["raw_message"].([]map[string]any)
		if message["message_id"] != "bot:peer:9007199254740993" || message["timestamp"] != "1795219200.123" ||
			parts[0]["binary_data_base64"] != "cG5n" || parts[1]["data"] != "语音转写" {
			t.Fatalf("wrong Weixin inbound envelope: %#v", message)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	conn, slot, ok := adapter.Slot(0)
	if !ok || conn == nil {
		t.Fatal("missing account slot")
	}
	outbound := platformio.OutboundMessageEnvelope{TargetID: "peer", Segments: []platformio.Segment{
		{Kind: platformio.SegmentText, Text: "hello"},
		{Kind: platformio.SegmentImage, Extra: map[string]string{"binary_data_base64": base64.StdEncoding.EncodeToString([]byte("image"))}},
	}}
	receipt, err := slot.Send(ctx, outbound, platformio.RouteKey{AccountID: "bot"})
	if err != nil || receipt.Status != platformio.DeliveryStatusSent {
		t.Fatalf("send failed: %+v, %v", receipt, err)
	}
	first, second := <-wire, <-wire
	if first["message_type"] != float64(2) || first["message_state"] != float64(2) || first["context_token"] != "context" ||
		len(first["item_list"].([]any)) != 1 || first["client_id"] == second["client_id"] || second["client_id"] != receipt.ExternalMessageID {
		t.Fatalf("wrong Weixin outbound payloads: %#v %#v", first, second)
	}
	if item := second["item_list"].([]any)[0].(map[string]any); item["type"] != float64(2) {
		t.Fatalf("image was replaced by text: %#v", item)
	}
	deadline := time.After(time.Second)
	for {
		state := conn.Store.State()
		if state.Cursor == "after" {
			if !reflect.DeepEqual(state.Seen, []string{"bot:peer:9007199254740993"}) {
				t.Fatalf("incorrect persisted seen IDs: %v", state.Seen)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("Weixin cursor was not committed")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestNativeAdapterQQOfficialAPI(t *testing.T) {
	wire := make(chan map[string]any, 2)
	stopGateway := make(chan struct{})
	fileBytes := []byte("\x89PNG\r\n\x1a\n")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			fmt.Fprint(w, `{"access_token":"local-token","expires_in":"7200"}`)
		case "/gateway":
			fmt.Fprint(w, `{"url":"wss://grouppro.grouppro.qq.com/ws"}`)
		case "/ws":
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.CloseNow()
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"op":0,"t":"READY","s":1,"d":{"session_id":"session","user":{"id":"bot","username":"麦麦"}}}`)); err != nil {
				t.Error(err)
				return
			}
			update := fmt.Sprintf(`{"op":0,"t":"GROUP_MESSAGE_CREATE","s":2,"d":{"id":9007199254740993,"group_openid":"group","author":{"member_openid":"peer"},"timestamp":%q,"content":"<faceType=1>","attachments":[{"content_type":"image/png","url":"//grouppro.grouppro.qq.com/sticker"}],"message_scene":{"ext":["msg_idx=REFIDX_abc"]}}}`, time.Now().UTC().Format(time.RFC3339Nano))
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(update)); err != nil {
				t.Error(err)
				return
			}
			<-stopGateway
		case "/sticker":
			_, _ = w.Write(fileBytes)
		case "/v2/groups/group/upload_prepare":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload["file_size"] != "8" || payload["file_type"] != float64(1) {
				t.Errorf("QQ upload types changed: %#v", payload)
			}
			fmt.Fprint(w, `{"upload_id":"upload","block_size":"128","parts":[{"index":1,"presigned_url":"https://grouppro.grouppro.qq.com/put"}]}`)
		case "/put":
			body, _ := io.ReadAll(r.Body)
			if r.Method != http.MethodPut || r.Header.Get("Authorization") != "" || string(body) != string(fileBytes) {
				t.Errorf("invalid QQ signed upload: %s, %d bytes", r.Method, len(body))
			}
		case "/v2/groups/group/upload_part_finish":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload["part_index"] != float64(1) || payload["block_size"] != "8" {
				t.Errorf("QQ upload part types changed: %#v", payload)
			}
			fmt.Fprint(w, `{}`)
		case "/v2/groups/group/files":
			fmt.Fprint(w, `{"file_info":"media"}`)
		case "/v2/groups/group/messages":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			wire <- payload
			fmt.Fprint(w, `{"id":9007199254740995,"ext_info":{"ref_idx":"REFIDX_out"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer close(stopGateway)

	original := http.DefaultTransport
	testTransport := server.Client().Transport.(*http.Transport).Clone()
	testTransport.TLSClientConfig.ServerName = server.Certificate().DNSNames[0]
	dialer := &net.Dialer{}
	testTransport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, server.Listener.Addr().String())
	}
	http.DefaultTransport = testTransport
	defer func() { http.DefaultTransport = original }()

	inbound := make(chan map[string]any, 1)
	settings := qq_official.DefaultSettings()
	settings.Plugin.Enabled = true
	settings.QQOfficial.AppID, settings.QQOfficial.AppSecret = "local-app", "local-secret"
	settings.QQOfficial.APIBaseURL = "https://grouppro.grouppro.qq.com"
	dir := t.TempDir()
	adapter := qq_official.NewAdapter(settings, &qq_official.HostHooks{
		DataDir: func() string { return dir },
		Inbound: nativeAdapterSink(func(message map[string]any) error {
			inbound <- message
			return nil
		}),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := adapter.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer adapter.Stop(ctx)
	select {
	case message := <-inbound:
		parts := message["raw_message"].([]map[string]any)
		if message["message_id"] != "9007199254740993" || message["is_emoji"] != true || len(parts) != 1 ||
			parts[0]["type"] != "emoji" || parts[0]["binary_data_base64"] != base64.StdEncoding.EncodeToString(fileBytes) {
			t.Fatalf("invalid QQ sticker payload: %#v", message)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	out := platformio.OutboundMessageEnvelope{
		TargetID: "group", ChatType: platformio.ChatTypeGroup, ExternalReplyToMessageID: "9007199254740993",
		Segments: []platformio.Segment{
			{Kind: platformio.SegmentReply, Extra: map[string]string{"id": "9007199254740993"}},
			{Kind: platformio.SegmentText, Text: "你好"},
			{Kind: platformio.SegmentImage, Extra: map[string]string{"binary_data_base64": base64.StdEncoding.EncodeToString(fileBytes)}},
		},
	}
	receipt, err := adapter.Send(ctx, out, platformio.RouteKey{Platform: "qq", AccountID: "bot"})
	if err != nil || receipt.ExternalMessageID != "9007199254740995" {
		t.Fatalf("QQ send failed: %+v: %v", receipt, err)
	}
	first, second := <-wire, <-wire
	ref, _ := first["message_reference"].(map[string]any)
	if first["msg_type"] != float64(0) || first["msg_seq"] != float64(1) || ref["message_id"] != "REFIDX_abc" ||
		second["msg_type"] != float64(7) || second["msg_seq"] != float64(2) || second["message_reference"] != nil {
		t.Fatalf("wrong QQ message bodies: %#v, %#v", first, second)
	}
}

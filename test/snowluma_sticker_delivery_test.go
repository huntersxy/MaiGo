package test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"MaiGo/internal/maisaka/builtin_tool"
	"MaiGo/internal/platform_io"
	"MaiGo/internal/platform_io/drivers/snowluma"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestPrivateReplySendsStickerBytesOverSnowLuma(t *testing.T) {
	const gif = "R0lGODlhAQABAIAAAAUEBAAAACwAAAAAAQABAAACAkQBADs="
	image, err := base64.StdEncoding.DecodeString(gif)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sticker.gif")
	if err := os.WriteFile(path, image, 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		sendCount := 0
		for {
			var request struct {
				Action string `json:"action"`
				Echo   string `json:"echo"`
				Params struct {
					UserID  string `json:"user_id"`
					Message []struct {
						Type string `json:"type"`
						Data struct {
							File    string `json:"file"`
							SubType int    `json:"sub_type"`
							Summary string `json:"summary"`
						} `json:"data"`
					} `json:"message"`
				} `json:"params"`
			}
			if err := wsjson.Read(r.Context(), conn, &request); err != nil {
				return
			}
			response := map[string]any{"status": "ok", "retcode": 0, "echo": request.Echo}
			switch request.Action {
			case "get_login_info":
				response["data"] = map[string]any{"user_id": 10001}
			case "get_version_info":
				response["data"] = map[string]any{"app_name": "NapCat"}
			case "send_private_msg":
				sendCount++
				if request.Params.UserID != "12345" || len(request.Params.Message) != 1 {
					t.Errorf("unexpected private message parameters: %+v", request.Params)
					return
				}
				segment := request.Params.Message[0]
				if segment.Type != "image" || segment.Data.SubType != 1 || segment.Data.File != "base64://"+gif || segment.Data.Summary != "[动画表情]" {
					t.Errorf("unexpected OneBot sticker payload: %+v", segment)
				}
				response["data"] = map[string]any{"message_id": 9876}
				if sendCount == 2 {
					response["retcode"] = 1200
				}
			default:
				t.Errorf("unexpected OneBot action: %s", request.Action)
				return
			}
			if err := wsjson.Write(r.Context(), conn, response); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	host, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	settings := snowluma.DefaultSettings()
	settings.Plugin.Enabled = true
	settings.Client.Server = host
	settings.Client.Port, err = strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{}, 1)
	adapter := snowluma.NewAdapter(settings, &snowluma.HostHooks{RegisterDriver: func(context.Context, platformio.Adapter) error {
		ready <- struct{}{}
		return nil
	}})
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	if err := adapter.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer adapter.Stop(ctx)
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("OneBot handshake did not complete")
	}

	tool, err := tools.NewReplyTool()
	if err != nil {
		t.Fatal(err)
	}
	tool, err = tools.ApplyMaisakaToolSpec("reply", tool)
	if err != nil {
		t.Fatal(err)
	}
	structured := tool.(tools.StructuredToolResultInvoker)
	ctx = tools.WithToolContext(ctx, &tools.ToolContext{
		ConversationKind: "private", Platform: "qq", AccountID: "10001", TargetID: 12345,
		ResolveBotAccount: func(context.Context, string, string) (string, error) { return "10001", nil },
		ReplySendCallback: func(context.Context, int64, string, int64, []int64) (tools.ReplySendOutcome, error) {
			return tools.ReplySendOutcome{Sent: true}, nil
		},
		ResolveEmojiAttachment: func(context.Context, any) (tools.EmojiAttachment, error) {
			return tools.EmojiAttachment{Path: path}, nil
		},
		SendEmojiAttachment: func(ctx context.Context, targetID int64, emoji tools.EmojiAttachment) error {
			receipt, err := adapter.Send(ctx, platformio.OutboundMessageEnvelope{
				ChatType: platformio.ChatTypePrivate, TargetID: strconv.FormatInt(targetID, 10),
				Segments: []platformio.Segment{{Kind: platformio.SegmentImage, Extra: map[string]string{"file": emoji.Path, "sub_type": "1"}}},
			}, platformio.RouteKey{})
			if err == nil && (receipt.Status != platformio.DeliveryStatusSent || receipt.ExternalMessageID != "9876") {
				return fmt.Errorf("unexpected delivery receipt: %+v", receipt)
			}
			return err
		},
	})
	const arguments = `{"chat_anlyze":"用户要表情包","msg_id":"1","content":"给你～","_replyer_generated":true,"attach_emoji":{"emotion":"可爱"}}`
	for _, want := range []struct{ status, text string }{{"sent", "表情包已发送"}, {"send_failed", "表情包发送失败"}} {
		result, err := structured.InvokeStructuredToolResult(ctx, arguments)
		if err != nil || !result.Success || result.Metadata["emoji_attachment_status"] != want.status || !strings.Contains(result.Content, want.text) {
			t.Fatalf("wanted %s: %+v, %v", want.status, result, err)
		}
	}
}

package runtime

import (
	"testing"

	"MaiGo/internal/platform_io/drivers/qq_official"
)

func TestQQOfficialPrivateAttachmentsReachImagePipeline(t *testing.T) {
	const imageURL = "https://example.com/qq-test.png"
	message, _, _, err := qq_official.DecodeMessage("C2C_MESSAGE_CREATE", map[string]any{
		"id":        "message-1",
		"timestamp": "2026-11-20T18:15:00+08:00",
		"author":    map[string]any{"user_openid": "user-1"},
	}, "bot-1", "Bot", "", "", []map[string]any{{"type": "image", "data": imageURL}}, []string{"[图片]"})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeExternalPluginMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ConversationKind != "private" || len(decoded.Images) != 1 || len(decoded.MessageParts) != 1 {
		t.Fatal("QQ official image-only message lost its attachment")
	}
	if decoded.Images[0].URL != imageURL || decoded.MessageParts[0].Kind != "image" {
		t.Fatal("host did not retain the QQ official image URL and segment")
	}
}

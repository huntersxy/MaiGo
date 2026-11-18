package runtime

import (
	"testing"

	"github.com/jellydator/ttlcache/v3"

	memory "MaiGo/internal/common/database"
	"MaiGo/internal/maisaka"
	"MaiGo/internal/platform_io"
	"MaiGo/internal/platform_io/drivers/onebot"
)

func newIdentityTestAgent() *Agent {
	return &Agent{externalIdentities: newAgentTTLCache[string, string](externalIdentityCapacity, externalIdentityTTL)}
}

func TestReplySegmentUsesExternalIdentity(t *testing.T) {
	agent := newIdentityTestAgent()

	segment, ok := agent.replySegment(memory.ConversationKindGroup, 1, 100)
	if !ok || segment.Kind != platformio.SegmentReply || segment.Text != "100" || segment.Extra["id"] != "100" {
		t.Fatalf("内部编号引用段 = %+v, ok=%v", segment, ok)
	}

	agent.externalIdentities.Set(externalIdentityCacheKey(memory.ConversationKindGroup, 1, "reply", 100), "platform-100", ttlcache.DefaultTTL)
	segment, ok = agent.replySegment(memory.ConversationKindGroup, 1, 100)
	if !ok || segment.Text != "platform-100" || segment.Extra["id"] != "platform-100" {
		t.Fatalf("平台编号引用段 = %+v, ok=%v", segment, ok)
	}
}

func TestRememberExternalIdentitiesRecordsInboundMessageID(t *testing.T) {
	agent := newIdentityTestAgent()
	agent.rememberExternalIdentities(&onebot.ConversationMessage{
		ConversationKind:  memory.ConversationKindGroup,
		TargetID:          1,
		MessageID:         42,
		ExternalMessageID: "ext-42",
	})
	if got := agent.externalIdentityFor(memory.ConversationKindGroup, 1, "reply", 42); got != "ext-42" {
		t.Fatalf("入站消息平台 ID = %q, 期望 ext-42", got)
	}
}

func TestMonitorMessageReplyPreviewLooksUpSource(t *testing.T) {
	source := maisaka.ContextMessage{
		MessageID: "42",
		UserID:    "u1",
		Content:   "原文内容",
		Metadata:  map[string]any{"sender_cardname": "群名片"},
	}
	preview := monitorMessageReplyPreview(maisaka.ContextMessage{
		Metadata: map[string]any{"reply_to": "42"},
	}, func(messageID string) (maisaka.ContextMessage, bool) {
		if messageID == "42" {
			return source, true
		}
		return maisaka.ContextMessage{}, false
	})
	if preview["content"] != "原文内容" || preview["sender_name"] != "群名片" {
		t.Fatalf("引用预览 = %+v", preview)
	}
}

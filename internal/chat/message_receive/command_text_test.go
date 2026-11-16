package messagereceive

import (
	"testing"

	"MaiGo/internal/common/data_models/messagecomponent"
)

func TestCommandTextRemovesOnlyLeadingBotMention(t *testing.T) {
	account := "bot"
	text := "@麦麦 /list chat"
	nickname := "麦麦"
	message := &SessionMessage{
		AccountID:          &account,
		ProcessedPlainText: &text,
		RawMessage: &messagecomponent.MessageSequence{Components: []messagecomponent.Component{
			messagecomponent.AtComponent{TargetUserID: account, Nickname: &nickname},
			messagecomponent.TextComponent{Text: " /list chat"},
		}},
	}
	if got := message.CommandText(); got != "/list chat" {
		t.Fatalf("应移除机器人前导 @: %q", got)
	}

	other := "@群友 /list chat"
	message.ProcessedPlainText = &other
	if got := message.CommandText(); got != other {
		t.Fatalf("非机器人 @ 不应移除: %q", got)
	}

	rewritten := "@麦麦 /list chat"
	message.ProcessedPlainText = &rewritten
	message.RawMessage.Components[0] = messagecomponent.AtComponent{TargetUserID: "someone", Nickname: &nickname}
	if got := message.CommandText(); got != rewritten {
		t.Fatalf("组件不匹配时不应改写正文: %q", got)
	}
}

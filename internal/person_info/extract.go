package personinfo

import (
	"context"
	"fmt"
	"strings"

	"MaiGo/internal/common/llmjson"
	"MaiGo/internal/llm_models"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// FactExtractor 人物事实 LLM 提取（TaskLearner→utils）
type FactExtractor struct {
	Router *llm.Router
}

// ExtractedFact 单条提取结果
type ExtractedFact struct {
	Fact              string
	EvidenceMessageID string
	EvidenceQuote     string
}

// ExtractFactsFromReply 从用户证据与机器人回复中提取稳定事实；MAIBOT_PERSON_LLM 关闭时返回 nil
func (ex *FactExtractor) ExtractFactsFromReply(ctx context.Context, personName, replyText, userEvidenceText string) ([]ExtractedFact, error) {
	if ex == nil || ex.Router == nil || !PersonLLMEnabled() {
		return nil, nil
	}
	personName = strings.TrimSpace(personName)
	replyText = strings.TrimSpace(replyText)
	userEvidenceText = strings.TrimSpace(userEvidenceText)
	if personName == "" || replyText == "" || userEvidenceText == "" {
		return nil, nil
	}

	prompt := buildPersonFactExtractPrompt(personName, replyText, userEvidenceText)
	raw, err := ex.generate(ctx, prompt)
	if err != nil {
		zap.L().Debug("人物事实提取模型调用失败", zap.Error(err))
		return nil, nil
	}
	return parseExtractedFacts(raw), nil
}

func buildPersonFactExtractPrompt(personName, replyText, userEvidenceText string) string {
	return fmt.Sprintf(`你要从用户原始发言中提取“关于%s的稳定事实”。

目标人物：%s
用户原始发言证据：
%s

机器人回复：
%s

请只提取满足以下条件的事实：
1. 事实值必须能被“目标用户原始发言”直接支持，不能只来自机器人回复或邻近上下文。
2. 明确是关于目标人物本人的信息。
3. 具有相对稳定性，可以作为长期记忆保存。
4. 用简洁中文陈述句表达。
5. 如果用户原始发言中出现“我/我的/自己”，默认指目标人物，请先改写成关于目标人物的第三人称事实再输出。
6. 邻近上下文只能用于补全目标用户短答、省略、被追问的问题或代词指向。
7. 如果完整事实只能靠机器人回复或邻近上下文中的新增事实值成立，而目标用户原始发言没有确认或给出该事实值，不要提取。

不要提取机器人的情绪、计划、临时动作、客套话、猜测或与目标人物无关的信息。

严格输出 JSON 数组，每项包含 fact、evidence_message_id、evidence_quote。如果没有可写入的事实，输出 []`, personName, personName, userEvidenceText, replyText)
}

func (ex *FactExtractor) generate(ctx context.Context, userBody string) (string, error) {
	_, client, err := ex.Router.NewClientForTask(ctx, llm.TaskLearner)
	if err != nil {
		_, client, err = ex.Router.NewClientForTask(ctx, llm.TaskUtils)
	}
	if err != nil {
		return "", err
	}
	msg, err := client.Generate(ctx, []*schema.AgenticMessage{
		schema.UserAgenticMessage(userBody),
	})
	if err != nil {
		return "", err
	}
	return agenticPlainText(msg), nil
}

func agenticPlainText(msg *schema.AgenticMessage) string {
	if msg == nil {
		return ""
	}
	var b strings.Builder
	for _, block := range msg.ContentBlocks {
		if block == nil {
			continue
		}
		switch block.Type {
		case schema.ContentBlockTypeUserInputText:
			if block.UserInputText != nil {
				b.WriteString(block.UserInputText.Text)
			}
		case schema.ContentBlockTypeAssistantGenText:
			if block.AssistantGenText != nil {
				b.WriteString(block.AssistantGenText.Text)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func parseExtractedFacts(raw string) []ExtractedFact {
	decoded, err := llmjson.Parse(raw, true, true)
	if err != nil {
		return nil
	}
	payload, ok := decoded.([]any)
	if !ok {
		return nil
	}
	out := make([]ExtractedFact, 0, len(payload))
	seen := make(map[string]struct{})
	for _, item := range payload {
		fact, evID, evQuote := "", "", ""
		switch m := item.(type) {
		case map[string]any:
			fact = strings.TrimSpace(fmt.Sprint(m["fact"]))
			evID = strings.TrimSpace(fmt.Sprint(m["evidence_message_id"]))
			evQuote = strings.TrimSpace(fmt.Sprint(m["evidence_quote"]))
		default:
			fact = strings.TrimSpace(fmt.Sprint(item))
		}
		if len(fact) < 4 {
			continue
		}
		if _, dup := seen[fact]; dup {
			continue
		}
		seen[fact] = struct{}{}
		out = append(out, ExtractedFact{Fact: fact, EvidenceMessageID: evID, EvidenceQuote: evQuote})
		if len(out) >= 5 {
			break
		}
	}
	return out
}

// LooksEphemeralReply 过滤过短或纯客套机器人回复
// looksEphemeralMarkers 寒暄/短应答标记，对齐 Python memory_flow_service.py:487-498 的 ephemeral_markers
var looksEphemeralMarkers = []string{
	"哈哈", "好的", "收到", "嗯嗯", "晚安",
	"早安", "拜拜", "谢谢", "在吗", "？",
}

// LooksEphemeralReply 判断回复是否为无需提取事实的寒暄
//
// 严格对齐 Python memory_flow_service.py:482-500 `_looks_ephemeral`：
// 空文本视为寒暄；文本长度 ≤ 8 且包含任一标记词才视为寒暄，否则不是。
// 注意 Python 只在“短文本 + 命中标记词”时返回 True，长文本即使命中也不算。
func LooksEphemeralReply(text string) bool {
	content := strings.TrimSpace(text)
	if content == "" {
		return true
	}
	if len([]rune(content)) > 8 {
		return false
	}
	for _, marker := range looksEphemeralMarkers {
		if strings.Contains(content, marker) {
			return true
		}
	}
	return false
}

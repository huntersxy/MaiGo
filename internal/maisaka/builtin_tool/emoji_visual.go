package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"

	"maibot-go/internal/emoji_system"
	"maibot-go/internal/maisaka"
)

// 对齐 Python 内置工具的表情文案与常量
const (
	// emojiSelectionPromptName 表情选择子代理的 Prompt 模板名
	emojiSelectionPromptName = "emoji_selection"
	// emojiSuccessMessage 对齐 send_emoji.py:42
	emojiSuccessMessage = "表情包发送成功"
	// emojiVLMNotConfiguredMessage 对齐 send_emoji.py:43
	emojiVLMNotConfiguredMessage = "错误，没有配置视觉模型，无法使用表情包功能"
	// emojiVisualPlannerRequiredMessage 对齐 show_emoji_list.py:50
	emojiVisualPlannerRequiredMessage = "show_emoji_list 需要启用 Planner 视觉能力。"
	// emojiLibraryEmptyMessage 对齐 show_emoji_list.py:53
	emojiLibraryEmptyMessage = "当前表情包库中没有可用表情。"
	// emojiMissingVisualModelFragment 对齐 send_emoji.py:_is_missing_visual_model_error 的第二个片段
	emojiMissingVisualModelFragment = "未找到名为 '' 的模型"
	// emojiSubAgentContextLimit 对齐 send_emoji.py:38
	emojiSubAgentContextLimit = 12
	// emojiCollageMIME 拼图固定按 PNG 编码
	emojiCollageMIME = "image/png"
)

// emojiIndexCounter 进程级单调序号，对齐 show_emoji_list.py:26 的 itertools.count(1)
// 历史只存在于进程内，聊天切换或裁剪后也不复用旧编号
var emojiIndexCounter atomic.Int64

// nextEmojiIndices 一次性分配连续序号，对齐 show_emoji_list.py:56
func nextEmojiIndices(count int) []int {
	if count <= 0 {
		return nil
	}
	indices := make([]int, count)
	for position := range indices {
		indices[position] = int(emojiIndexCounter.Add(1))
	}
	return indices
}

// emojiCandidateLimit 对齐 send_emoji.py:73-79：配置值 clamp 到 1..64
func emojiCandidateLimit() int {
	settings := emojisystem.LoadEmojiSettings()
	limit := settings.EmojiSendNum
	if limit < 1 {
		limit = 1
	}
	if limit > emojisystem.CollageMaxCandidateCount {
		limit = emojisystem.CollageMaxCandidateCount
	}
	return limit
}

// buildEmojiCollage 读取候选表情原始字节并拼接成带序号角标的 PNG
// 单个文件读取失败时保留占位图，不静默丢弃整个拼图
func buildEmojiCollage(ctx context.Context, mgr *emojisystem.Manager, candidates []emojisystem.Emoji, startIndex int) ([]byte, error) {
	if mgr == nil {
		return nil, fmt.Errorf("表情包子系统未初始化")
	}
	images := make([][]byte, 0, len(candidates))
	for index := range candidates {
		data, err := mgr.ReadEmojiBytes(&candidates[index])
		if err != nil {
			images = append(images, nil)
			continue
		}
		images = append(images, data)
	}
	return emojisystem.MergeEmojiTiles(images, startIndex)
}

// maisakaRuntimeProvider 由宿主会话实现，暴露当前会话的 Maisaka 运行时
type maisakaRuntimeProvider interface {
	MaisakaRuntime() *maisaka.Runtime
}

// emojiToolRuntime 读取工具上下文绑定的 Maisaka 运行时，未接线时为 nil
func emojiToolRuntime(ctx context.Context) *maisaka.Runtime {
	tc := GetToolContext(ctx)
	if tc == nil || tc.Session == nil {
		return nil
	}
	provider, ok := tc.Session.(maisakaRuntimeProvider)
	if !ok {
		return nil
	}
	return provider.MaisakaRuntime()
}

// emojiPassiveReplyer 对齐 show_emoji_list.py:62：非 focus 组且启用被动回复实验模式
func emojiPassiveReplyer(ctx context.Context) bool {
	runtime := emojiToolRuntime(ctx)
	if runtime == nil || runtime.FocusModeActive() {
		return false
	}
	return runtime.PassiveReplyerActive()
}

// emojiVisualPlannerActive 对齐 resolve_enable_visual_planner
func emojiVisualPlannerActive(ctx context.Context) bool {
	runtime := emojiToolRuntime(ctx)
	if runtime == nil {
		return false
	}
	return runtime.VisualPlannerActive()
}

// emojiSelectionText 对齐 show_emoji_list.py:63-70 的两分支文案
func emojiSelectionText(firstIndex, lastIndex int, passiveReplyer bool) string {
	text := "表情包选择图，序号 " + strconv.Itoa(firstIndex) + "–" + strconv.Itoa(lastIndex) + "。"
	if passiveReplyer {
		return text + "请在最终发言指导中说明表情意图；并行回复补充器会选择序号并安排发送位置。"
	}
	return text + "请在 reply.attach_emoji 中填写选中的整数序号；表情包将于文字回复后单独发送。"
}

// emojiHashesByIndex 把序号到 file_hash 的映射转成历史消息可消费的形式
func emojiHashesByIndex(indices []int, candidates []emojisystem.Emoji) map[int]string {
	hashes := make(map[int]string, len(indices))
	for position, index := range indices {
		if position >= len(candidates) {
			break
		}
		hashes[index] = candidates[position].FileHash
	}
	return hashes
}

// emojiCollageBase64 拼图按 base64 进入上下文
func emojiCollageBase64(collage []byte) string {
	return base64.StdEncoding.EncodeToString(collage)
}

// buildEmojiMonitorDetail 对齐 send_emoji.py:_build_send_emoji_monitor_detail
// 只保留非空字段，键顺序固定
func buildEmojiMonitorDetail(requestMessageCount int, reasoningText, outputText string, metrics map[string]any, extraSections []map[string]any) map[string]any {
	detail := map[string]any{}
	if requestMessageCount > 0 {
		detail["request_message_count"] = requestMessageCount
	}
	if trimmed := strings.TrimSpace(reasoningText); trimmed != "" {
		detail["reasoning_text"] = trimmed
	}
	if trimmed := strings.TrimSpace(outputText); trimmed != "" {
		detail["output_text"] = trimmed
	}
	if len(metrics) > 0 {
		detail["metrics"] = metrics
	}
	normalized := make([]map[string]any, 0, len(extraSections))
	for _, section := range extraSections {
		title, _ := section["title"].(string)
		content, _ := section["content"].(string)
		title = strings.TrimSpace(title)
		content = strings.TrimSpace(content)
		if title == "" || content == "" {
			continue
		}
		normalized = append(normalized, map[string]any{"title": title, "content": content})
	}
	if len(normalized) > 0 {
		detail["extra_sections"] = normalized
	}
	return detail
}

// emojiSendOutcome 表情发送结果，字段对齐 MaisakaEmojiSendResult
type emojiSendOutcome struct {
	Success        bool
	Message        string
	Description    string
	Emotions       []string
	MatchedEmotion string
}

// buildEmojiMonitorMetadata 对齐 send_emoji.py:_build_send_emoji_monitor_metadata
// 成功与失败都追加「表情发送结果」段；Python 没有 send_result 顶层键
func buildEmojiMonitorMetadata(selection map[string]any, send *emojiSendOutcome, errorMessage string) map[string]any {
	detail := map[string]any{}
	sections := make([]map[string]any, 0, 2)
	if selection != nil {
		if raw, ok := selection["monitor_detail"].(map[string]any); ok {
			for key, value := range raw {
				detail[key] = value
			}
		}
		if existing, ok := detail["extra_sections"].([]map[string]any); ok {
			sections = append(sections, existing...)
		} else if existingAny, ok := detail["extra_sections"].([]any); ok {
			for _, item := range existingAny {
				if section, ok := item.(map[string]any); ok {
					sections = append(sections, section)
				}
			}
		}
		delete(detail, "extra_sections")
	}
	if send != nil {
		resultText := strings.TrimSpace(send.Message)
		if resultText == "" {
			if send.Success {
				resultText = "成功"
			} else {
				resultText = "失败"
			}
		}
		lines := []string{
			"命中情绪：" + emojiOrFallback(send.MatchedEmotion, "未命中"),
			"表情描述：" + emojiOrFallback(send.Description, "无描述"),
			"情绪标签：" + emojiOrFallback(strings.Join(send.Emotions, "、"), "无"),
			"发送结果：" + resultText,
		}
		sections = append(sections, map[string]any{"title": "表情发送结果", "content": strings.Join(lines, "\n")})
	} else if trimmed := strings.TrimSpace(errorMessage); trimmed != "" {
		sections = append(sections, map[string]any{"title": "表情发送结果", "content": "发送结果：" + trimmed})
	}
	if len(sections) > 0 {
		detail["extra_sections"] = sections
	}
	metadata := map[string]any{}
	if len(detail) > 0 {
		metadata["monitor_detail"] = detail
	}
	if selection != nil {
		if uri, ok := selection["prompt_html_uri"].(string); ok && strings.TrimSpace(uri) != "" {
			metadata["prompt_html_uri"] = strings.TrimSpace(uri)
		}
	}
	return metadata
}

func emojiOrFallback(value, fallback string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return fallback
}

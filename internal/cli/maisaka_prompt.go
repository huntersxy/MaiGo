package cli

import (
	"fmt"
	"strings"

	"MaiGo/internal/prompt"
)

// maisakaPreviewTemplates 迁移期 CLI 默认展示的 Maisaka 模板（对齐 Python heartflow 主链路）
var maisakaPreviewTemplates = []string{"maisaka_replyer", "maisaka_chat_focus"}

func maisakaDemoVars() map[string]string {
	return map[string]string{
		"identity":                   "你是 MaiSaka CLI 调试 Bot",
		"reply_style":                "口语化、简短",
		"group_chat_attention_block": "",
		"replyer_output_instruction": "只输出给用户看的正文，不要解释规则",
		"bot_name":                   "麦麦",
		"bot_alias_notice":           "",
		"personality":                "友好、好奇",
		"behavior_style":             "先思考再行动",
		"bot_nickname_notice":        "",
		"query_memory_rule":          "",
		"subagent_rule":              "",
	}
}

func renderMaisakaPrompt(pm *prompt.Manager, name string) (string, error) {
	out, err := pm.Render(name, maisakaDemoVars())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func printMaisakaPromptPreview(w fmtWriter, pm *prompt.Manager, name string, maxRunes int) {
	text, err := renderMaisakaPrompt(pm, name)
	if err != nil {
		fmt.Fprintf(w, "  [%s] 渲染失败: %v\n", name, err)
		return
	}
	fmt.Fprintf(w, "── %s ──\n", name)
	fmt.Fprintln(w, truncateRunes(text, maxRunes))
	fmt.Fprintln(w)
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "\n…（已截断，使用 /prompt <名称> 查看完整渲染）"
}

// fmtWriter 抽象 stdout/stderr
type fmtWriter interface {
	Write([]byte) (int, error)
}

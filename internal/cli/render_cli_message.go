package cli

import (
	"fmt"
	"os"
	"strings"

	"MaiGo/internal/core/localoperator"

	"go.uber.org/zap"
)

// MaisakaCLIPlatformName 对齐 Python CLI_PLATFORM_NAME
const MaisakaCLIPlatformName = localoperator.MaisakaCLIPlatform

// CLIMessageNickname CLI 出站面板的机器人昵称；为空时回退环境变量与 MaiSaka
var CLIMessageNickname string

// SetCLIMessageNickname 注入机器人昵称，供 render_cli_message 使用
func SetCLIMessageNickname(nickname string) {
	CLIMessageNickname = strings.TrimSpace(nickname)
}

// RenderCLIMessage 将 CLI 私聊消息输出到终端，对齐 Python render_cli_message
func RenderCLIMessage(content string) {
	RenderCLIMessageWithTitle(content, "")
}

// RenderCLIMessageWithTitle 使用指定标题输出 CLI 消息
func RenderCLIMessageWithTitle(content, title string) {
	RenderCLIMessageTo(DefaultConsole, content, title)
}

// RenderCLIMessageTo 使用指定控制台输出 CLI 消息
func RenderCLIMessageTo(console *Console, content, title string) {
	if console == nil {
		console = DefaultConsole
	}
	previewText := strings.TrimSpace(content)
	if previewText == "" {
		previewText = "..."
	}
	resolvedTitle := strings.TrimSpace(title)
	if resolvedTitle == "" {
		resolvedTitle = resolveCLIMessageNickname()
	}
	console.Panel(console.RenderMarkdown(previewText), PanelOptions{
		Title:             resolvedTitle,
		BorderStyle:       StyleMagenta,
		PaddingTop:        1,
		PaddingHorizontal: 2,
		Box:               RoundedBox,
	})
	zap.L().Info(fmt.Sprintf("[CLI] 已将消息输出到终端: content=%q", previewText))
}

// resolveCLIMessageNickname 对齐 title or bot.nickname.strip() or "MaiSaka"
func resolveCLIMessageNickname() string {
	if nickname := strings.TrimSpace(CLIMessageNickname); nickname != "" {
		return nickname
	}
	if nickname := strings.TrimSpace(os.Getenv("MAIBOT_BOT_NICKNAME")); nickname != "" {
		return nickname
	}
	return "MaiSaka"
}

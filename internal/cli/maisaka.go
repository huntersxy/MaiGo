package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"MaiGo/internal/prompt"
)

const maisakaPreviewMax = 480

// runMaisaka 保留为 prompt/LLM 预览 REPL（开发调试用）。
// 正式入口 `maibot maisaka` 已改为 prepareRun + StartBot + BufferCLI（见 Dispatch / RunMaisakaCLI）。
func runMaisaka(directory string) (int, error) {
	if strings.TrimSpace(directory) != "" {
		if err := os.Setenv(InstanceDirEnv, directory); err != nil {
			return 2, fmt.Errorf("设置 %s: %w", InstanceDirEnv, err)
		}
	}

	pm, err := prompt.NewManager()
	if err != nil {
		return 1, fmt.Errorf("加载 prompt: %w", err)
	}
	if loc := strings.TrimSpace(os.Getenv("MAIBOT_LOCALE")); loc != "" {
		_ = pm.SetLocale(loc)
	}
	if directory != "" {
		customRoot := prompt.CustomPromptsRoot(directory)
		if err := pm.LoadFromDir(customRoot); err != nil {
			fmt.Fprintf(os.Stderr, "警告: 加载 custom_prompts 失败: %v\n", err)
		}
	}

	llmBridge, err := tryMaisakaLLM(directory)
	if err != nil {
		return 1, fmt.Errorf("初始化 LLM: %w", err)
	}

	console := NewStdConsole()
	printMaisakaBanner(console, llmBridge)
	console.PrintStyled(StyleMuted, "迁移期说明：本子命令仍是 prompt/LLM 预览；Python BufferCLI 的 Heartflow 环见 BufferCLI（尚未挂载）。")
	console.PrintStyled(StyleMuted, "命令: /help  /prompt <名称>  /preview  /exit ；直接输入一行可尝试 LLM 回复（需配置模型）")
	console.Println()
	for _, name := range maisakaPreviewTemplates {
		printMaisakaPromptPreview(console, pm, name, maisakaPreviewMax)
	}

	reader := NewInputReader(os.Stdin)
	reader.Start()
	defer reader.Close()
	for {
		console.Prompt(StyleBoldCyan, "> ")
		line, err := reader.Next(context.Background())
		if err != nil {
			if errors.Is(err, io.EOF) {
				console.Println()
				console.PrintStyled(StyleMuted, "再见")
				return 0, nil
			}
			return 1, fmt.Errorf("读取输入: %w", err)
		}
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "/") {
			exitCode, cmdErr := handleMaisakaSlashCommand(console, console, pm, llmBridge, text)
			if cmdErr != nil {
				console.Errorf("错误: %v\n", cmdErr)
			}
			if exitCode >= 0 {
				return exitCode, nil
			}
			continue
		}

		if !llmBridge.available() {
			console.PrintStyled(StyleWarning, "LLM 未配置，无法回复。可用 /prompt reply_private_system 查看模板，或配置 MAIBOT_MAISAKA_API_KEY。")
			continue
		}
		systemPrompt, err := renderMaisakaPrompt(pm, "reply_private_system")
		if err != nil {
			console.Errorf("渲染 replyer prompt 失败: %v\n", err)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		reply, err := llmBridge.oneShot(ctx, systemPrompt, text)
		cancel()
		if err != nil {
			console.Errorf("LLM 调用失败: %v\n", err)
			continue
		}
		RenderCLIMessageTo(console, reply, "")
	}
}

func printMaisakaBanner(console *Console, llmBridge *maisakaLLM) {
	label := "未配置"
	if llmBridge != nil && llmBridge.modelLabel != "" {
		label = llmBridge.modelLabel
	}
	content := console.decorate(StyleBoldCyan, "MaiSaka") + console.decorate(StyleMuted, " v2.0") + "\n" +
		console.decorate(StyleMuted, "模型: "+label) + "\n" +
		console.decorate(StyleMuted, "输入内容开始对话 | Ctrl+C 退出")
	console.Panel(content, PanelOptions{
		BorderStyle:       StyleCyan,
		PaddingTop:        1,
		PaddingHorizontal: 2,
		Box:               DoubleEdgeBox,
	})
	console.Println()
}

func handleMaisakaSlashCommand(out, errOut fmtWriter, pm *prompt.Manager, llmBridge *maisakaLLM, line string) (int, error) {
	fields := strings.Fields(line)
	cmd := strings.ToLower(fields[0])
	switch cmd {
	case "/exit", "/quit", "/q":
		fmt.Fprintln(out, "再见")
		return 0, nil
	case "/help", "/h", "/?":
		printMaisakaHelp(out)
		return -1, nil
	case "/preview":
		for _, name := range maisakaPreviewTemplates {
			printMaisakaPromptPreview(out, pm, name, 0)
		}
		return -1, nil
	case "/prompt", "/p":
		if len(fields) < 2 {
			return -1, fmt.Errorf("用法: /prompt <模板名>，例如 /prompt reply_private_system")
		}
		name := fields[1]
		text, err := renderMaisakaPrompt(pm, name)
		if err != nil {
			return -1, err
		}
		fmt.Fprintln(out, text)
		return -1, nil
	case "/prompts":
		for _, n := range pm.Names() {
			if strings.HasPrefix(n, "maisaka_") {
				fmt.Fprintln(out, n)
			}
		}
		return -1, nil
	case "/chat":
		if len(fields) < 2 {
			return -1, fmt.Errorf("用法: /chat <内容>")
		}
		userText := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
		if !llmBridge.available() {
			return -1, fmt.Errorf("LLM 未配置")
		}
		systemPrompt, err := renderMaisakaPrompt(pm, "reply_private_system")
		if err != nil {
			return -1, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		reply, err := llmBridge.oneShot(ctx, systemPrompt, userText)
		if err != nil {
			return -1, err
		}
		if console, ok := out.(*Console); ok {
			RenderCLIMessageTo(console, reply, "")
		} else {
			fmt.Fprintln(out, reply)
		}
		return -1, nil
	default:
		return -1, fmt.Errorf("未知命令 %q，输入 /help 查看", cmd)
	}
}

func printMaisakaHelp(w fmtWriter) {
	fmt.Fprintln(w, "MaiSaka 交互命令（迁移期）:")
	fmt.Fprintln(w, "  /help              显示此帮助")
	fmt.Fprintln(w, "  /preview           渲染默认 Maisaka 模板")
	fmt.Fprintln(w, "  /prompt <名称>     渲染指定 prompt 模板（演示占位变量）")
	fmt.Fprintln(w, "  /prompts           列出 maisaka_* 模板名")
	fmt.Fprintln(w, "  /chat <内容>       使用 reply_private_system 系统提示做单次 LLM")
	fmt.Fprintln(w, "  /exit              退出")
	fmt.Fprintln(w, "  直接输入一行       同 /chat（需 MAIBOT_MAISAKA_API_KEY 或实例 model_config）")
}

package host

// 宿主 CLI：管理工作区内的 Bot，对齐 src/host/cli.py。

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CommandResult CLI 子命令结果
type CommandResult struct {
	Handled  bool
	ExitCode int
}

// RunCommand 执行 maigo host 子命令；handled 为 false 时调用方继续原启动路径
func RunCommand(argv []string) CommandResult {
	if len(argv) == 0 || argv[0] != "host" {
		return CommandResult{}
	}
	tail := argv[1:]
	if len(tail) == 0 {
		fmt.Fprintln(os.Stderr, "错误: host 需要子命令；可用 list/create-bot/adopt/status/open-webui/start/stop/restart/shutdown")
		return CommandResult{Handled: true, ExitCode: 2}
	}
	command := tail[0]
	fs := flag.NewFlagSet("host "+command, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	directory := fs.String("directory", "", "工作区目录，默认当前目录")
	short := fs.String("d", "", "工作区目录（简写）")
	name := fs.String("name", "", "Bot 显示名称")
	botID := fs.String("bot-id", "", "Bot ID")
	source := fs.String("from", "", "旧实例目录")
	undo := fs.String("undo", "", "撤销一次 adopt")
	mode := fs.String("mode", "register", "register 只登记路径；move 同盘重命名")
	installDeps := fs.Bool("install", false, "停止全部 Bot，安装后按期望状态恢复")
	if err := fs.Parse(tail[1:]); err != nil {
		return CommandResult{Handled: true, ExitCode: 2}
	}
	root := *directory
	if root == "" {
		root = *short
	}
	if root == "" {
		workingDir, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			return CommandResult{Handled: true, ExitCode: 2}
		}
		root = workingDir
	}
	root, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return CommandResult{Handled: true, ExitCode: 2}
	}
	root = filepath.Clean(root)
	if err := os.Setenv(InstanceDirEnv, root); err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return CommandResult{Handled: true, ExitCode: 2}
	}
	switch command {
	case "list", "create-bot", "adopt":
		if err := runWorkspaceCommand(root, command, *name, *source, *undo, *mode); err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			return CommandResult{Handled: true, ExitCode: 2}
		}
		return CommandResult{Handled: true}
	case "status", "open-webui", "start", "stop", "restart", "shutdown":
		action := command
		if action == "open-webui" {
			action = "open_webui"
		}
		if command == "shutdown" {
			action = "shutdown"
		}
		if (command == "start" || command == "stop" || command == "restart") && strings.TrimSpace(*botID) == "" {
			fmt.Fprintln(os.Stderr, "错误: 需要 --bot-id")
			return CommandResult{Handled: true, ExitCode: 2}
		}
		result, err := ControlRequest(root, action, *botID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			return CommandResult{Handled: true, ExitCode: 1}
		}
		printJSON(result)
		return CommandResult{Handled: true}
	case "plugin-deps":
		action := "plugin_dependencies"
		if *installDeps {
			action = "install_plugin_dependencies"
		}
		result, err := ControlRequest(root, action, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			return CommandResult{Handled: true, ExitCode: 1}
		}
		printJSON(result)
		return CommandResult{Handled: true}
	default:
		fmt.Fprintf(os.Stderr, "错误: 未知 host 子命令 %s\n", command)
		return CommandResult{Handled: true, ExitCode: 2}
	}
}

func runWorkspaceCommand(root, command, name, source, undo, mode string) error {
	workspace, err := OpenWorkspace(root)
	if err != nil {
		return err
	}
	switch command {
	case "list":
		bots, err := workspace.Registry.ListBots()
		if err != nil {
			return err
		}
		payload := make([]map[string]any, 0, len(bots))
		for _, bot := range bots {
			payload = append(payload, recordPayload(bot))
		}
		printJSON(payload)
		return nil
	case "create-bot":
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("create-bot 需要 --name")
		}
		record, err := CreateBot(workspace, name)
		if err != nil {
			return err
		}
		printJSON(recordPayload(record))
		return nil
	default:
		if undo != "" {
			if err := UndoAdopt(workspace, undo); err != nil {
				return err
			}
			fmt.Printf("已撤销 %s\n", undo)
			return nil
		}
		if strings.TrimSpace(source) == "" {
			return fmt.Errorf("adopt 需要 --from 或 --undo")
		}
		record, err := Adopt(workspace, source, mode, name)
		if err != nil {
			return err
		}
		printJSON(recordPayload(record))
		return nil
	}
}

func recordPayload(record BotRecord) map[string]any {
	return map[string]any{
		"bot_id": record.BotID, "name": record.Name, "directory": record.Directory,
		"owned": record.Owned, "initialization": record.Initialization, "error": record.Error,
		"desired_state": record.DesiredState, "restart_policy": record.RestartPolicy,
	}
}

func printJSON(value any) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return
	}
	fmt.Println(string(encoded))
}

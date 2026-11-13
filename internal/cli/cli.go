package cli

import (
	"fmt"
	"os"

	"maibot-go/internal/common/version"
)

// Dispatch 解析 os.Args；Handled 为 false 时 main 应走原有默认启动路径
func Dispatch(argv []string) Outcome {
	if len(argv) == 0 {
		return Outcome{}
	}
	// argv[0] 为程序名
	args := argv[1:]
	if !isCLISubcommand(args) {
		return Outcome{}
	}

	p, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return Outcome{Handled: true, ExitCode: 2}
	}
	if p.showHelp {
		printUsage(os.Stdout)
		return Outcome{Handled: true, ExitCode: 0}
	}
	if p.showVer {
		fmt.Println(version.String())
		return Outcome{Handled: true, ExitCode: 0}
	}

	switch p.command {
	case cmdInit:
		code, err := runInit(p.directory)
		if err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
		return Outcome{Handled: true, ExitCode: code}
	case cmdRun:
		if err := prepareRun(p.directory); err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			return Outcome{Handled: true, ExitCode: 2}
		}
		return Outcome{Handled: true, StartBot: true}
	case cmdMaisaka:
		if err := prepareRun(p.directory); err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			return Outcome{Handled: true, ExitCode: 2}
		}
		maisakaCLIRequested = true
		return Outcome{Handled: true, StartBot: true, MaisakaCLI: true}
	default:
		return Outcome{}
	}
}

func printUsage(w *os.File) {
	name := programName()
	fmt.Fprintf(w, "MaiBot 实例初始化与运行（Go 迁移版 CLI）\n\n")
	fmt.Fprintf(w, "用法:\n")
	fmt.Fprintf(w, "  %s [--version]\n", name)
	fmt.Fprintf(w, "  %s init [-d 实例目录]\n", name)
	fmt.Fprintf(w, "  %s run     [-d 实例目录]\n", name)
	fmt.Fprintf(w, "  %s maisaka [-d 实例目录]\n\n", name)
	fmt.Fprintf(w, "子命令:\n")
	fmt.Fprintf(w, "  init     初始化实例（保留已有配置）\n")
	fmt.Fprintf(w, "  run      运行实例（校验配置后启动 bot）\n")
	fmt.Fprintf(w, "  maisaka  启动实例并以 BufferCLI（maisaka_cli）接入 Heartflow\n\n")
	fmt.Fprintf(w, "选项:\n")
	fmt.Fprintf(w, "  -d, --directory  实例目录，默认为当前工作目录\n")
	fmt.Fprintf(w, "  -h, --help       显示此帮助\n")
	fmt.Fprintf(w, "  --version        显示版本\n")
}

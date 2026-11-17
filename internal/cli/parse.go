package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// InstanceDirEnv 与 MaiBot src/common/paths.py INSTANCE_DIRECTORY_ENV 一致
const InstanceDirEnv = "MAIBOT_INSTANCE_DIR"

const (
	cmdInit    = "init"
	cmdRun     = "run"
	cmdMaisaka = "maisaka"
)

// Outcome 描述 main 对 CLI 的分支处理
type Outcome struct {
	Handled    bool
	ExitCode   int
	StartBot   bool
	MaisakaCLI bool // StartBot 后挂载 BufferCLI，取代 prompt 预览 stub
	// HostMode run 子命令启动宿主，管理 bots/ 下的 Worker
	HostMode bool
}

// maisakaCLIRequested 由 Dispatch 在 maisaka 子命令成功准备后置位，供 bootstrap 读取
var maisakaCLIRequested bool

// WantMaisakaCLI 报告本次启动是否应挂载 BufferCLI（maibot maisaka）
func WantMaisakaCLI() bool {
	return maisakaCLIRequested
}

// ResetMaisakaCLIRequest 仅测试用：清掉 Dispatch 留下的挂载标志
func ResetMaisakaCLIRequest() {
	maisakaCLIRequested = false
}

type parsed struct {
	command   string
	directory string
	showHelp  bool
	showVer   bool
}

func programName() string {
	return filepath.Base(os.Args[0])
}

func parseArgs(argv []string) (parsed, error) {
	if len(argv) == 0 {
		return parsed{}, nil
	}

	switch argv[0] {
	case "-h", "--help", "help":
		return parsed{showHelp: true}, nil
	case "--version", "-version", "version":
		return parsed{showVer: true}, nil
	case cmdInit, cmdRun, cmdMaisaka:
		p := parsed{command: argv[0]}
		fs := flag.NewFlagSet(p.command, flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		dir := fs.String("directory", "", "实例目录，默认当前目录")
		short := fs.String("d", "", "实例目录（简写）")
		if err := fs.Parse(argv[1:]); err != nil {
			return parsed{}, err
		}
		chosen := *dir
		if *short != "" {
			if chosen != "" && chosen != *short {
				return parsed{}, fmt.Errorf("不能同时指定不同的 -d 与 --directory")
			}
			chosen = *short
		}
		if chosen == "" {
			wd, err := os.Getwd()
			if err != nil {
				return parsed{}, fmt.Errorf("获取当前目录: %w", err)
			}
			chosen = wd
		}
		abs, err := filepath.Abs(chosen)
		if err != nil {
			return parsed{}, fmt.Errorf("解析实例目录: %w", err)
		}
		p.directory = filepath.Clean(abs)
		return p, nil
	default:
		return parsed{}, nil
	}
}

func isCLISubcommand(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	switch argv[0] {
	case cmdInit, cmdRun, cmdMaisaka, "-h", "--help", "help", "--version", "-version", "version":
		return true
	default:
		return false
	}
}

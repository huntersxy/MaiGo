package scripts

// A_memorix 脚本统一 CLI 分发器
// Python 版每个脚本独立执行，Go 版保留相同命令名并把入口收敛到 RunX

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Command 一个可分发脚本命令
type Command struct {
	// Name 对齐 Python 脚本文件名，如 runtime_self_check.py
	Name string
	// Aliases 额外接受的命令名
	Aliases []string
	// Description 命令列表中的说明
	Description string
	// Run 执行命令，返回进程退出码
	Run func(ctx context.Context, argv []string) int
}

// Commands 返回 13 个 Python 脚本对应的 Go 命令
func Commands() []Command {
	return []Command{
		{Name: "_bootstrap.py", Description: "脚本公共引导层（无独立操作）", Run: runBootstrapCommand},
		{Name: "runtime_self_check.py", Description: "运行时自检", Run: RunRuntimeSelfCheck},
		{Name: "import_lpmm_json.py", Description: "导入 LPMM OpenIE JSON", Run: RunImportLPMMJSON},
		{Name: "backfill_temporal_metadata.py", Description: "回填时序元数据", Run: RunBackfillTemporalMetadata},
		{Name: "process_knowledge.py", Description: "处理 raw 知识并导入", Run: RunProcessKnowledge},
		{Name: "migrate_chat_history.py", Description: "迁移 chat_history 摘要", Run: RunMigrateChatHistory},
		{Name: "migrate_maibot_memory.py", Description: "迁移 MaiBot 记忆到 A_Memorix", Run: RunMigrateMaiBotMemory},
		{Name: "convert_lpmm.py", Description: "转换 LPMM parquet/graphml", Run: RunConvertLPMM},
		{Name: "migrate_person_memory_points.py", Description: "迁移人物记忆点", Run: RunMigratePersonMemoryPoints},
		{Name: "rebuild_episodes.py", Description: "重建 Episode", Run: RunRebuildEpisodes},
		{Name: "release_vnext_migrate.py", Description: "vNext 发布迁移", Run: RunReleaseVNextMigrate},
		{Name: "audit_vector_consistency.py", Description: "审计向量一致性", Run: RunAuditVectorConsistency},
		{Name: "backfill_relation_vectors.py", Description: "回填关系向量", Run: RunBackfillRelationVectors},
	}
}

// RunCLI 执行统一 CLI，argv 不含程序名
func RunCLI(ctx context.Context, argv []string) int {
	return RunCLITo(ctx, argv, os.Stdout, os.Stderr)
}

// RunCLITo 使用指定输出执行统一 CLI
func RunCLITo(ctx context.Context, argv []string, stdout io.Writer, stderr io.Writer) int {
	ctx = WithScriptIO(ctx, stdout, stderr)
	if len(argv) == 0 {
		WriteCLIHelp(stdout)
		return 0
	}
	switch argv[0] {
	case "-h", "--help", "help":
		WriteCLIHelp(stdout)
		return 0
	}
	command, ok := LookupCommand(argv[0])
	if !ok {
		fmt.Fprintf(stderr, "memorix-scripts: error: unknown command %q\n", argv[0])
		WriteCLIHelp(stderr)
		return 2
	}
	return command.Run(ctx, argv[1:])
}

// RunCommand 按命令名执行，第二个返回值表示命令是否存在
func RunCommand(ctx context.Context, name string, argv []string) (int, bool) {
	return RunCommandWithIO(ctx, name, argv, ScriptStdout(ctx), ScriptStderr(ctx))
}

// RunCommandWithIO 使用指定输出执行命令，供 WebUI 与测试捕获脚本输出
func RunCommandWithIO(ctx context.Context, name string, argv []string, stdout io.Writer, stderr io.Writer) (int, bool) {
	command, ok := LookupCommand(name)
	if !ok {
		return 2, false
	}
	return command.Run(WithScriptIO(ctx, stdout, stderr), argv), true
}

// LookupCommand 按脚本文件名或不带 .py 的名称查找命令
func LookupCommand(name string) (Command, bool) {
	normalized := normalizeCommandName(name)
	if normalized == "" {
		return Command{}, false
	}
	for _, command := range Commands() {
		if normalizeCommandName(command.Name) == normalized {
			return command, true
		}
		for _, alias := range command.Aliases {
			if normalizeCommandName(alias) == normalized {
				return command, true
			}
		}
	}
	return Command{}, false
}

// HasCommand 判断命令是否存在
func HasCommand(name string) bool {
	_, ok := LookupCommand(name)
	return ok
}

// CommandNames 返回按命令名排序的脚本名
func CommandNames() []string {
	commands := Commands()
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		names = append(names, command.Name)
	}
	sort.Strings(names)
	return names
}

// WriteCLIHelp 输出统一 CLI 帮助
func WriteCLIHelp(w io.Writer) {
	if w == nil {
		w = os.Stdout
	}
	fmt.Fprintln(w, "usage: memorix-scripts <command> [args...]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "A_memorix Go 脚本命令（命令名对齐 Python 脚本文件名）")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	commands := Commands()
	width := 0
	for _, command := range commands {
		if len(command.Name) > width {
			width = len(command.Name)
		}
	}
	for _, command := range commands {
		fmt.Fprintf(w, "  %-*s  %s\n", width, command.Name, command.Description)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "直接运行某个命令的帮助：memorix-scripts <command> --help")
}

// runBootstrapCommand 对齐 _bootstrap.py 的导入副作用；Go 版无需初始化路径
func runBootstrapCommand(context.Context, []string) int {
	return 0
}

// normalizeCommandName 归一脚本名，兼容路径、.py 后缀与连字符
func normalizeCommandName(name string) string {
	value := strings.TrimSpace(name)
	value = strings.ReplaceAll(value, "\\", "/")
	value = filepath.Base(value)
	value = strings.TrimSuffix(value, ".py")
	value = strings.ToLower(value)
	value = strings.ReplaceAll(value, "-", "_")
	return value
}

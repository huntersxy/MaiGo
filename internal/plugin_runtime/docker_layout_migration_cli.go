package pluginruntime

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// PluginLayoutMigrationOptions 是 Docker 入口脚本传入的迁移路径
type PluginLayoutMigrationOptions struct {
	SourceRoot string
	DataRoot   string
	ReportPath string
}

// ParsePluginLayoutMigrationArgs 解析 --source-root/--data-root/--report-path
func ParsePluginLayoutMigrationArgs(args []string) (PluginLayoutMigrationOptions, error) {
	options := PluginLayoutMigrationOptions{
		SourceRoot: DefaultPluginLayoutSourceRoot,
		DataRoot:   DefaultPluginLayoutDataRoot,
		ReportPath: DefaultPluginLayoutReportPath,
	}
	flags := flag.NewFlagSet("plugin-layout-migration", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.SourceRoot, "source-root", options.SourceRoot, "插件源码根目录")
	flags.StringVar(&options.DataRoot, "data-root", options.DataRoot, "插件数据根目录")
	flags.StringVar(&options.ReportPath, "report-path", options.ReportPath, "迁移报告路径")
	if err := flags.Parse(args); err != nil {
		return PluginLayoutMigrationOptions{}, err
	}
	return options, nil
}

// RunPluginLayoutMigrationCLI 执行一次 Docker 插件目录布局迁移并打印结果
func RunPluginLayoutMigrationCLI(args []string, stdout io.Writer) error {
	if stdout == nil {
		stdout = os.Stdout
	}
	options, err := ParsePluginLayoutMigrationArgs(args)
	if err != nil {
		return err
	}
	_, reportErr := os.Stat(options.ReportPath)
	reportAlreadyExists := reportErr == nil
	report, err := MigratePluginLayout(options.SourceRoot, options.DataRoot, options.ReportPath)
	if err != nil {
		return err
	}
	if report == nil {
		_, _ = fmt.Fprintln(stdout,
			"[插件目录迁移] 插件源码目录与数据目录仍指向同一位置，已跳过迁移；"+
				"请更新 docker-compose.yml，增加独立的 /MaiMBot/data/plugins 挂载。")
		return nil
	}
	if reportAlreadyExists {
		_, _ = fmt.Fprintf(stdout, "[插件目录迁移] 已完成过迁移，报告位于 %s\n", options.ReportPath)
		return nil
	}
	_, _ = fmt.Fprintf(stdout,
		"[插件目录迁移] 自动迁移完成：移动 %d 个，清理空目录 %d 个，跳过歧义目录 %d 个，冲突 %d 个。\n",
		len(report.Moved), len(report.RemovedEmpty), len(report.SkippedAmbiguous), len(report.Conflicts))
	if report.RequiresManualReview() {
		_, _ = fmt.Fprintf(stdout, "[插件目录迁移] 存在需要人工确认的项目，详情见 %s\n", options.ReportPath)
	}
	return nil
}

// MainPluginLayoutMigration 便于 Docker 入口直接调用
func MainPluginLayoutMigration() {
	if err := RunPluginLayoutMigrationCLI(os.Args[1:], os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

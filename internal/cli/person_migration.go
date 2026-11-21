package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"MaiGo/internal/services"
)

type migrationExtensionFlags []string

func (flags *migrationExtensionFlags) String() string { return strings.Join(*flags, ",") }

func (flags *migrationExtensionFlags) Set(value string) error {
	*flags = append(*flags, value)
	return nil
}

func runPersonMigrationCommand(args []string) int {
	if err := personMigrationCommand(context.Background(), args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, "错误:", err)
		return 1
	}
	return 0
}

func personMigrationCommand(ctx context.Context, args []string) error {
	command := flag.NewFlagSet("person-migrate", flag.ContinueOnError)
	var directory string
	command.StringVar(&directory, "directory", ".", "Bot 实例目录")
	command.StringVar(&directory, "d", ".", "Bot 实例目录")
	if err := command.Parse(args); err != nil {
		return err
	}
	if command.NArg() == 0 {
		return errors.New("用法：person-migrate [-d 实例目录] <preview|execute|recover>")
	}
	action := command.Arg(0)
	options := flag.NewFlagSet("person-migrate "+action, flag.ContinueOnError)
	parse := func(required ...*string) error {
		if err := options.Parse(command.Args()[1:]); err != nil {
			return err
		}
		if options.NArg() != 0 {
			return fmt.Errorf("意外的参数: %s", options.Arg(0))
		}
		for _, value := range required {
			if *value == "" {
				options.PrintDefaults()
				return errors.New("缺少必需参数")
			}
		}
		return nil
	}
	switch action {
	case "preview":
		platform := options.String("platform", "", "来源和目标的平台")
		source := options.String("source", "", "来源用户 ID")
		target := options.String("target", "", "目标用户 ID")
		output := options.String("output", "", "迁移计划文件")
		decisionsPath := options.String("decisions", "", "冲突选择 JSON 文件")
		var extensions migrationExtensionFlags
		options.Var(&extensions, "extension", "已登记的扩展名称，可重复")
		if err := parse(platform, source, target, output); err != nil {
			return err
		}
		var decisions map[string]string
		if *decisionsPath != "" {
			if err := services.ReadPersonMigrationJSON(*decisionsPath, &decisions); err != nil {
				return err
			}
		}
		plan, err := services.PreviewPersonMigration(ctx, directory, *platform, *source, *target, decisions, extensions...)
		if err != nil {
			return err
		}
		if err := services.WritePersonMigrationJSON(*output, plan); err != nil {
			return err
		}
		review := strings.TrimSuffix(*output, filepath.Ext(*output)) + ".review.json"
		if err := services.WritePersonMigrationJSON(review, map[string]any{"identity": plan.Identity, "counts": plan.Counts(), "conflicts": plan.Conflicts}); err != nil {
			return err
		}
		fmt.Printf("计划已保存: %s\n影响范围和冲突: %s\n冲突: %d；全部确认: %t\n", *output, review, len(plan.Conflicts), plan.Ready)
	case "execute":
		path := options.String("plan", "", "已确认的迁移计划")
		if err := parse(path); err != nil {
			return err
		}
		var plan services.PersonMigrationPlan
		if err := services.ReadPersonMigrationJSON(*path, &plan); err != nil {
			return err
		}
		job, err := services.ExecutePersonMigration(ctx, directory, plan)
		if err != nil {
			return err
		}
		fmt.Println("人物合并完成，执行记录:", job)
	case "recover":
		job := options.String("job", "", "任务目录")
		if err := parse(job); err != nil {
			return err
		}
		if err := services.RecoverPersonMigration(ctx, directory, *job); err != nil {
			return err
		}
		fmt.Println("人物迁移任务状态已恢复")
	default:
		return fmt.Errorf("未知的迁移操作: %s", action)
	}
	return nil
}

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	memory "MaiGo/internal/common/database"
	modelstats "MaiGo/internal/services/model_stats"
)

func runUsageCostRecalcCommand(args []string) int {
	if err := usageCostRecalcCommand(context.Background(), args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, "错误:", err)
		return 1
	}
	return 0
}

func usageCostRecalcCommand(ctx context.Context, args []string) error {
	command := flag.NewFlagSet("usage-cost-recalc", flag.ContinueOnError)
	var directory string
	command.StringVar(&directory, "directory", ".", "Bot 实例目录")
	command.StringVar(&directory, "d", ".", "Bot 实例目录")
	if err := command.Parse(args); err != nil {
		return err
	}
	if command.NArg() != 0 {
		return fmt.Errorf("意外的参数: %s", command.Arg(0))
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	dbPath := filepath.Join(directory, "data", memory.DefaultDatabaseFileName)
	db, err := memory.OpenDB(dbPath)
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	report, err := modelstats.RecalcUsageCosts(ctx, directory, db)
	if err != nil {
		return err
	}
	fmt.Printf(
		"用量费用重算完成：扫描 %d，更新 %d（其中嵌入 %d），无价格 %d，仍为零 %d，费用增量 %.6f，汇总已重建 %t\n",
		report.Scanned, report.Updated, report.EmbeddingUpdated,
		report.SkippedNoPrice, report.SkippedUnchanged, report.CostDelta, report.AggregatesRebuilt,
	)
	return nil
}

package host

// 宿主入口：0.x 升级需要真实终端确认，必须在派生无控制台的 Worker 之前完成。
// 对齐 src/host/bootstrap.py。

import (
	"bufio"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/glebarez/go-sqlite"

	maibotcfg "MaiGo/internal/config/maibot"
)

func confirmHostUpgrades(workspace Workspace, confirmed map[string]struct{}) map[string]struct{} {
	if confirmed == nil {
		confirmed = map[string]struct{}{}
	}
	bots, err := workspace.Registry.ListBots()
	if err != nil {
		return confirmed
	}
	preconfirmed := os.Getenv(maibotcfg.LegacyUpgradeConfirmEnv) == "1"
	interactive := stdinIsTerminal()
	reader := bufio.NewReader(os.Stdin)
	for _, bot := range bots {
		if bot.DesiredState != "running" || bot.Initialization != "initialized" {
			continue
		}
		reasons := maibotcfg.CollectLegacyUpgradeReasons(bot.Directory, detectLegacy0XDatabase)
		if len(reasons) == 0 {
			continue
		}
		if preconfirmed {
			confirmed[bot.BotID] = struct{}{}
			continue
		}
		fmt.Println()
		fmt.Println(strings.Repeat("=", 72))
		fmt.Printf("MaiBot 升级提示：%s（%s）\n", bot.Name, bot.Directory)
		fmt.Println("检测到此 Bot 可能是从 1.0.0 以前的 0.x.x 版本升级而来。")
		fmt.Println("继续启动将会执行自动升级，可能包括数据库结构更新和配置文件更新。")
		fmt.Println("建议在继续前备份 data/ 与 config/ 目录。")
		fmt.Println()
		for _, reason := range reasons {
			fmt.Printf("- %s\n", reason.Message)
		}
		fmt.Println(strings.Repeat("=", 72))
		answer := ""
		if interactive {
			fmt.Print("确认继续升级并启动这个 Bot 吗？请输入 y 后回车：")
			line, readErr := reader.ReadString('\n')
			if readErr == nil {
				answer = strings.TrimSpace(line)
			}
		}
		if strings.EqualFold(answer, "y") {
			confirmed[bot.BotID] = struct{}{}
			continue
		}
		fmt.Printf("未确认升级，本次不启动 %s。\n", bot.Name)
	}
	return confirmed
}

// detectLegacy0XDatabase 只按旧库的 user_version 判定，避免加载数据库层
func detectLegacy0XDatabase(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return false
	}
	defer db.Close()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return false
	}
	return version == 1
}

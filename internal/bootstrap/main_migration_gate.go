package bootstrap

import (
	"fmt"
	"os"
	"strings"

	"maibot-go/internal/common/database"
	"maibot-go/internal/common/database/migrations"
	maibotcfg "maibot-go/internal/config/maibot"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// migrateSchemaAtStartup 先探测数据库结构再执行迁移
// 空库、完整 v17 结构与旧版本记录都按 GuardSchema 决策处理，无法自动升级的旧库真实终止
func migrateSchemaAtStartup(db *gorm.DB) error {
	if err := checkLegacyConfigUpgradeAtStartup(db); err != nil {
		return err
	}
	decision, err := migration.GuardSchema(db)
	if err != nil {
		return fmt.Errorf("数据库 schema 探测失败: %w", err)
	}
	zap.L().Info("数据库 schema 探测完成",
		zap.String("action", string(decision.Action)),
		zap.Int("current_version", decision.CurrentVersion),
		zap.Int("supported_version", decision.SupportedVersion),
		zap.Ints("pending_versions", decision.PendingVersions),
		zap.String("reason", decision.Reason))

	run, err := schemaMigrationPlan(decision)
	if err != nil {
		return err
	}
	if !run {
		return nil
	}
	if err := migration.RunMigrations(db); err != nil {
		return fmt.Errorf("数据库迁移失败: %w", err)
	}
	zap.L().Info("数据库迁移完成", zap.Int("version", decision.SupportedVersion))
	return nil
}

// schemaMigrationPlan 把门禁决策转成是否执行迁移；纯函数便于覆盖各分支
func schemaMigrationPlan(decision migration.GuardDecision) (bool, error) {
	switch decision.Action {
	case migration.GuardInitialize, migration.GuardAdoptV17, migration.GuardUpgrade, migration.GuardNoop:
		return true, nil
	case migration.GuardManual:
		if decision.CurrentVersion > decision.SupportedVersion {
			return false, fmt.Errorf("数据库 schema 版本为 v%d，高于当前程序支持的 v%d：%s",
				decision.CurrentVersion, decision.SupportedVersion, decision.Reason)
		}
		return false, fmt.Errorf("数据库 schema 无法自动升级：%s", decision.Reason)
	default:
		return false, fmt.Errorf("未知的数据库 schema 决策: %s", decision.Action)
	}
}

// checkLegacyConfigUpgradeAtStartup 在任何配置加载/数据库升级前检查 0.x 旧配置
// 未设置 MAIBOT_LEGACY_0X_UPGRADE_CONFIRMED=1 时终止启动，避免静默改写旧实例
func checkLegacyConfigUpgradeAtStartup(db *gorm.DB) error {
	instanceRoot := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR"))
	if instanceRoot == "" {
		return nil
	}
	detectLegacyDB := func(string) bool { return memory.DetectLegacy0XDatabase(db) }
	if err := maibotcfg.RequireLegacyUpgradeConfirmation(instanceRoot, os.Getenv, detectLegacyDB); err != nil {
		return fmt.Errorf("旧版配置升级未确认: %w", err)
	}
	return nil
}

package storage

// A_memorix 表结构补齐逻辑，对齐 Python core/storage/metadata_schema.py 的 _ensure_* 系列

import (
	"fmt"
	"sort"
	"strings"
)

// ensureColumns 严格补齐正式版本字段，失败时直接暴露迁移错误
func ensureColumns(conn *Conn, table string, migrations [][2]string) error {
	existing, err := tableColumnSet(conn, table)
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		if _, ok := existing[migration[0]]; ok {
			continue
		}
		if _, err := conn.Exec(migration[1]); err != nil {
			return fmt.Errorf("Schema迁移失败 (%s.%s): %w", table, migration[0], err)
		}
	}

	migrated, err := tableColumnSet(conn, table)
	if err != nil {
		return err
	}
	missing := make([]string, 0, len(migrations))
	for _, migration := range migrations {
		if _, ok := migrated[migration[0]]; !ok {
			missing = append(missing, migration[0])
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("Schema迁移失败 (%s: %s)", table, strings.Join(missing, ", "))
	}
	return nil
}

// addColumnIfMissing 单列补齐，对齐 Python 带 try/except 的历史列迁移
// 已存在时直接跳过，执行失败后复查列，仍未补齐才返回错误
func addColumnIfMissing(conn *Conn, table string, column string, sqlText string, failureMessage string) error {
	columns, err := tableColumnSet(conn, table)
	if err != nil {
		return err
	}
	if _, ok := columns[column]; ok {
		return nil
	}
	if _, execErr := conn.Exec(sqlText); execErr != nil {
		current, err := tableColumnSet(conn, table)
		if err != nil {
			return err
		}
		if _, ok := current[column]; !ok {
			return fmt.Errorf("%s: %w", failureMessage, execErr)
		}
	}
	return nil
}

// ensurePersonIdentityTables 昵称历史独立于人物画像和记忆正文保存，身份仅按平台用户 ID 确定
func ensurePersonIdentityTables(conn *Conn) error {
	return execAll(conn, ddlPersonIdentities, ddlPersonIdentityAliases, ddlIdxPersonIdentityAlias)
}

// ensureEpisodeEmptyGroupTable 保存成功但未产生情景的分组指纹，不生成虚假的情景记忆
func ensureEpisodeEmptyGroupTable(conn *Conn) error {
	return execAll(conn, ddlEpisodeEmptyGroups)
}

// ensureSummaryCheckpointTables 独立保存已完成摘要窗口的标识与进度，空增量也能推进游标
func ensureSummaryCheckpointTables(conn *Conn) error {
	if err := execAll(conn, ddlChatSummaryCheckpoints, ddlIdxChatSummaryCheckpointsChat); err != nil {
		return err
	}
	columns, err := tableColumnSet(conn, "chat_summary_checkpoints")
	if err != nil {
		return err
	}
	if _, ok := columns["phase"]; !ok {
		if _, err := conn.Exec("ALTER TABLE chat_summary_checkpoints ADD COLUMN phase TEXT NOT NULL DEFAULT 'complete'"); err != nil {
			return err
		}
	}
	if _, ok := columns["result_json"]; !ok {
		if _, err := conn.Exec("ALTER TABLE chat_summary_checkpoints ADD COLUMN result_json TEXT"); err != nil {
			return err
		}
	}
	return execAll(conn, ddlIdxFactClaimsScopeValue, ddlIdxExternalMemoryRefsChat)
}

// ensurePersonProfileAliasOverrideTable 创建人物画像的人工别名覆盖表
func ensurePersonProfileAliasOverrideTable(conn *Conn) error {
	return execAll(conn, ddlPersonProfileAliasOverrides, ddlIdxPersonProfileAliasOverridesUpdated)
}

// ensureStorageCleanupTables 创建存储清理队列表与索引
func ensureStorageCleanupTables(conn *Conn) error {
	return execAll(conn, ddlStorageCleanupJobs, ddlIdxStorageCleanupReady, ddlIdxStorageCleanupOperation)
}

// ensureFactTables 创建事实账本表，对齐 Python FACT_SCHEMA_STATEMENTS 的语句顺序
func ensureFactTables(conn *Conn) error {
	return execAll(conn,
		ddlFactClaims,
		ddlIdxFactClaimsScopeStatus,
		ddlIdxFactClaimsConflict,
		ddlFactEvidence,
		ddlIdxFactEvidenceClaim,
		ddlFactTransitions,
		ddlIdxFactTransitionsOld,
		ddlIdxFactTransitionsNew,
	)
}

// ensureEpisodeColumns 补齐 Episode 分组输入指纹列
func ensureEpisodeColumns(conn *Conn) error {
	return addColumnIfMissing(
		conn,
		"episodes",
		"input_fingerprint",
		"ALTER TABLE episodes ADD COLUMN input_fingerprint TEXT",
		"Schema迁移失败 (episodes.input_fingerprint)",
	)
}

// ensureEpisodeSourceRevisionColumns 把 Episode 调度切换为来源级 revision/outbox
func ensureEpisodeSourceRevisionColumns(conn *Conn) error {
	if err := ensureColumns(conn, "episode_rebuild_sources", [][2]string{
		{"desired_revision", "ALTER TABLE episode_rebuild_sources ADD COLUMN desired_revision INTEGER NOT NULL DEFAULT 1"},
		{"built_revision", "ALTER TABLE episode_rebuild_sources ADD COLUMN built_revision INTEGER NOT NULL DEFAULT 0"},
		{"claimed_revision", "ALTER TABLE episode_rebuild_sources ADD COLUMN claimed_revision INTEGER"},
		{"dirty_start", "ALTER TABLE episode_rebuild_sources ADD COLUMN dirty_start REAL"},
		{"dirty_end", "ALTER TABLE episode_rebuild_sources ADD COLUMN dirty_end REAL"},
		{"first_requested_at", "ALTER TABLE episode_rebuild_sources ADD COLUMN first_requested_at REAL"},
		{"ready_at", "ALTER TABLE episode_rebuild_sources ADD COLUMN ready_at REAL"},
		{"lease_token", "ALTER TABLE episode_rebuild_sources ADD COLUMN lease_token TEXT"},
		{"lease_until", "ALTER TABLE episode_rebuild_sources ADD COLUMN lease_until REAL"},
		{"next_attempt_at", "ALTER TABLE episode_rebuild_sources ADD COLUMN next_attempt_at REAL"},
		{"built_generation_hash", "ALTER TABLE episode_rebuild_sources ADD COLUMN built_generation_hash TEXT"},
		{"claimed_generation_hash", "ALTER TABLE episode_rebuild_sources ADD COLUMN claimed_generation_hash TEXT"},
		{"retry_revision", "ALTER TABLE episode_rebuild_sources ADD COLUMN retry_revision INTEGER"},
		{"retry_generation_hash", "ALTER TABLE episode_rebuild_sources ADD COLUMN retry_generation_hash TEXT"},
	}); err != nil {
		return err
	}

	now := nowTimestamp()
	if _, err := conn.Exec(episodeRebuildRevisionResetSQL, now, now, now); err != nil {
		return err
	}

	var exists int
	pendingErr := conn.QueryRow(
		"SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'episode_pending_paragraphs'",
	).Scan(&exists)
	if pendingErr == nil {
		if _, err := conn.Exec(episodePendingParagraphsMigrateSQL, now); err != nil {
			return err
		}
		if _, err := conn.Exec("DROP TABLE episode_pending_paragraphs"); err != nil {
			return err
		}
	}

	if _, err := conn.Exec(episodeSourceDiscoverySQL, now, now, now, now, now); err != nil {
		return err
	}

	return execAll(conn, ddlIdxEpisodeRebuildClaim, ddlIdxEpisodeRebuildRevision)
}

// ensureFuzzyModifyPlanTables 补齐模糊修改计划表，用于预览、确认、执行和追溯
func ensureFuzzyModifyPlanTables(conn *Conn) error {
	return execAll(conn,
		ddlMemoryFuzzyModifyPlans,
		ddlIdxMemoryFuzzyModifyPlansCreated,
		ddlIdxMemoryFuzzyModifyPlansStatusUpdated,
		ddlIdxMemoryFuzzyModifyPlansTarget,
	)
}

// ensureKnowledgePackageTables 创建可分享知识包的安装登记和段落归属表
func ensureKnowledgePackageTables(conn *Conn) error {
	return execAll(conn,
		ddlKnowledgePackages,
		ddlIdxKnowledgePackagesUpdated,
		ddlIdxKnowledgePackagesPackage,
		ddlKnowledgePackageParagraphs,
		ddlIdxKnowledgePackageParagraphsHash,
		ddlKnowledgePackageResources,
		ddlIdxKnowledgePackageResourcesLookup,
	)
}

// ensureImageMemoryTables 创建图片记忆的权威记录和可恢复嵌入任务
// Python 侧用 executescript 一次执行，这里按同一顺序逐条执行
func ensureImageMemoryTables(conn *Conn) error {
	return execAll(conn,
		ddlImageAssets,
		ddlIdxImageAssetsStatusUpdated,
		ddlImageOccurrences,
		ddlIdxImageOccurrencesScope,
		ddlIdxImageOccurrencesAsset,
		ddlIdxImageOccurrencesInstallation,
		ddlImageObservations,
		ddlIdxImageObservationsOccurrence,
		ddlImageMemoryLinks,
		ddlIdxImageMemoryLinksTarget,
		ddlImageDescriptionCompensations,
		ddlIdxImageDescriptionCompensationsClaim,
		ddlImagePendingDescriptions,
		ddlImageEmbeddings,
		ddlImageEmbeddingJobs,
		ddlIdxImageEmbeddingJobsClaim,
		ddlImageRuntimeState,
	)
}

// ensureMemoryFeedbackTaskColumns 补齐 memory_feedback_tasks 历史库缺失的 rollback_* 列
func ensureMemoryFeedbackTaskColumns(conn *Conn) error {
	migrations := [][2]string{
		{"rollback_status", "ALTER TABLE memory_feedback_tasks ADD COLUMN rollback_status TEXT DEFAULT 'none'"},
		{"rollback_plan_json", "ALTER TABLE memory_feedback_tasks ADD COLUMN rollback_plan_json TEXT"},
		{"rollback_result_json", "ALTER TABLE memory_feedback_tasks ADD COLUMN rollback_result_json TEXT"},
		{"rollback_error", "ALTER TABLE memory_feedback_tasks ADD COLUMN rollback_error TEXT"},
		{"rollback_requested_by", "ALTER TABLE memory_feedback_tasks ADD COLUMN rollback_requested_by TEXT"},
		{"rollback_reason", "ALTER TABLE memory_feedback_tasks ADD COLUMN rollback_reason TEXT"},
		{"rollback_requested_at", "ALTER TABLE memory_feedback_tasks ADD COLUMN rollback_requested_at REAL"},
		{"rolled_back_at", "ALTER TABLE memory_feedback_tasks ADD COLUMN rolled_back_at REAL"},
	}
	columns, err := tableColumnSet(conn, "memory_feedback_tasks")
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		if _, ok := columns[migration[0]]; ok {
			continue
		}
		message := "Schema迁移失败 (memory_feedback_tasks." + migration[0] + ")"
		if err := addColumnIfMissing(conn, "memory_feedback_tasks", migration[0], migration[1], message); err != nil {
			return err
		}
	}
	return nil
}

// ensureParagraphStaleRelationMarkColumns 补齐段落陈旧关系标记的来源追踪列
func ensureParagraphStaleRelationMarkColumns(conn *Conn) error {
	migrations := [][2]string{
		{"source_type", "ALTER TABLE paragraph_stale_relation_marks ADD COLUMN source_type TEXT"},
		{"source_id", "ALTER TABLE paragraph_stale_relation_marks ADD COLUMN source_id TEXT"},
		{"source_operation_id", "ALTER TABLE paragraph_stale_relation_marks ADD COLUMN source_operation_id TEXT"},
	}
	columns, err := tableColumnSet(conn, "paragraph_stale_relation_marks")
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		if _, ok := columns[migration[0]]; ok {
			continue
		}
		message := "Schema迁移失败 (paragraph_stale_relation_marks." + migration[0] + ")"
		if err := addColumnIfMissing(conn, "paragraph_stale_relation_marks", migration[0], migration[1], message); err != nil {
			return err
		}
	}
	return nil
}

// ensurePersonProfileSnapshotColumns 补齐人物画像证据版本列
func ensurePersonProfileSnapshotColumns(conn *Conn) error {
	columns, err := tableColumnSet(conn, "person_profile_snapshots")
	if err != nil {
		return err
	}
	if _, ok := columns["evidence_fingerprint"]; !ok {
		if err := addColumnIfMissing(
			conn,
			"person_profile_snapshots",
			"evidence_fingerprint",
			"ALTER TABLE person_profile_snapshots ADD COLUMN evidence_fingerprint TEXT",
			"Schema迁移失败 (person_profile_snapshots.evidence_fingerprint)",
		); err != nil {
			return err
		}
	}
	if _, ok := columns["fact_claim_ids_json"]; !ok {
		if _, err := conn.Exec("ALTER TABLE person_profile_snapshots ADD COLUMN fact_claim_ids_json TEXT"); err != nil {
			return err
		}
	}
	return nil
}

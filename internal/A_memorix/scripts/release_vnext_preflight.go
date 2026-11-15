package scripts

// vNext 发布迁移 preflight 检查，对齐 release_vnext_migrate.py 的 _preflight_impl

import (
	"fmt"
	"path/filepath"
	"strings"

	"MaiGo/internal/A_memorix/core/storage"
)

// releasePreflight 对齐 _preflight_impl
func releasePreflight(configPath, dataDir string) releaseReport {
	checks := make([]releaseCheck, 0)
	facts := map[string]any{
		"config_path": configPath,
		"data_dir":    dataDir,
	}
	if !pathIsFile(configPath) {
		checks = append(checks, releaseCheck{Code: "CFG-00", Level: "error", Message: fmt.Sprintf("config not found: %s", configPath)})
		return releaseReport{OK: false, Checks: checks, Facts: facts}
	}
	configDoc, err := LoadTOML(configPath)
	if err != nil {
		checks = append(checks, releaseCheck{Code: "CFG-00", Level: "error", Message: fmt.Sprintf("config not found: %s", configPath)})
		return releaseReport{OK: false, Checks: checks, Facts: facts}
	}

	toolMode := strings.ToLower(strings.TrimSpace(pyString(NestedGet(configDoc, "routing.tool_search_mode", "forward"))))
	summaryModel := NestedGet(configDoc, "summarization.model_name", []any{"auto"})
	summaryKnowledgeType := strings.ToLower(strings.TrimSpace(pyString(NestedGet(configDoc, "summarization.default_knowledge_type", "narrative"))))
	quantization := strings.ToLower(strings.TrimSpace(pyString(NestedGet(configDoc, "embedding.quantization_type", "int8"))))

	facts["routing.tool_search_mode"] = toolMode
	facts["summarization.model_name_type"] = pyTypeName(summaryModel)
	facts["summarization.default_knowledge_type"] = summaryKnowledgeType
	facts["embedding.quantization_type"] = quantization

	if toolMode == "legacy" {
		checks = append(checks, releaseCheck{Code: "CP-04", Level: "error", Message: "routing.tool_search_mode=legacy is no longer accepted at runtime"})
	} else if toolMode != "forward" && toolMode != "disabled" {
		checks = append(checks, releaseCheck{Code: "CP-04", Level: "error", Message: fmt.Sprintf("routing.tool_search_mode invalid value: %s", toolMode)})
	}

	if _, isString := summaryModel.(string); isString {
		checks = append(checks, releaseCheck{Code: "CP-11", Level: "error", Message: "summarization.model_name must be List[str], string legacy format detected"})
	} else if !isStringList(summaryModel) {
		checks = append(checks, releaseCheck{Code: "CP-11", Level: "error", Message: "summarization.model_name must be List[str]"})
	}

	if !isAllowedKnowledgeType(summaryKnowledgeType) {
		checks = append(checks, releaseCheck{Code: "CP-13", Level: "error", Message: fmt.Sprintf("invalid summarization.default_knowledge_type: %s", summaryKnowledgeType)})
	}
	if quantization != "int8" {
		checks = append(checks, releaseCheck{Code: "UG-07", Level: "error", Message: "embedding.quantization_type must be int8 in vNext"})
	}

	vectorsDir := filepath.Join(dataDir, "vectors")
	npyPath := filepath.Join(vectorsDir, "vectors.npy")
	binPath := filepath.Join(vectorsDir, "vectors.bin")
	idsBinPath := filepath.Join(vectorsDir, "vectors_ids.bin")
	facts["vectors.npy_exists"] = pathIsFile(npyPath)
	facts["vectors.bin_exists"] = pathIsFile(binPath)
	facts["vectors_ids.bin_exists"] = pathIsFile(idsBinPath)
	if pathIsFile(npyPath) && !(pathIsFile(binPath) && pathIsFile(idsBinPath)) {
		checks = append(checks, releaseCheck{Code: "CP-07", Level: "error", Message: "legacy vectors.npy detected; offline migrate required", Details: map[string]any{"npy_path": npyPath}})
	}

	metadataDB := filepath.Join(dataDir, "metadata", "metadata.db")
	facts["metadata_db_exists"] = pathIsFile(metadataDB)
	relationCount := 0
	if pathIsFile(metadataDB) {
		db, openErr := openReleaseSQLite(metadataDB)
		if openErr != nil {
			checks = append(checks, releaseCheck{Code: "CP-06", Level: "error", Message: fmt.Sprintf("failed to read graph metadata: %v", openErr)})
		} else {
			hasSchemaTable := releaseTableExists(db, "schema_migrations")
			hasParagraphBackfill := releaseTableExists(db, "paragraph_vector_backfill")
			hasStaleMarks := releaseTableExists(db, "paragraph_stale_relation_marks")
			hasProfileRefreshQueue := releaseTableExists(db, "person_profile_refresh_queue")
			hasFeedbackRollbackStatus := releaseColumnExists(db, "memory_feedback_tasks", "rollback_status")
			hasFeedbackRollbackPlan := releaseColumnExists(db, "memory_feedback_tasks", "rollback_plan_json")
			facts["schema_migrations_exists"] = hasSchemaTable
			facts["paragraph_vector_backfill_exists"] = hasParagraphBackfill
			facts["paragraph_stale_relation_marks_exists"] = hasStaleMarks
			facts["person_profile_refresh_queue_exists"] = hasProfileRefreshQueue
			facts["memory_feedback_tasks_rollback_status_exists"] = hasFeedbackRollbackStatus
			facts["memory_feedback_tasks_rollback_plan_exists"] = hasFeedbackRollbackPlan

			if !hasSchemaTable {
				checks = append(checks, releaseCheck{Code: "CP-08", Level: "error", Message: "schema_migrations table missing (legacy metadata schema)"})
			} else {
				version := releaseSchemaVersion(db)
				facts["schema_version"] = version
				runtimeAutoMigratable := version < storage.SchemaVersion && version >= storage.RuntimeAutoMigrationMinSchemaVersion
				facts["schema_runtime_auto_migratable"] = runtimeAutoMigratable
				if version != storage.SchemaVersion {
					if runtimeAutoMigratable {
						checks = append(checks, releaseCheck{Code: "CP-18", Level: "warning", Message: fmt.Sprintf("schema version behind runtime target: current=%d, expected=%d; runtime auto migration will handle this update", version, storage.SchemaVersion)})
					} else {
						checks = append(checks, releaseCheck{Code: "CP-08", Level: "error", Message: fmt.Sprintf("schema version mismatch: current=%d, expected=%d", version, storage.SchemaVersion)})
					}
				} else if !hasParagraphBackfill {
					checks = append(checks, releaseCheck{Code: "CP-14", Level: "error", Message: "paragraph_vector_backfill table missing under current schema version"})
				} else if !hasStaleMarks {
					checks = append(checks, releaseCheck{Code: "CP-15", Level: "error", Message: "paragraph_stale_relation_marks table missing under current schema version"})
				} else if !hasProfileRefreshQueue {
					checks = append(checks, releaseCheck{Code: "CP-16", Level: "error", Message: "person_profile_refresh_queue table missing under current schema version"})
				} else if !hasFeedbackRollbackStatus || !hasFeedbackRollbackPlan {
					checks = append(checks, releaseCheck{Code: "CP-17", Level: "error", Message: "memory_feedback_tasks rollback columns missing under current schema version"})
				}
			}

			if releaseTableExists(db, "relations") {
				relationCount = releaseRelationCount(db)
			}
			facts["relations_count"] = relationCount

			conflicts := collectReleaseAliasConflicts(db)
			facts["alias_conflict_count"] = len(conflicts)
			if len(conflicts) > 0 {
				aliases := sortedMapKeys(conflicts)
				checks = append(checks, releaseCheck{
					Code: "CP-05", Level: "error", Message: "32-bit relation hash alias conflict detected",
					Details: map[string]any{"aliases": firstStrings(aliases, 20), "total": len(aliases)},
				})
			}
			invalidTypes := collectReleaseInvalidKnowledgeTypes(db)
			facts["invalid_knowledge_type_values"] = invalidTypes
			if len(invalidTypes) > 0 {
				checks = append(checks, releaseCheck{
					Code: "CP-12", Level: "error", Message: "invalid paragraph knowledge_type values detected",
					Details: map[string]any{"values": firstStrings(invalidTypes, 20), "total": len(invalidTypes)},
				})
			}
			_ = db.Close()
		}
	} else {
		checks = append(checks, releaseCheck{Code: "META-00", Level: "warning", Message: "metadata.db not found, schema checks skipped"})
	}

	graphMetaPath := filepath.Join(dataDir, "graph", "graph_metadata.json")
	facts["graph_metadata_exists"] = pathIsFile(graphMetaPath)
	if relationCount > 0 {
		if !pathIsFile(graphMetaPath) {
			checks = append(checks, releaseCheck{Code: "CP-06", Level: "error", Message: "relations exist but graph metadata missing"})
		} else if size, sizeErr := graphEdgeHashMapSize(dataDir); sizeErr != nil {
			checks = append(checks, releaseCheck{Code: "CP-06", Level: "error", Message: fmt.Sprintf("failed to read graph metadata: %v", sizeErr)})
		} else {
			facts["edge_hash_map_size"] = size
			if size <= 0 {
				checks = append(checks, releaseCheck{Code: "CP-06", Level: "error", Message: "edge_hash_map missing/empty while relations exist"})
			}
		}
	}

	return releaseReport{OK: !releaseHasError(checks), Checks: checks, Facts: facts}
}

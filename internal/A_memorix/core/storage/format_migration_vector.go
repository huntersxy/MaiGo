package storage

// 向量元数据目录的 pickle 转 JSON 迁移
// 对齐 Python format_migration.py 的 _vector_format_work/_prepare_vector_metadata_payload/
// _load_vector_metadata_from_pickle/_validate_vector_metadata_json/_recover_vector_metadata_from_backup/
// _migrate_vector_metadata_dir

import (
	"fmt"
	"path/filepath"
)

// vectorMetadataFileNames 向量目录内的新旧文件名
const (
	vectorPickleName = "vectors_metadata.pkl"
	vectorJSONName   = "vectors_metadata.json"
)

// vectorFormatWork 对齐 _vector_format_work，只做检测不落盘
func vectorFormatWork(vectorDir string, migrationRecordExists bool) (VectorMigrationEntry, error) {
	picklePath := filepath.Join(vectorDir, vectorPickleName)
	jsonPath := filepath.Join(vectorDir, vectorJSONName)
	backups := legacyBackupCandidates(picklePath)

	jsonValid := false
	jsonError := ""
	if pathExists(jsonPath) {
		if err := ValidateVectorMetadataJSON(jsonPath); err != nil {
			jsonError = err.Error()
		} else {
			jsonValid = true
		}
	}

	if pathExists(picklePath) {
		if migrationRecordExists && jsonValid {
			return VectorMigrationEntry{
				Action: "none",
				Reason: "stale_legacy_artifact",
				Path:   vectorDir,
				Error:  &jsonError,
			}, nil
		}
		reason := "legacy_recovery"
		switch {
		case !pathExists(jsonPath):
			reason = "legacy_pickle"
		case jsonValid:
			reason = "incomplete_migration"
		}
		return VectorMigrationEntry{Action: "migrate", Reason: reason, Path: vectorDir}, nil
	}
	if jsonValid {
		return VectorMigrationEntry{Action: "none", Reason: "current_valid", Path: vectorDir}, nil
	}
	if len(backups) > 0 {
		return VectorMigrationEntry{Action: "recover", Reason: "legacy_backup", Path: vectorDir}, nil
	}
	if pathExists(jsonPath) {
		return VectorMigrationEntry{
			Action: "none",
			Reason: "current_format_corrupt",
			Path:   vectorDir,
			Error:  &jsonError,
		}, nil
	}
	return VectorMigrationEntry{Action: "none", Reason: "legacy_missing", Path: vectorDir}, nil
}

// prepareVectorMetadataPayload 对齐 _prepare_vector_metadata_payload
func prepareVectorMetadataPayload(payload map[string]any, path string) (map[string]any, error) {
	if _, ok := payload["known_hashes"]; !ok {
		if _, ok := payload["ids"]; !ok {
			return nil, fmt.Errorf("向量元数据缺少 known_hashes 或 ids: %s", path)
		}
	}
	metadata := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		metadata[key] = value
	}
	schemaVersion := 1
	if raw, ok := payload["schema_version"]; ok && pythonTruthy(raw) {
		parsed, err := pythonInt(raw)
		if err != nil {
			return nil, err
		}
		schemaVersion = parsed
	}
	metadata["schema_version"] = int64(schemaVersion)
	return metadata, nil
}

// LoadVectorMetadataFromPickle 对齐 _load_vector_metadata_from_pickle
func LoadVectorMetadataFromPickle(path string) (map[string]any, error) {
	payload, err := loadLegacyPickleDict(path, "向量元数据")
	if err != nil {
		return nil, err
	}
	return prepareVectorMetadataPayload(payload, path)
}

// ValidateVectorMetadataJSON 对齐 _validate_vector_metadata_json
func ValidateVectorMetadataJSON(path string) error {
	payload, err := readJSONDict(path)
	if err != nil {
		return err
	}
	_, err = prepareVectorMetadataPayload(payload, path)
	return err
}

// recoverVectorMetadataFromBackup 对齐 _recover_vector_metadata_from_backup
func recoverVectorMetadataFromBackup(vectorDir string, jsonPath string, backups []string) (VectorMigrationEntry, error) {
	if len(backups) == 0 {
		return VectorMigrationEntry{}, fmt.Errorf("向量元数据 JSON 损坏且没有可恢复备份: %s", jsonPath)
	}
	payload, err := LoadVectorMetadataFromPickle(backups[0])
	if err != nil {
		return VectorMigrationEntry{}, err
	}
	if err := writeJSONFile(jsonPath, payload); err != nil {
		return VectorMigrationEntry{}, err
	}
	return VectorMigrationEntry{
		Migrated:  boolPtr(false),
		Recovered: boolPtr(true),
		Reason:    "legacy_backup_recovered",
		Path:      vectorDir,
		Backup:    backups[0],
	}, nil
}

// MigrateVectorMetadataDir 对齐 _migrate_vector_metadata_dir
func MigrateVectorMetadataDir(vectorDir string) (VectorMigrationEntry, error) {
	picklePath := filepath.Join(vectorDir, vectorPickleName)
	jsonPath := filepath.Join(vectorDir, vectorJSONName)

	if !pathExists(picklePath) {
		backups := legacyBackupCandidates(picklePath)
		if pathExists(jsonPath) {
			if err := ValidateVectorMetadataJSON(jsonPath); err != nil {
				return recoverVectorMetadataFromBackup(vectorDir, jsonPath, backups)
			}
		} else if len(backups) > 0 {
			return recoverVectorMetadataFromBackup(vectorDir, jsonPath, backups)
		}
		return VectorMigrationEntry{
			Migrated: boolPtr(false),
			Reason:   "legacy_missing",
			Path:     vectorDir,
		}, nil
	}

	payload, err := LoadVectorMetadataFromPickle(picklePath)
	if err != nil {
		return VectorMigrationEntry{}, err
	}

	if pathExists(jsonPath) {
		reason := "json_exists"
		if err := ValidateVectorMetadataJSON(jsonPath); err != nil {
			if err := writeJSONFile(jsonPath, payload); err != nil {
				return VectorMigrationEntry{}, err
			}
			reason = "json_recovered_from_legacy"
		}
		backup, err := backupLegacyFile(picklePath)
		if err != nil {
			return VectorMigrationEntry{}, err
		}
		return VectorMigrationEntry{
			Migrated:  boolPtr(false),
			Recovered: boolPtr(reason == "json_recovered_from_legacy"),
			Reason:    reason,
			Path:      vectorDir,
			Backup:    backup,
		}, nil
	}

	if err := writeJSONFile(jsonPath, payload); err != nil {
		return VectorMigrationEntry{}, err
	}
	backup, err := backupLegacyFile(picklePath)
	if err != nil {
		return VectorMigrationEntry{}, err
	}
	return VectorMigrationEntry{
		Migrated: boolPtr(true),
		Path:     vectorDir,
		Backup:   backup,
	}, nil
}

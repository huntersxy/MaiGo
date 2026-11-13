package migration

import "fmt"

// 与上游 legacy_v1_to_v2.py 对齐的版本与表名前缀
const (
	// LegacyV1SchemaVersion 对应上游 LEGACY_V1_SCHEMA_VERSION
	LegacyV1SchemaVersion = 1
	// V2SchemaVersion 对应上游 V2_SCHEMA_VERSION
	V2SchemaVersion = 2
	// LegacyV1BackupPrefix 对应上游 _LEGACY_V1_BACKUP_PREFIX
	LegacyV1BackupPrefix = "__legacy_v1_"
	// LegacyV1ToV2StepName 对应上游 MigrationStep.name
	LegacyV1ToV2StepName = "legacy_v1_to_v2"
)

// DatabaseMigrationExecutionError 对应上游 DatabaseMigrationExecutionError
// 外层文案与 Python manager 的包装文案一致，Cause 保留具体失败原因
type DatabaseMigrationExecutionError struct {
	Message string
	Cause   error
}

func (e *DatabaseMigrationExecutionError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *DatabaseMigrationExecutionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func newExecutionError(message string, cause error) *DatabaseMigrationExecutionError {
	return &DatabaseMigrationExecutionError{Message: message, Cause: cause}
}

func newStepExecutionError(cause error) *DatabaseMigrationExecutionError {
	return &DatabaseMigrationExecutionError{
		Message: fmt.Sprintf(
			"执行迁移步骤 %s (%d -> %d) 失败。",
			LegacyV1ToV2StepName, LegacyV1SchemaVersion, V2SchemaVersion,
		),
		Cause: cause,
	}
}

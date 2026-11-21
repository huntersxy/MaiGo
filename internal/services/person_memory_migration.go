package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	memorixcore "MaiGo/internal/A_memorix/core"
)

func buildPersonMemoryMigration(plan *PersonDatabasePlan, move PersonMigrationIdentity) error {
	return memorixcore.BuildPersonMigration(plan, move.Platform, move.Source, move.Target, move.SourcePerson, move.TargetPerson)
}

type personMemoryQueueRecord struct {
	RecordID string `json:"record_id"`
	Payload  any    `json:"payload"`
	Error    string `json:"error"`
}

func readPersonMemoryQueue(path string, visit func(personMemoryQueueRecord)) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	for {
		var record personMemoryQueueRecord
		if err := decoder.Decode(&record); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		if record.RecordID == "" {
			return fmt.Errorf("%s 中的写入任务缺少 record_id", filepath.Base(path))
		}
		visit(record)
	}
}

func checkPersonMemoryWriteQueue(directory string, move PersonMigrationIdentity) error {
	done := map[string]bool{}
	queued := map[string]bool{}
	if err := readPersonMemoryQueue(filepath.Join(directory, "startup_write_queue.done.jsonl"), func(record personMemoryQueueRecord) {
		done[record.RecordID] = true
	}); err != nil {
		return err
	}
	if err := readPersonMemoryQueue(filepath.Join(directory, "startup_write_queue.jsonl"), func(record personMemoryQueueRecord) {
		if !done[record.RecordID] && personMemoryQueueRelevant(record.Payload, move.Source, move.SourcePerson) {
			queued[record.RecordID] = true
		}
	}); err != nil {
		return err
	}
	if len(queued) == 0 {
		return nil
	}
	reasons := []string{}
	seen := map[string]bool{}
	if err := readPersonMemoryQueue(filepath.Join(directory, "startup_write_queue.failed.jsonl"), func(record personMemoryQueueRecord) {
		if queued[record.RecordID] && record.Error != "" && !seen[record.Error] {
			seen[record.Error] = true
			reasons = append(reasons, record.Error)
		}
	}); err != nil {
		return err
	}
	if len(reasons) > 0 {
		return fmt.Errorf("该用户有 %d 条记忆未写入，其中存在失败任务：%s。请先处理失败任务后再迁移", len(queued), strings.Join(reasons, "；"))
	}
	return fmt.Errorf("该用户有 %d 条记忆正在等待写入，请完成后重新预览", len(queued))
}

func personMemoryQueueRelevant(value any, source, personID string) bool {
	switch typed := value.(type) {
	case string:
		return strings.Contains(typed, source) || strings.Contains(typed, personID)
	case []any:
		for _, child := range typed {
			if personMemoryQueueRelevant(child, source, personID) {
				return true
			}
		}
	case map[string]any:
		for key, child := range typed {
			if key != "image_base64" && personMemoryQueueRelevant(child, source, personID) {
				return true
			}
		}
	}
	return false
}

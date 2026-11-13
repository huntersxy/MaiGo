package memorix

// A_memorix 启动写入队列：对齐 host_service.py 的 JSONL 追加、待回放记录筛选与回放
// 文件：<data_dir>/startup_write_queue.jsonl、.done.jsonl、.failed.jsonl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"
)

func (s *AMemorixHostService) startupQueuePath() string {
	return filepath.Join(s.GetRuntimeDataDir(), "startup_write_queue.jsonl")
}

func (s *AMemorixHostService) startupQueueDonePath() string {
	return filepath.Join(s.GetRuntimeDataDir(), "startup_write_queue.done.jsonl")
}

func (s *AMemorixHostService) startupQueueFailedPath() string {
	return filepath.Join(s.GetRuntimeDataDir(), "startup_write_queue.failed.jsonl")
}

// appendJSONL 加队列锁后追加，对照 _append_jsonl
func (s *AMemorixHostService) appendJSONL(path string, payload any) error {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	return s.appendJSONLSync(path, payload)
}

// appendJSONLSync 调用方需自行持有 queueMu
func (s *AMemorixHostService) appendJSONLSync(path string, payload any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	line, err := sonic.Marshal(payload)
	if err != nil {
		return err
	}
	if err := ensureJSONLAppendBoundary(path); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	data := make([]byte, 0, len(line)+1)
	data = append(data, line...)
	data = append(data, '\n')
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// ensureJSONLAppendBoundary 保证追加前文件以换行结尾，对齐 _ensure_jsonl_append_boundary
func ensureJSONLAppendBoundary(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() <= 0 {
		return nil
	}
	if _, err := file.Seek(-1, io.SeekEnd); err != nil {
		return err
	}
	last := make([]byte, 1)
	if _, err := file.Read(last); err != nil {
		return err
	}
	if last[0] == '\n' {
		return nil
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	if _, err := file.Write([]byte{'\n'}); err != nil {
		return err
	}
	return file.Sync()
}

// readJSONL 对齐 _read_jsonl：跳过空行与坏行，只收对象
func readJSONL(path string) ([]map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	records := make([]map[string]any, 0)
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var record map[string]any
		if err := sonic.UnmarshalString(trimmed, &record); err != nil {
			continue
		}
		if record != nil {
			records = append(records, record)
		}
	}
	return records, nil
}

func (s *AMemorixHostService) queueDoneIDs() (map[string]struct{}, error) {
	records, err := readJSONL(s.startupQueueDonePath())
	if err != nil {
		return nil, err
	}
	done := make(map[string]struct{}, len(records))
	for _, record := range records {
		if id := strings.TrimSpace(anyToString(record["record_id"])); id != "" {
			done[id] = struct{}{}
		}
	}
	return done, nil
}

// startupQueuePendingRecords 对照 _startup_queue_pending_records
func (s *AMemorixHostService) startupQueuePendingRecords() ([]map[string]any, error) {
	done, err := s.queueDoneIDs()
	if err != nil {
		return nil, err
	}
	records, err := readJSONL(s.startupQueuePath())
	if err != nil {
		return nil, err
	}
	allowed := map[string]struct{}{
		"ingest_summary":       {},
		"ingest_text":          {},
		"image_memory":         {},
		"memory_profile_admin": {},
	}
	pending := make([]map[string]any, 0, len(records))
	for _, record := range records {
		recordID := strings.TrimSpace(anyToString(record["record_id"]))
		if recordID == "" {
			continue
		}
		if _, exists := done[recordID]; exists {
			continue
		}
		component := anyToString(record["component_name"])
		if _, ok := allowed[component]; !ok {
			continue
		}
		pending = append(pending, record)
	}
	sort.SliceStable(pending, func(i, j int) bool {
		return recordTimeValue(pending[i]["created_at"]) < recordTimeValue(pending[j]["created_at"])
	})
	return pending, nil
}

// ensureStartupQueueCache 双检缓存，对照 _ensure_startup_queue_cache
func (s *AMemorixHostService) ensureStartupQueueCache() error {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	if s.queueCacheLoaded {
		return nil
	}
	records, err := s.startupQueuePendingRecords()
	if err != nil {
		return err
	}
	s.queuePending = len(records)
	s.queueCacheLoaded = true
	return nil
}

func (s *AMemorixHostService) startupQueuePending() int {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	return s.queuePending
}

func (s *AMemorixHostService) decrementQueuePending() {
	s.queueMu.Lock()
	if s.queuePending > 0 {
		s.queuePending--
	}
	s.queueMu.Unlock()
}

func (s *AMemorixHostService) setQueueCache(records []map[string]any) {
	s.queueMu.Lock()
	s.queuePending = len(records)
	s.queueCacheLoaded = true
	s.queueMu.Unlock()
}

// replayStartupWriteQueue 对照 _replay_startup_write_queue
func (s *AMemorixHostService) replayStartupWriteQueue(ctx context.Context, kernel HostKernel) error {
	records, err := s.startupQueuePendingRecords()
	if err != nil {
		return err
	}
	s.setQueueCache(records)
	if len(records) == 0 {
		return nil
	}
	zap.L().Info("A_Memorix 开始回放启动写入队列", zap.Int("pending", len(records)))

	failures := make(map[string]int)
	order := make([]string, 0)
	for _, record := range records {
		recordID := anyToString(record["record_id"])
		component := anyToString(record["component_name"])
		payload := anyToMap(record["payload"])
		if payload == nil {
			payload = map[string]any{}
		}

		result, dispatchErr := s.dispatchIngestWrite(ctx, kernel, component, payload)
		if dispatchErr == nil {
			if mapped, ok := result.(map[string]any); ok {
				if rawSuccess, exists := mapped["success"]; exists && rawSuccess == false {
					dispatchErr = errors.New(ingestFailureMessage(mapped))
				}
			}
		}
		if dispatchErr != nil {
			message := dispatchErr.Error()
			if _, seen := failures[message]; !seen {
				order = append(order, message)
			}
			failures[message]++
			if appendErr := s.appendJSONL(s.startupQueueFailedPath(), map[string]any{
				"record_id":      recordID,
				"component_name": component,
				"failed_at":      nowUnixFloat(),
				"error":          message,
			}); appendErr != nil {
				return appendErr
			}
			continue
		}

		doneResult := map[string]any{}
		if mapped, ok := result.(map[string]any); ok {
			doneResult = mapped
		}
		// 对齐 Python：done 落盘失败也走失败分支（写 failed 记录后继续），不中止回放
		if appendErr := s.appendJSONL(s.startupQueueDonePath(), map[string]any{
			"record_id":      recordID,
			"component_name": component,
			"finished_at":    nowUnixFloat(),
			"result":         doneResult,
		}); appendErr != nil {
			message := appendErr.Error()
			if _, seen := failures[message]; !seen {
				order = append(order, message)
			}
			failures[message]++
			if failedErr := s.appendJSONL(s.startupQueueFailedPath(), map[string]any{
				"record_id":      recordID,
				"component_name": component,
				"failed_at":      nowUnixFloat(),
				"error":          message,
			}); failedErr != nil {
				return failedErr
			}
			continue
		}
		s.decrementQueuePending()
	}

	if len(failures) > 0 {
		failedCount := 0
		for _, count := range failures {
			failedCount += count
		}
		sort.SliceStable(order, func(i, j int) bool {
			return failures[order[i]] > failures[order[j]]
		})
		reasons := make([]string, 0, 3)
		for index, message := range order {
			if index >= 3 {
				break
			}
			collapsed := strings.Join(strings.Fields(message), " ")
			reasons = append(reasons, fmt.Sprintf("%s（%d 条）", truncateRunes(collapsed, 120), failures[message]))
		}
		summary := strings.Join(reasons, "；")
		if len(order) > 3 {
			summary += fmt.Sprintf("；另有 %d 种原因", len(order)-3)
		}
		zap.L().Warn(fmt.Sprintf(
			"A_Memorix 启动队列回放完成: 成功=%d, 失败=%d；原因: %s；详细记录: %s",
			len(records)-failedCount, failedCount, summary, s.startupQueueFailedPath(),
		))
	}
	return nil
}

func truncateRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

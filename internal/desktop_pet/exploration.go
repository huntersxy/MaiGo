package desktoppet

// 探索任务与产物检索，对齐 desktop_pet/exploration.py 与 exploration_search.py

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

var explorationStoreMu sync.Mutex

const explorationResultVersion = 2

func explorationTasksPath(dataDir string) string {
	return filepath.Join(dataDir, "desktop_pet", "exploration_tasks.json")
}

// ExperienceRef 内容版本指纹，防止任务产物替换后把反馈记到另一条经验
func ExperienceRef(experience map[string]any) string {
	encoded, err := json.Marshal(experience)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

var explorationOutcomes = []string{"success", "partial", "failed"}

// ReuseSummary 统计某条经验的复用反馈
func ReuseSummary(task map[string]any, reference string) map[string]any {
	records := make([]map[string]any, 0)
	for _, raw := range anyList(task["reuse_records"]) {
		entry, _ := raw.(map[string]any)
		if entry == nil || asString(entry["experience_ref"]) != reference {
			continue
		}
		records = append(records, entry)
	}
	counts := map[string]any{"total": len(records)}
	for _, outcome := range explorationOutcomes {
		total := 0
		for _, entry := range records {
			report, _ := entry["report"].(map[string]any)
			if report != nil && asString(report["outcome"]) == outcome {
				total++
			}
		}
		counts[outcome] = total
	}
	recent := make([]any, 0, 5)
	for index := len(records) - 1; index >= 0 && len(recent) < 5; index-- {
		recent = append(recent, records[index])
	}
	counts["recent"] = recent
	var latestSuccess, latestFailure any
	for index := len(records) - 1; index >= 0; index-- {
		report, _ := records[index]["report"].(map[string]any)
		if report == nil {
			continue
		}
		if latestSuccess == nil && asString(report["outcome"]) == "success" {
			latestSuccess = records[index]
		}
		if latestFailure == nil && asString(report["outcome"]) != "success" {
			latestFailure = records[index]
		}
	}
	counts["latest_success"] = latestSuccess
	counts["latest_failure"] = latestFailure
	return counts
}

// RecordExperienceReuse 原子追加复用证据，不改写原实验结论或已结束任务状态
func RecordExperienceReuse(dataDir, sessionID, taskID, reference, reuseKey string, report map[string]any) (map[string]any, error) {
	key := strings.TrimSpace(reuseKey)
	if len([]rune(key)) < 1 || len([]rune(key)) > 100 {
		return nil, fmt.Errorf("reuse_key 必须为本次尝试的唯一标识，1 至 100 字")
	}
	normalizedReport, err := normalizeExperienceReuse(report)
	if err != nil {
		return nil, err
	}
	path := explorationTasksPath(dataDir)
	explorationStoreMu.Lock()
	defer explorationStoreMu.Unlock()
	tasks, err := readExplorationTasks(path)
	if err != nil {
		return nil, err
	}
	task := findExplorationTask(tasks, sessionID, taskID)
	if task == nil {
		return nil, fmt.Errorf("原探索任务不存在或不属于当前会话")
	}
	for _, raw := range anyList(task["reuse_records"]) {
		entry, _ := raw.(map[string]any)
		if entry == nil || asString(entry["reuse_key"]) != key {
			continue
		}
		if asString(entry["experience_ref"]) != reference || !reflect.DeepEqual(entry["report"], normalizedReport) {
			return nil, fmt.Errorf("该 reuse_key 已记录其他反馈；新尝试请使用新标识")
		}
		return map[string]any{"record": entry, "reuse": ReuseSummary(task, reference)}, nil
	}
	validReference := false
	for _, raw := range anyList(task["experiences"]) {
		entry, _ := raw.(map[string]any)
		if entry != nil && ExperienceRef(entry) == reference {
			validReference = true
			break
		}
	}
	if !validReference {
		return nil, fmt.Errorf("经验版本已变化或引用无效，请重新检索，不要猜测经验引用")
	}
	entry := map[string]any{
		"id": uuid.NewString(), "reuse_key": key, "experience_ref": reference,
		"created_at": float64(time.Now().UnixNano()) / 1e9, "report": normalizedReport,
	}
	records := append(anyList(task["reuse_records"]), entry)
	task["reuse_records"] = records
	if err := writeExplorationTasks(path, tasks); err != nil {
		return nil, err
	}
	return map[string]any{"record": entry, "reuse": ReuseSummary(task, reference)}, nil
}

// ExplorationTasks 查询或上报探索任务；状态由明确上报更新
func ExplorationTasks(dataDir, sessionID, action, taskID string, goal, summary, result string, products map[string]any) (any, error) {
	path := explorationTasksPath(dataDir)
	explorationStoreMu.Lock()
	defer explorationStoreMu.Unlock()
	tasks, err := readExplorationTasks(path)
	if err != nil {
		return nil, err
	}
	now := float64(time.Now().UnixNano()) / 1e9
	var item map[string]any
	if action == "list" {
		list := make([]map[string]any, 0, len(tasks))
		for _, task := range tasks {
			if asString(task["session_id"]) == sessionID {
				list = append(list, task)
			}
		}
		return list, nil
	}
	item = findExplorationTask(tasks, sessionID, taskID)
	if item == nil {
		return nil, fmt.Errorf("探索任务不存在或不属于当前会话")
	}
	if action == "get" {
		return item, nil
	}
	switch action {
	case "create":
		validatedGoal, goalErr := validateExplorationGoal(goal)
		if goalErr != nil {
			return nil, goalErr
		}
		item = map[string]any{
			"id": uuid.NewString(), "session_id": sessionID, "goal": validatedGoal,
			"status": "queued", "created_at": now, "updated_at": now, "result": "", "events": []any{},
			"result_version": explorationResultVersion, "resources": []any{}, "experiences": []any{},
		}
		tasks = append(tasks, item)
		summary = "探索任务已记录，等待执行"
	case "progress", "complete", "fail":
		if strings.TrimSpace(summary) == "" || len([]rune(summary)) > 2000 {
			return nil, fmt.Errorf("进度摘要必须为 1 至 2000 字")
		}
		if len([]rune(result)) > 30000 {
			return nil, fmt.Errorf("结果必须是最多 30000 字的文本")
		}
		if action == "complete" && strings.TrimSpace(result) == "" {
			return nil, fmt.Errorf("完成任务必须提供实际结果")
		}
		if asFloat(item["result_version"]) == explorationResultVersion {
			if len([]rune(result)) > 400 {
				return nil, fmt.Errorf("结果概述最多 400 字，详细产物请分为资源和经验")
			}
			if action == "complete" && products == nil {
				return nil, fmt.Errorf("完成任务必须提供 products，包含 resources 和 experiences 两类产物；无产物可为空列表")
			}
		}
		if products != nil {
			validated, err := normalizeExplorationProducts(products)
			if err != nil {
				return nil, err
			}
			item["resources"] = validated["resources"]
			item["experiences"] = validated["experiences"]
		}
		item["status"] = map[string]string{"progress": "running", "complete": "completed", "fail": "failed"}[action]
		item["updated_at"] = now
		item["summary"] = strings.TrimSpace(summary)
		item["result"] = strings.TrimSpace(result)
		events := append(anyList(item["events"]), map[string]any{"summary": strings.TrimSpace(summary), "at": now})
		item["events"] = events
	default:
		return nil, fmt.Errorf("未知探索任务操作")
	}
	if err := writeExplorationTasks(path, tasks); err != nil {
		return nil, err
	}
	return item, nil
}

// SearchExplorationProducts 按关键词检索探索产物，返回有界分页
func SearchExplorationProducts(dataDir, sessionID string, keywords []string, kind string, offset, limit int) (map[string]any, error) {
	if len(keywords) < 1 || len(keywords) > 6 {
		return nil, fmt.Errorf("请提供 1 至 6 个具体关键词")
	}
	for _, word := range keywords {
		if length := len([]rune(strings.TrimSpace(word))); length < 1 || length > 80 {
			return nil, fmt.Errorf("每个关键词必须为 1 至 80 字")
		}
	}
	if kind != "all" && kind != "resource" && kind != "experience" {
		return nil, fmt.Errorf("未知产物分类")
	}
	if offset < 0 || limit < 1 || limit > 10 {
		return nil, fmt.Errorf("offset 必须为非负整数，limit 必须为 1 至 10")
	}
	terms := make([]string, 0, len(keywords))
	seen := map[string]struct{}{}
	for _, word := range keywords {
		term := normalizedExplorationText(strings.TrimSpace(word))
		if _, exists := seen[term]; exists {
			continue
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}
	path := explorationTasksPath(dataDir)
	explorationStoreMu.Lock()
	tasks, err := readExplorationTasks(path)
	explorationStoreMu.Unlock()
	if err != nil {
		return nil, err
	}
	type scored struct {
		score []any
		item  map[string]any
	}
	matches := make([]scored, 0)
	for _, task := range tasks {
		if asString(task["session_id"]) != sessionID {
			continue
		}
		for _, category := range []struct{ name, key string }{{"resource", "resources"}, {"experience", "experiences"}} {
			if kind != "all" && kind != category.name {
				continue
			}
			for index, raw := range anyList(task[category.key]) {
				entry, _ := raw.(map[string]any)
				if entry == nil {
					continue
				}
				title := normalizedExplorationText(asString(entry["title"]))
				reference := ""
				var reuse any
				if category.name == "experience" {
					reference = ExperienceRef(entry)
					reuse = ReuseSummary(task, reference)
				}
				encoded, _ := json.Marshal(entry)
				content := normalizedExplorationText(asString(task["goal"]) + "\n" + string(encoded))
				if reuse != nil {
					reuseJSON, _ := json.Marshal(reuse)
					content += normalizedExplorationText(string(reuseJSON))
				}
				hitTerms := make([]string, 0)
				for _, term := range terms {
					if strings.Contains(content, term) {
						hitTerms = append(hitTerms, term)
					}
				}
				if len(hitTerms) == 0 {
					continue
				}
				titleHits := 0
				for _, term := range terms {
					if strings.Contains(title, term) {
						titleHits++
					}
				}
				item := map[string]any{
					"kind": category.name, "task_id": task["id"], "product_index": index,
					"goal": task["goal"], "task_status": task["status"], "updated_at": task["updated_at"],
					"matched_keywords": hitTerms, "product": entry,
				}
				if reference != "" {
					item["experience_ref"] = reference
					item["reuse"] = reuse
				}
				matches = append(matches, scored{
					score: []any{len(hitTerms), titleHits, asFloat(task["updated_at"])},
					item:  item,
				})
			}
		}
	}
	sort.SliceStable(matches, func(left, right int) bool {
		return compareExplorationScore(matches[left].score, matches[right].score) > 0
	})
	start := offset
	if start > len(matches) {
		start = len(matches)
	}
	end := start + limit
	if end > len(matches) {
		end = len(matches)
	}
	items := make([]map[string]any, 0, end-start)
	for _, match := range matches[start:end] {
		items = append(items, match.item)
	}
	next := start + len(items)
	var nextOffset any
	if next < len(matches) {
		nextOffset = next
	}
	return map[string]any{"items": items, "total": len(matches), "next_offset": nextOffset}, nil
}

func compareExplorationScore(left, right []any) int {
	for index := 0; index < len(left) && index < len(right); index++ {
		leftNumber, _ := asNumber(left[index])
		rightNumber, _ := asNumber(right[index])
		if leftNumber != rightNumber {
			if leftNumber > rightNumber {
				return 1
			}
			return -1
		}
	}
	return 0
}

func validateExplorationGoal(goal string) (string, error) {
	trimmed := strings.TrimSpace(goal)
	if length := len([]rune(trimmed)); length < 1 || length > 2000 {
		return "", fmt.Errorf("探索目标必须为 1 至 2000 字")
	}
	return trimmed, nil
}

func normalizedExplorationText(text string) string {
	return cases.Fold().String(norm.NFKC.String(text))
}

func readExplorationTasks(path string) ([]map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []map[string]any{}, nil
		}
		return nil, err
	}
	decoded := []any{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	tasks := make([]map[string]any, 0, len(decoded))
	for _, item := range decoded {
		if task, ok := item.(map[string]any); ok {
			tasks = append(tasks, task)
		}
	}
	return tasks, nil
}

func writeExplorationTasks(path string, tasks []map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	temporary := strings.TrimSuffix(path, filepath.Ext(path)) + ".tmp"
	if err := os.WriteFile(temporary, raw, 0o666); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func findExplorationTask(tasks []map[string]any, sessionID, taskID string) map[string]any {
	for _, task := range tasks {
		if asString(task["session_id"]) == sessionID && asString(task["id"]) == taskID {
			return task
		}
	}
	return nil
}

func anyList(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case nil:
		return nil
	default:
		return nil
	}
}

func normalizeExperienceReuse(report map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	var reuse ExperienceReuse
	if err := json.Unmarshal(raw, &reuse); err != nil {
		return nil, err
	}
	if err := ValidateExperienceReuse(reuse); err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(reuse)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if err := json.Unmarshal(normalized, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func normalizeExplorationProducts(products map[string]any) (map[string]any, error) {
	if len(products) != 2 || products["resources"] == nil || products["experiences"] == nil {
		return nil, fmt.Errorf("products 必须同时包含 resources 和 experiences")
	}
	raw, err := json.Marshal(products)
	if err != nil {
		return nil, err
	}
	var parsed ExplorationProducts
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	if err := ValidateExplorationProducts(parsed); err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(parsed)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if err := json.Unmarshal(normalized, &out); err != nil {
		return nil, err
	}
	return out, nil
}

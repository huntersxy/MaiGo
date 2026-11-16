package desktoppet

// 桌宠待办存储，窗口线程与 Agent 指令共用，完成即删除，对齐 desktop_pet/todos.py

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// TodoStore 桌宠待办的原子 JSON 存储
type TodoStore struct {
	path  string
	mu    sync.Mutex
	items []map[string]any
}

// NewTodoStore 加载已有待办并补齐字段；未获得回执的派发项回退为 pending
func NewTodoStore(path string) *TodoStore {
	store := &TodoStore{path: path}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &store.items)
	}
	for _, item := range store.items {
		setDefault(item, "condition_type", "natural")
		setDefault(item, "condition", "")
		setDefault(item, "kind", "user")
		setDefault(item, "due_at", nil)
		setDefault(item, "status", "pending")
		if _, ok := item["revision"]; !ok {
			item["revision"] = uuid.NewString()
		}
		if asString(item["status"]) == "dispatching" {
			item["status"] = "pending"
		}
	}
	return store
}

// List 返回全部待办副本
func (s *TodoStore) List() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, len(s.items))
	for index, item := range s.items {
		out[index] = copyAnyMap(item)
	}
	return out
}

// Add 新增待办
func (s *TodoStore) Add(text string, options map[string]any) (map[string]any, error) {
	normalized, err := validateTodoText(text)
	if err != nil {
		return nil, err
	}
	item := map[string]any{
		"id": uuid.NewString(), "text": normalized, "created_at": float64(time.Now().UnixNano()) / 1e9,
		"kind": "user", "due_at": nil, "status": "pending", "condition": "", "condition_type": "natural",
		"revision": uuid.NewString(),
	}
	item, err = applyTodoOptions(item, options)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := append(append([]map[string]any(nil), s.items...), item)
	if err := s.save(items); err != nil {
		return nil, err
	}
	return copyAnyMap(item), nil
}

// Update 修改待办；text 为 nil 时保留原文
func (s *TodoStore) Update(todoID string, text *string, options map[string]any) (map[string]any, error) {
	var normalized *string
	if text != nil {
		value, err := validateTodoText(*text)
		if err != nil {
			return nil, err
		}
		normalized = &value
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, item := range s.items {
		if asString(item["id"]) != todoID {
			continue
		}
		updated := copyAnyMap(item)
		if normalized != nil {
			updated["text"] = *normalized
		}
		updated, err := applyTodoOptions(updated, options)
		if err != nil {
			return nil, err
		}
		changed := false
		for _, key := range []string{"text", "kind", "due_at", "condition", "condition_type"} {
			if !todoValueEqual(updated[key], item[key]) {
				changed = true
				break
			}
		}
		if changed {
			updated["updated_at"] = float64(time.Now().UnixNano()) / 1e9
		}
		updated["revision"] = uuid.NewString()
		if asString(updated["status"]) == "dispatching" {
			updated["status"] = "pending"
		}
		items := append([]map[string]any(nil), s.items...)
		items[index] = updated
		if err := s.save(items); err != nil {
			return nil, err
		}
		return copyAnyMap(updated), nil
	}
	return nil, fmt.Errorf("待办不存在，请重新查询列表")
}

// Done 删除待办（完成即删除）
func (s *TodoStore) Done(todoID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.items {
		if asString(item["id"]) == todoID {
			items := make([]map[string]any, 0, len(s.items)-1)
			for _, entry := range s.items {
				if asString(entry["id"]) != todoID {
					items = append(items, entry)
				}
			}
			return s.save(items)
		}
	}
	return fmt.Errorf("待办不存在，请重新查询列表")
}

// ClaimDue 持锁领取到期且无条件的待办
func (s *TodoStore) ClaimDue() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := float64(time.Now().UnixNano()) / 1e9
	due := make([]map[string]any, 0)
	items := make([]map[string]any, 0, len(s.items))
	for _, item := range s.items {
		if asString(item["status"]) == "pending" && strings.TrimSpace(asString(item["condition"])) == "" &&
			item["due_at"] != nil && asFloat(item["due_at"]) <= now {
			claimed := copyAnyMap(item)
			claimed["status"] = "dispatching"
			due = append(due, claimed)
			items = append(items, claimed)
			continue
		}
		items = append(items, item)
	}
	if len(due) > 0 {
		_ = s.save(items)
	}
	return due
}

// ClaimCondition 模型判断条件后原子领取；版本检查阻止旧上下文触发修改后的事项
func (s *TodoStore) ClaimCondition(todoID, revision, evidence string) (map[string]any, error) {
	trimmed := strings.TrimSpace(evidence)
	if len([]rune(trimmed)) < 1 || len([]rune(trimmed)) > 1000 {
		return nil, fmt.Errorf("请提供 1～1000 字的当前条件成立依据")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, item := range s.items {
		if asString(item["id"]) != todoID {
			continue
		}
		if asString(item["revision"]) != revision {
			return nil, fmt.Errorf("待办已修改或删除，请重新查询")
		}
		if strings.TrimSpace(asString(item["condition"])) == "" || asString(item["status"]) != "pending" {
			return nil, fmt.Errorf("此事项不是待触发的条件待办")
		}
		if item["due_at"] != nil && asFloat(item["due_at"]) > float64(time.Now().UnixNano())/1e9 {
			return nil, fmt.Errorf("尚未到达允许触发的时间")
		}
		claimed := copyAnyMap(item)
		claimed["status"] = "dispatching"
		claimed["evidence"] = trimmed
		items := append([]map[string]any(nil), s.items...)
		items[index] = claimed
		if err := s.save(items); err != nil {
			return nil, err
		}
		return copyAnyMap(claimed), nil
	}
	return nil, fmt.Errorf("待办已修改或删除，请重新查询")
}

// Finish 结算领取结果：失败保留原因，成功后标记 triggered
func (s *TodoStore) Finish(todoID, revision, errorText string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := append([]map[string]any(nil), s.items...)
	for index, item := range items {
		if asString(item["id"]) != todoID || asString(item["revision"]) != revision {
			continue
		}
		updated := copyAnyMap(item)
		if errorText != "" {
			updated["status"] = "failed"
			updated["error"] = errorText
		} else {
			updated["status"] = "triggered"
			updated["error"] = ""
		}
		items[index] = updated
	}
	_ = s.save(items)
}

func (s *TodoStore) save(items []map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o777); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	temporary := strings.TrimSuffix(s.path, filepath.Ext(s.path)) + ".tmp"
	if err := os.WriteFile(temporary, raw, 0o666); err != nil {
		return err
	}
	if err := os.Rename(temporary, s.path); err != nil {
		return err
	}
	s.items = items
	return nil
}

func validateTodoText(text string) (string, error) {
	trimmed := strings.TrimSpace(text)
	if len([]rune(trimmed)) < 1 || len([]rune(trimmed)) > 200 {
		return "", fmt.Errorf("待办内容必须为 1 至 200 字")
	}
	return trimmed, nil
}

// applyTodoOptions 校验并应用待办可选字段，对齐 _apply_options
func applyTodoOptions(item map[string]any, options map[string]any) (map[string]any, error) {
	allowed := map[string]struct{}{"kind": {}, "due_at": {}, "delay_seconds": {}, "condition": {}, "condition_type": {}}
	for key := range options {
		if _, ok := allowed[key]; !ok {
			return nil, fmt.Errorf("未知待办字段")
		}
	}
	if raw, exists := options["condition_type"]; exists {
		conditionType := asString(raw)
		if conditionType != "natural" && conditionType != "app" {
			return nil, fmt.Errorf("条件类型必须为 natural 或 app")
		}
		if conditionType != asString(item["condition_type"]) {
			item["condition_type"] = conditionType
			item["status"] = "pending"
			item["error"] = ""
			item["evidence"] = ""
		}
	}
	if raw, exists := options["condition"]; exists {
		condition, ok := raw.(string)
		if !ok || len([]rune(condition)) > 500 {
			return nil, fmt.Errorf("触发条件必须是最多 500 字的文本，空字符串取消条件")
		}
		if strings.TrimSpace(condition) != asString(item["condition"]) {
			item["condition"] = strings.TrimSpace(condition)
			item["status"] = "pending"
			item["error"] = ""
			item["evidence"] = ""
		}
	}
	if raw, exists := options["kind"]; exists {
		kind := asString(raw)
		if kind != "bot" && kind != "user" {
			return nil, fmt.Errorf("事项类型必须为 bot 或 user")
		}
		item["kind"] = kind
	}
	_, hasDue := options["due_at"]
	_, hasDelay := options["delay_seconds"]
	if hasDue && hasDelay {
		return nil, fmt.Errorf("绝对时间和相对时间只能选择一个")
	}
	if hasDue || hasDelay {
		var due *float64
		if hasDelay {
			delay, ok := asNumber(options["delay_seconds"])
			if !ok || math.IsNaN(delay) || math.IsInf(delay, 0) || delay <= 0 {
				return nil, fmt.Errorf("相对时间必须为大于 0 的秒数")
			}
			value := float64(time.Now().UnixNano())/1e9 + delay
			due = &value
		} else {
			raw := options["due_at"]
			if raw != nil {
				text, ok := raw.(string)
				if !ok {
					return nil, fmt.Errorf("绝对时间须为 ISO 8601 日期时间，或 null 取消定时")
				}
				parsed, err := parseTodoDueAt(text)
				if err != nil {
					return nil, fmt.Errorf("绝对时间须为 ISO 8601 日期时间，或 null 取消定时")
				}
				due = &parsed
			}
		}
		if due != nil && (*due <= float64(time.Now().UnixNano())/1e9) {
			return nil, fmt.Errorf("请设定未来时间")
		}
		item["due_at"] = nil
		if due != nil {
			item["due_at"] = *due
		}
		item["status"] = "pending"
		item["error"] = ""
	}
	if strings.TrimSpace(asString(item["condition"])) == "" && asString(options["condition_type"]) != "app" {
		item["condition_type"] = "natural"
	}
	if asString(item["condition_type"]) == "app" {
		condition := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(asString(item["condition"]))), ".exe")
		if condition == "" {
			return nil, fmt.Errorf("应用检测需要填写进程名或窗口标题关键字")
		}
	}
	return item, nil
}

func parseTodoDueAt(text string) (float64, error) {
	layouts := []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05"}
	for _, layout := range layouts {
		if parsed, err := time.ParseInLocation(layout, strings.TrimSpace(text), time.Local); err == nil {
			return float64(parsed.UnixNano()) / 1e9, nil
		}
	}
	return 0, fmt.Errorf("invalid due_at")
}

func setDefault(item map[string]any, key string, value any) {
	if _, ok := item[key]; !ok {
		item[key] = value
	}
}

func copyAnyMap(source map[string]any) map[string]any {
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func asString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

func asFloat(value any) float64 {
	number, _ := asNumber(value)
	return number
}

func asNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func todoValueEqual(left, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftNumber, leftOK := asNumber(left)
	rightNumber, rightOK := asNumber(right)
	if leftOK && rightOK {
		return leftNumber == rightNumber
	}
	return asString(left) == asString(right)
}

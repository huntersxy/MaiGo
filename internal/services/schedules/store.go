package schedules

// 聊天日程与桌宠事项共用的 JSON 存储，完成后归档留存，对齐 services/schedules/store.py。

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "golang.org/x/image/webp"
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
		raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
		if unmarshalErr := json.Unmarshal(raw, &store.items); unmarshalErr != nil {
			quarantineTodoFile(path)
			store.items = nil
		}
	}
	for _, item := range store.items {
		setDefault(item, "session_id", "")
		setDefault(item, "session_name", "主人私聊")
		setDefault(item, "images", []any{})
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

// quarantineTodoFile 保留损坏文件，并把正式路径释放给下一次写入
func quarantineTodoFile(path string) {
	backup := path + ".corrupt-" + uuid.NewString()
	_ = os.Rename(path, backup)
}

// List 返回未完成待办；includeCompleted 时包含已完成与已取消项
func (s *TodoStore) List(includeCompleted bool) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, 0, len(s.items))
	for _, item := range s.items {
		status := asString(item["status"])
		if !includeCompleted && (status == "completed" || status == "cancelled") {
			continue
		}
		out = append(out, copyAnyMap(item))
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
		"kind": "user", "due_at": nil, "session_id": "", "session_name": "主人私聊", "images": []any{},
		"status": "pending", "condition": "", "condition_type": "natural", "revision": uuid.NewString(),
	}
	item, err = s.applyTodoOptions(item, options)
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

// Update 修改待办；text 为 nil 时保留原文，已完成事项不可修改
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
		if status := asString(item["status"]); status == "completed" || status == "cancelled" {
			return nil, fmt.Errorf("已完成或取消的事项不可修改，请新增待办")
		}
		updated := copyAnyMap(item)
		if normalized != nil {
			updated["text"] = *normalized
		}
		updated, err := s.applyTodoOptions(updated, options)
		if err != nil {
			return nil, err
		}
		changed := false
		for _, key := range []string{"text", "kind", "due_at", "condition", "condition_type", "images"} {
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

// Done 标记完成并归档，保留原内容与触发信息
func (s *TodoStore) Done(todoID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, item := range s.items {
		if asString(item["id"]) != todoID {
			continue
		}
		if asString(item["status"]) == "completed" {
			return nil
		}
		completed := copyAnyMap(item)
		completed["status"] = "completed"
		completed["completed_at"] = float64(time.Now().UnixNano()) / 1e9
		completed["revision"] = uuid.NewString()
		items := append([]map[string]any(nil), s.items...)
		items[index] = completed
		return s.save(items)
	}
	return fmt.Errorf("待办不存在，请重新查询列表")
}

// Delete 删除待办
func (s *TodoStore) Delete(todoID string) error {
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

// Cancel 取消日程并保留记录，更换版本使已领取的旧任务失效
func (s *TodoStore) Cancel(todoID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, item := range s.items {
		if asString(item["id"]) != todoID {
			continue
		}
		status := asString(item["status"])
		if status == "completed" || status == "cancelled" {
			return nil
		}
		cancelled := copyAnyMap(item)
		cancelled["status"] = "cancelled"
		cancelled["cancelled_at"] = float64(time.Now().UnixNano()) / 1e9
		cancelled["revision"] = uuid.NewString()
		items := append([]map[string]any(nil), s.items...)
		items[index] = cancelled
		return s.save(items)
	}
	return fmt.Errorf("待办不存在，请重新查询列表")
}

// ClaimDue 持锁领取到期且无条件的待办；kind 非空时只领取对应类型
func (s *TodoStore) ClaimDue(kind *string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := float64(time.Now().UnixNano()) / 1e9
	due := make([]map[string]any, 0)
	items := make([]map[string]any, 0, len(s.items))
	for _, item := range s.items {
		matches := asString(item["status"]) == "pending" && strings.TrimSpace(asString(item["condition"])) == "" &&
			item["due_at"] != nil && asFloat(item["due_at"]) <= now
		if matches && kind != nil && asString(item["kind"]) != *kind {
			matches = false
		}
		if matches {
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

// Image 读取已保存待办图片的 data URL
func (s *TodoStore) Image(imageID string) (string, error) {
	if len(imageID) != 64 {
		return "", fmt.Errorf("图片标识无效")
	}
	for _, char := range imageID {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return "", fmt.Errorf("图片标识无效")
		}
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(s.path), "todo_images", imageID))
	if err != nil {
		return "", fmt.Errorf("图片不存在")
	}
	mime := mimeForImage(raw)
	if mime == "" {
		return "", fmt.Errorf("图片数据无效")
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw), nil
}

func (s *TodoStore) save(items []map[string]any) error {
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o777); err != nil {
		return err
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return err
	}
	temporary := filepath.Join(directory, "."+filepath.Base(s.path)+"."+uuid.NewString()+".tmp")
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(raw)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(temporary)
		return errors.Join(writeErr, syncErr, closeErr)
	}
	if err := os.Rename(temporary, s.path); err != nil {
		_ = os.Remove(temporary)
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
func (s *TodoStore) applyTodoOptions(item map[string]any, options map[string]any) (map[string]any, error) {
	allowed := map[string]struct{}{
		"kind": {}, "due_at": {}, "delay_seconds": {}, "condition": {}, "condition_type": {},
		"images": {}, "session_id": {}, "session_name": {},
	}
	for key := range options {
		if _, ok := allowed[key]; !ok {
			return nil, fmt.Errorf("未知待办字段")
		}
	}
	for _, key := range []string{"session_id", "session_name"} {
		if raw, exists := options[key]; exists {
			text, ok := raw.(string)
			if !ok {
				return nil, fmt.Errorf("会话归属必须为字符串")
			}
			item[key] = text
		}
	}
	if raw, exists := options["images"]; exists {
		images, ok := raw.([]any)
		if !ok || len(images) > 4 {
			return nil, fmt.Errorf("每个事项最多保存 4 张图片")
		}
		stored := make([]any, 0, len(images))
		for _, image := range images {
			source, ok := image.(string)
			if !ok {
				return nil, fmt.Errorf("图片格式无效")
			}
			id, err := s.storeImage(source)
			if err != nil {
				return nil, err
			}
			stored = append(stored, id)
		}
		item["images"] = stored
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

// storeImage 保存 data URL 或校验已保存附件
func (s *TodoStore) storeImage(source string) (string, error) {
	if !strings.HasPrefix(source, "data:") {
		if len(source) != 64 {
			return "", fmt.Errorf("图片格式无效")
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(s.path), "todo_images", source)); err != nil {
			return "", fmt.Errorf("图片不存在")
		}
		return source, nil
	}
	if len(source) > 12*1024*1024 {
		return "", fmt.Errorf("每张图片不能超过 8 MiB")
	}
	header, encoded, found := strings.Cut(source, ",")
	if !found {
		return "", fmt.Errorf("无法读取图片")
	}
	switch header {
	case "data:image/png;base64", "data:image/jpeg;base64", "data:image/webp;base64", "data:image/gif;base64":
	default:
		return "", fmt.Errorf("仅支持 PNG、JPEG、WebP 或 GIF 图片")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("无法读取图片")
	}
	if len(raw) > 8*1024*1024 {
		return "", fmt.Errorf("每张图片不能超过 8 MiB")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || !supportedTodoImageFormat(format) || config.Width*config.Height > 20_000_000 {
		return "", fmt.Errorf("图片格式不支持或尺寸超过 2000 万像素")
	}
	sum := sha256.Sum256(raw)
	imageID := hex.EncodeToString(sum[:])
	folder := filepath.Join(filepath.Dir(s.path), "todo_images")
	if err := os.MkdirAll(folder, 0o777); err != nil {
		return "", err
	}
	path := filepath.Join(folder, imageID)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, raw, 0o666); err != nil {
			return "", err
		}
	}
	return imageID, nil
}

func supportedTodoImageFormat(format string) bool {
	switch format {
	case "png", "jpeg", "webp", "gif":
		return true
	default:
		return false
	}
}

func mimeForImage(raw []byte) string {
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return ""
	}
	switch format {
	case "png":
		return "image/png"
	case "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	default:
		return ""
	}
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
	if leftList, ok := left.([]any); ok {
		if rightList, ok := right.([]any); ok {
			if len(leftList) != len(rightList) {
				return false
			}
			for index := range leftList {
				if !todoValueEqual(leftList[index], rightList[index]) {
					return false
				}
			}
			return true
		}
	}
	return asString(left) == asString(right)
}

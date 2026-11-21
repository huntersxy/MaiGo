package schedules

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
)

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var imageIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Repository struct {
	directory string
	stores    map[string]*TodoStore
}

func Directory(instanceRoot string) string {
	return filepath.Join(instanceRoot, "data", "schedules")
}

func schedulePath(directory, sessionID string) (string, error) {
	if !sessionIDPattern.MatchString(sessionID) || !filepath.IsLocal(sessionID+".json") {
		return "", errors.New("日程会话 ID 必须为非空的字母、数字、下划线或连字符")
	}
	return filepath.Join(directory, sessionID+".json"), nil
}

func scheduleSessions(directory string) ([]string, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sessions []string
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") || entry.IsDir() {
			continue
		}
		if !entry.Type().IsRegular() {
			return nil, fmt.Errorf("日程文件不是普通文件: %s", entry.Name())
		}
		sessions = append(sessions, strings.TrimSuffix(entry.Name(), ".json"))
	}
	return sessions, nil
}

func readItems(path string, sessionID string) ([]map[string]any, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var items []map[string]any
	if err := json.Unmarshal(bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}), &items); err != nil || items == nil {
		return nil, fmt.Errorf("日程文件必须是 JSON 对象数组: %s", path)
	}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		id, _ := item["id"].(string)
		text, _ := item["text"].(string)
		revision, _ := item["revision"].(string)
		owner, hasOwner := item["session_id"].(string)
		name, _ := item["session_name"].(string)
		images, hasImages := item["images"].([]any)
		condition, hasCondition := item["condition"].(string)
		conditionType, _ := item["condition_type"].(string)
		kind, _ := item["kind"].(string)
		status, _ := item["status"].(string)
		due, hasDue := item["due_at"]
		_, numericDue := due.(float64)
		_, textErr := validateTodoText(text)
		if id == "" || seen[id] || revision == "" || textErr != nil || !hasOwner || name == "" || !hasImages || len(images) > 4 ||
			!hasCondition || len([]rune(condition)) > 500 || !hasDue || (due != nil && !numericDue) ||
			(kind != "bot" && kind != "user") || (conditionType != "natural" && conditionType != "app") ||
			!slices.Contains([]string{"pending", "dispatching", "triggered", "failed", "completed", "cancelled"}, status) {
			return nil, fmt.Errorf("无效日程记录: %s, id=%q", path, id)
		}
		if sessionID != "" && owner != sessionID {
			return nil, fmt.Errorf("日程会话归属与文件名不一致: %s", path)
		}
		for _, rawID := range images {
			imageID, _ := rawID.(string)
			if !imageIDPattern.MatchString(imageID) {
				return nil, fmt.Errorf("日程图片标识无效: %s, id=%q", path, id)
			}
		}
		seen[id] = true
	}
	return items, nil
}

func ReadScheduleItems(directory, sessionID string, includeCompleted bool) ([]map[string]any, error) {
	sessions := []string{sessionID}
	if sessionID == "" {
		var err error
		sessions, err = scheduleSessions(directory)
		if err != nil {
			return nil, err
		}
	}
	result := []map[string]any{}
	for _, id := range sessions {
		path, err := schedulePath(directory, id)
		if err != nil {
			return nil, err
		}
		items, err := readItems(path, id)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if includeCompleted || (item["status"] != "completed" && item["status"] != "cancelled") {
				result = append(result, item)
			}
		}
	}
	return result, nil
}

func openRepository(ctx context.Context, directory, legacyPath string, resolveOwner func(context.Context) (string, error)) (*Repository, error) {
	repository := &Repository{directory: directory, stores: map[string]*TodoStore{}}
	sessions, err := scheduleSessions(directory)
	if err != nil {
		return nil, err
	}
	for _, id := range sessions {
		if _, err := repository.sessionStore(id); err != nil {
			return nil, err
		}
	}
	if legacyPath != "" {
		if err := repository.migrate(ctx, legacyPath, resolveOwner); err != nil {
			return nil, err
		}
	}
	return repository, nil
}

func (repo *Repository) sessionStore(sessionID string) (*TodoStore, error) {
	if store := repo.stores[sessionID]; store != nil {
		return store, nil
	}
	path, err := schedulePath(repo.directory, sessionID)
	if err != nil {
		return nil, err
	}
	items, err := readItems(path, sessionID)
	if err != nil {
		return nil, err
	}
	recoverDispatching(items)
	store := &TodoStore{path: path, items: items}
	repo.stores[sessionID] = store
	return store, nil
}

func (repo *Repository) migrate(ctx context.Context, legacyPath string, resolveOwner func(context.Context) (string, error)) error {
	if _, err := os.Stat(legacyPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	items, err := readItems(legacyPath, "")
	if err != nil {
		return err
	}
	recoverDispatching(items)
	groups := map[string][]map[string]any{}
	owner := ""
	for _, item := range items {
		sessionID := asString(item["session_id"])
		if sessionID == "" {
			if owner == "" {
				if resolveOwner == nil {
					return errors.New("无法确定日程所属的主人聊天流，请先建立唯一的主人会话")
				}
				owner, err = resolveOwner(ctx)
				if err != nil {
					return err
				}
			}
			sessionID = owner
			item["session_id"] = sessionID
		}
		if _, err := schedulePath(repo.directory, sessionID); err != nil {
			return err
		}
		groups[sessionID] = append(groups[sessionID], item)
	}
	for _, item := range items {
		for _, rawID := range item["images"].([]any) {
			id := rawID.(string)
			raw, err := os.ReadFile(filepath.Join(filepath.Dir(legacyPath), "todo_images", id))
			if err != nil {
				return err
			}
			hash := sha256.Sum256(raw)
			if hex.EncodeToString(hash[:]) != id {
				return fmt.Errorf("旧日程图片内容与标识不一致: %s", id)
			}
			destination := filepath.Join(repo.directory, "todo_images", id)
			if existing, err := os.ReadFile(destination); errors.Is(err, os.ErrNotExist) {
				if err := writePrivateBytes(destination, raw); err != nil {
					return err
				}
			} else if err != nil {
				return err
			} else if !bytes.Equal(existing, raw) {
				return fmt.Errorf("已有日程图片内容与标识不一致: %s", id)
			}
		}
	}
	for sessionID, migrated := range groups {
		store, err := repo.sessionStore(sessionID)
		if err != nil {
			return err
		}
		existing := store.List(true)
		seen := map[string]bool{}
		for _, item := range existing {
			seen[asString(item["id"])] = true
		}
		originalCount := len(existing)
		for _, item := range migrated {
			if !seen[asString(item["id"])] {
				existing = append(existing, item)
			}
		}
		if len(existing) != originalCount {
			if err := store.save(existing); err != nil {
				return err
			}
		}
	}
	if err := os.Rename(legacyPath, legacyPath+".migrated-"+newScheduleID()+".bak"); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(legacyPath))
}

func (repo *Repository) sessionIDs() []string {
	ids := make([]string, 0, len(repo.stores))
	for id := range repo.stores {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (repo *Repository) list(sessionID string, includeCompleted bool) ([]map[string]any, error) {
	if sessionID != "" {
		store, err := repo.sessionStore(sessionID)
		if err != nil {
			return nil, err
		}
		return store.List(includeCompleted), nil
	}
	items := []map[string]any{}
	for _, id := range repo.sessionIDs() {
		items = append(items, repo.stores[id].List(includeCompleted)...)
	}
	return items, nil
}

func (repo *Repository) execute(ctx context.Context, command map[string]any, sessionID string, hooks Hooks) (map[string]any, error) {
	options, _ := command["options"].(map[string]any)
	switch asString(command["action"]) {
	case "list":
		includeCompleted, _ := command["include_completed"].(bool)
		items, err := repo.list(sessionID, includeCompleted)
		return map[string]any{"items": items}, err
	case "image":
		data, err := readImage(filepath.Join(repo.directory, "todo_images"), asString(command["image_id"]))
		return map[string]any{"data_url": data}, err
	case "claim_due":
		store, err := repo.sessionStore(sessionID)
		if err != nil {
			return nil, err
		}
		return ExecuteTodo(store, command)
	case "add":
		target := sessionID
		if target == "" {
			target, _ = options["session_id"].(string)
		}
		if target == "" {
			if hooks.OwnerSession == nil {
				return nil, errors.New("无法确定日程所属的主人聊天流，请先建立唯一的主人会话")
			}
			var err error
			target, err = hooks.OwnerSession(ctx)
			if err != nil {
				return nil, err
			}
		}
		if actual, present := options["session_id"]; present && actual != target {
			return nil, errors.New("不能修改日程所属聊天")
		}
		options = copyAnyMap(options)
		options["session_id"] = target
		if _, present := options["session_name"]; !present {
			if hooks.SessionName == nil {
				return nil, errors.New("无法获取真实聊天名称")
			}
			name, err := hooks.SessionName(ctx, target)
			if err != nil {
				return nil, err
			}
			if name == "" {
				return nil, errors.New("无法获取真实聊天名称")
			}
			options["session_name"] = name
		}
		store, err := repo.sessionStore(target)
		if err != nil {
			return nil, err
		}
		command = copyAnyMap(command)
		command["options"] = options
		return ExecuteTodo(store, command)
	default:
		store, _, err := repo.find(sessionID, asString(command["todo_id"]))
		if err != nil {
			return nil, err
		}
		owner := strings.TrimSuffix(filepath.Base(store.path), ".json")
		if actual, present := options["session_id"]; present && actual != owner {
			return nil, errors.New("不能修改日程所属聊天")
		}
		return ExecuteTodo(store, command)
	}
}

func (repo *Repository) find(sessionID, todoID string) (*TodoStore, map[string]any, error) {
	ids := []string{sessionID}
	if sessionID == "" {
		ids = repo.sessionIDs()
	}
	for _, id := range ids {
		store, err := repo.sessionStore(id)
		if err != nil {
			return nil, nil, err
		}
		for _, item := range store.List(true) {
			if item["id"] == todoID {
				return store, item, nil
			}
		}
	}
	return nil, nil, errors.New("当前聊天中不存在此日程，请先查询真实 ID")
}

func recoverDispatching(items []map[string]any) {
	for _, item := range items {
		if item["status"] == "dispatching" {
			item["status"] = "pending"
		}
	}
}

func writePrivateBytes(path string, raw []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(raw)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func syncDirectory(directory string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

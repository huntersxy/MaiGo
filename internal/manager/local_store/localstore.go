// Package localstore 对应 MaiBot src/manager/local_store_manager.py：
// data/local_store.json 的加载、原子保存与损坏恢复
package localstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// DefaultPath 与 MaiBot 的 LOCAL_STORE_FILE_PATH 一致
const DefaultPath = "data/local_store.json"

// Store 本地 JSON 记事本，读写都过一把互斥锁
type Store struct {
	mu   sync.Mutex
	path string
	data map[string]any
}

// Open 读取本地存储；文件不存在时创建，损坏时先备份再重建
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultPath
	}
	store := &Store{path: path, data: map[string]any{}}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

var (
	defaultOnce  sync.Once
	defaultStore *Store
	defaultErr   error
)

// Default 返回进程级单例，对应 Python 的 local_storage = LocalStoreManager("data/local_store.json")
func Default() (*Store, error) {
	defaultOnce.Do(func() {
		defaultStore, defaultErr = Open(DefaultPath)
	})
	return defaultStore, defaultErr
}

// Path 返回存储文件路径
func (s *Store) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

// Get 读取一个键，键不存在返回 nil
func (s *Store) Get(key string) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[key]
}

// Has 判断键是否存在
func (s *Store) Has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[key]
	return ok
}

// Set 写入一个键并立即落盘
func (s *Store) Set(key string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return s.write()
}

// Delete 删除一个键并立即落盘；不存在的键返回 false
func (s *Store) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[key]; !ok {
		return nil
	}
	delete(s.data, key)
	return s.write()
}

// Apply 批量写入，value 为 nil 表示删除该键；只落盘一次
func (s *Store) Apply(changes map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, value := range changes {
		if value == nil {
			delete(s.data, key)
			continue
		}
		s.data[key] = value
	}
	return s.write()
}

// All 返回存储副本
func (s *Store) All() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]any, len(s.data))
	for key, value := range s.data {
		out[key] = value
	}
	return out
}

func (s *Store) load() error {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s.write()
	}
	if err != nil {
		return fmt.Errorf("读取本地存储失败: %w", err)
	}
	var loaded map[string]any
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return s.rebuildAfterCorruption()
	}
	if loaded == nil {
		return s.rebuildAfterCorruption()
	}
	s.data = loaded
	return nil
}

// rebuildAfterCorruption 先备份现场再写入空对象，避免直接覆盖损坏文件
func (s *Store) rebuildAfterCorruption() error {
	backup := s.nextBackupPath()
	if err := os.Rename(s.path, backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("备份损坏的本地存储失败: %w", err)
	}
	s.data = map[string]any{}
	return s.write()
}

func (s *Store) nextBackupPath() string {
	candidate := s.path + ".corrupt"
	if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
		return candidate
	}
	for index := 1; ; index++ {
		candidate = fmt.Sprintf("%s.corrupt.%d", s.path, index)
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
}

// write 先写同目录临时文件再原子替换，最后清理临时文件
func (s *Store) write() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("创建本地存储目录失败: %w", err)
	}
	tempFile, err := os.CreateTemp(filepath.Dir(s.path), "."+filepath.Base(s.path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("创建本地存储临时文件失败: %w", err)
	}
	tempPath := tempFile.Name()
	defer func() {
		if _, statErr := os.Stat(tempPath); statErr == nil {
			_ = os.Remove(tempPath)
		}
	}()
	encoder := json.NewEncoder(tempFile)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "    ")
	if err := encoder.Encode(s.data); err != nil {
		_ = tempFile.Close()
		return fmt.Errorf("序列化本地存储失败: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("关闭本地存储临时文件失败: %w", err)
	}
	if err := os.Rename(tempPath, s.path); err != nil {
		return fmt.Errorf("替换本地存储失败: %w", err)
	}
	return nil
}

package qq_official

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type idMapping struct {
	Group map[string]string `json:"group"`
	User  map[string]string `json:"user"`
}

func loadIDMapping(directory string) (idMapping, error) {
	data, err := os.ReadFile(filepath.Join(directory, "id_map.json"))
	if errors.Is(err, os.ErrNotExist) {
		return idMapping{}, nil
	}
	if err != nil {
		return idMapping{}, err
	}
	var mapping idMapping
	if err := json.Unmarshal(data, &mapping); err != nil {
		return idMapping{}, fmt.Errorf("QQ 统一 ID 映射文件格式无效: %w", err)
	}
	for _, entries := range []map[string]string{mapping.Group, mapping.User} {
		if entries == nil {
			return idMapping{}, fmt.Errorf("QQ 统一 ID 映射须包含 group 和 user 对象")
		}
		for key, value := range entries {
			if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
				return idMapping{}, fmt.Errorf("QQ 统一 ID 映射须包含非空字符串键值")
			}
		}
	}
	return mapping, nil
}

func (m idMapping) reverse(scene string) map[string]string {
	entries := m.User
	if scene == "group" {
		entries = m.Group
	}
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(entries))
	for _, key := range keys {
		out[entries[key]] = key
	}
	return out
}

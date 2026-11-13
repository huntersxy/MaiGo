// Package utils 是 WebUI 的文件处理工具层。
//
// 对齐 MaiBot src/webui/utils/toml_utils.py。go-toml/v2 只做 unmarshal/marshal、
// 不携带注释，因此写回时复用 config 包的保注释合并：未变更节整节复用原文，
// 变更节保留节上方/键上方注释与键尾注释。
package utils

import (
	"fmt"
	"os"
	"path/filepath"

	maibotcfg "maibot-go/internal/config/maibot"

	"github.com/pelletier/go-toml/v2"
)

// ReadTOMLDocument 读取 TOML 为通用 map
func ReadTOMLDocument(path string) (map[string]any, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// MarshalTOML 把通用 map 序列化为 TOML 字节
func MarshalTOML(doc map[string]any) ([]byte, error) {
	return toml.Marshal(doc)
}

// WriteFileAtomic 原子写入：先写同目录临时文件再 rename，避免半截文件
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// WriteTOMLDocument 以 TOML 序列化并原子写入，保留原文件注释
//
// 对齐 tomlkit 的保注释写回：读取现有文件后按节合并，未变更节原样保留，
// 变更节保留节/键注释与键尾注释。
func WriteTOMLDocument(path string, doc map[string]any) error {
	raw, err := MarshalTOML(doc)
	if err != nil {
		return err
	}
	original, readErr := os.ReadFile(filepath.Clean(path))
	if readErr == nil {
		raw = maibotcfg.MergeTOMLPreservingComments(original, raw)
	}
	return WriteFileAtomic(path, raw, 0o644)
}

// CoerceIntegralFloats 将 JSON 反序列化产生的整数值 float64 转为 int64
//
// JSON 数字统一解成 float64，直接写回 TOML 会把 3000 变成 3000.0，
// 与 Python 保存的整数形态不一致。
func CoerceIntegralFloats(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = CoerceIntegralFloats(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = CoerceIntegralFloats(item)
		}
		return out
	case float64:
		if typed == float64(int64(typed)) {
			return int64(typed)
		}
		return typed
	default:
		return value
	}
}

// ValidateTOML 校验一段 TOML 是否可解析，用于写盘前自检
func ValidateTOML(data []byte) error {
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("TOML 校验失败: %w", err)
	}
	return nil
}

package filesvc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"gorm.io/gorm"
)

// SourceKey 计算适配器来源的去重键，等价 Python 的
// sha256(json.dumps(source, sort_keys=True).encode()).hexdigest()
func SourceKey(source map[string]any) (string, error) {
	raw, err := marshalPythonJSON(source)
	if err != nil {
		return "", fmt.Errorf("序列化文件来源失败: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// marshalPythonJSON 复刻 Python json.dumps(source, sort_keys=True) 的字节序列
//
// 默认 ensure_ascii=True、separators=(", ", ": ")，Go 的 json.Marshal 使用紧凑
// 分隔符且不转义非 ASCII，直接使用会让 Python 和 Go 的 source_key 无法对齐。
func marshalPythonJSON(value any) ([]byte, error) {
	var builder strings.Builder
	if err := writePythonJSON(&builder, value); err != nil {
		return nil, err
	}
	return []byte(builder.String()), nil
}

func writePythonJSON(builder *strings.Builder, value any) error {
	switch typed := value.(type) {
	case nil:
		builder.WriteString("null")
	case bool:
		if typed {
			builder.WriteString("true")
		} else {
			builder.WriteString("false")
		}
	case string:
		writePythonJSONString(builder, typed)
	case int:
		builder.WriteString(strconv.FormatInt(int64(typed), 10))
	case int8:
		builder.WriteString(strconv.FormatInt(int64(typed), 10))
	case int16:
		builder.WriteString(strconv.FormatInt(int64(typed), 10))
	case int32:
		builder.WriteString(strconv.FormatInt(int64(typed), 10))
	case int64:
		builder.WriteString(strconv.FormatInt(typed, 10))
	case uint:
		builder.WriteString(strconv.FormatUint(uint64(typed), 10))
	case uint8:
		builder.WriteString(strconv.FormatUint(uint64(typed), 10))
	case uint16:
		builder.WriteString(strconv.FormatUint(uint64(typed), 10))
	case uint32:
		builder.WriteString(strconv.FormatUint(uint64(typed), 10))
	case uint64:
		builder.WriteString(strconv.FormatUint(typed, 10))
	case float32:
		writePythonJSONFloat(builder, float64(typed))
	case float64:
		writePythonJSONFloat(builder, typed)
	case json.Number:
		builder.WriteString(typed.String())
	case []any:
		builder.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				builder.WriteString(", ")
			}
			if err := writePythonJSON(builder, item); err != nil {
				return err
			}
		}
		builder.WriteByte(']')
	case []string:
		builder.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				builder.WriteString(", ")
			}
			writePythonJSONString(builder, item)
		}
		builder.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		builder.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				builder.WriteString(", ")
			}
			writePythonJSONString(builder, key)
			builder.WriteString(": ")
			if err := writePythonJSON(builder, typed[key]); err != nil {
				return err
			}
		}
		builder.WriteByte('}')
	case map[string]string:
		converted := make(map[string]any, len(typed))
		for key, item := range typed {
			converted[key] = item
		}
		return writePythonJSON(builder, converted)
	default:
		return fmt.Errorf("不支持的文件来源类型 %T", value)
	}
	return nil
}

func writePythonJSONString(builder *strings.Builder, value string) {
	builder.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			builder.WriteString("\\\"")
		case '\\':
			builder.WriteString("\\\\")
		case '\b':
			builder.WriteString("\\b")
		case '\f':
			builder.WriteString("\\f")
		case '\n':
			builder.WriteString("\\n")
		case '\r':
			builder.WriteString("\\r")
		case '\t':
			builder.WriteString("\\t")
		default:
			if r >= 0x20 && r <= 0x7e {
				builder.WriteRune(r)
				continue
			}
			if r <= 0xffff {
				fmt.Fprintf(builder, "\\u%04x", r)
				continue
			}
			r -= 0x10000
			high := 0xd800 + (r >> 10)
			low := 0xdc00 + (r & 0x3ff)
			fmt.Fprintf(builder, "\\u%04x\\u%04x", high, low)
		}
	}
	builder.WriteByte('"')
}

func writePythonJSONFloat(builder *strings.Builder, value float64) {
	if math.IsNaN(value) {
		builder.WriteString("NaN")
		return
	}
	if math.IsInf(value, 1) {
		builder.WriteString("Infinity")
		return
	}
	if math.IsInf(value, -1) {
		builder.WriteString("-Infinity")
		return
	}
	absolute := math.Abs(value)
	var text string
	if absolute != 0 && (absolute < 1e-4 || absolute >= 1e16) {
		text = strconv.FormatFloat(value, 'e', -1, 64)
	} else {
		text = strconv.FormatFloat(value, 'f', -1, 64)
		if !strings.ContainsAny(text, ".eE") {
			text += ".0"
		}
	}
	builder.WriteString(text)
}

// RegisterRemote 登记平台远程附件；同一来源键复用已有 file_ref 并补会话绑定
func (s *Store) RegisterRemote(sessionID, messageID, name, mimeType string, size int64, source map[string]any, sourceKey string) (Asset, error) {
	name, err := NormalizeName(name)
	if err != nil {
		return Asset{}, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return Asset{}, fmt.Errorf("session_id 不能为空")
	}
	sourceKey = strings.TrimSpace(sourceKey)
	if len(sourceKey) != 64 {
		return Asset{}, fmt.Errorf("无效的附件来源键")
	}
	if source == nil {
		source = map[string]any{}
	}
	rawSource, err := json.Marshal(source)
	if err != nil {
		return Asset{}, fmt.Errorf("序列化文件来源失败: %w", err)
	}
	if size < 0 {
		size = 0
	}
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "application/octet-stream"
	}
	now := nowSeconds()
	var asset Asset
	err = s.db.Transaction(func(tx *gorm.DB) error {
		err := tx.Where("source_key = ?", sourceKey).First(&asset).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fileRef, refErr := newFileRef()
			if refErr != nil {
				return refErr
			}
			key := sourceKey
			asset = Asset{
				FileRef: fileRef, Name: name, MimeType: mimeType, Size: size,
				Origin: "remote", State: "remote", Source: string(rawSource), SourceKey: &key,
				CreatedAt: now, AccessedAt: now,
			}
			if createErr := tx.Create(&asset).Error; createErr != nil {
				return createErr
			}
		} else if err != nil {
			return err
		}
		if asset.State == "deleted" {
			return fmt.Errorf("%w: 附件已被管理员删除", ErrNotFound)
		}
		var binding Binding
		bindingErr := tx.Where("file_ref = ? AND session_id = ?", asset.FileRef, sessionID).First(&binding).Error
		switch {
		case errors.Is(bindingErr, gorm.ErrRecordNotFound):
			return tx.Create(&Binding{
				FileRef: asset.FileRef, SessionID: sessionID, MessageID: messageID, CreatedAt: now,
			}).Error
		case bindingErr != nil:
			return bindingErr
		default:
			return nil
		}
	})
	if err != nil {
		return Asset{}, err
	}
	return asset, nil
}

// BeginDownload 为远程附件预留下载配额并创建临时文件
func (s *Store) BeginDownload(fileRef, sessionID string, admin bool) (Asset, error) {
	if !fileRefPattern.MatchString(fileRef) {
		return Asset{}, ErrInvalidRef
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, err := s.Get(fileRef, sessionID, admin)
	if err != nil {
		return Asset{}, err
	}
	if asset.Origin != "remote" {
		return Asset{}, fmt.Errorf("本地原件不存在，无法从平台重新下载")
	}
	if asset.State == "ready" {
		return asset, nil
	}
	if err := s.reserveLocked(&asset); err != nil {
		return Asset{}, err
	}
	if err := s.db.Model(&Asset{}).Where("file_ref = ?", fileRef).Updates(map[string]any{
		"transfer_id": asset.TransferID, "reserved_bytes": asset.ReservedBytes,
		"transfer_started": asset.TransferStarted, "state": asset.State, "error": "",
	}).Error; err != nil {
		if partial, pathErr := s.partialPath(fileRef); pathErr == nil {
			_ = os.Remove(partial)
		}
		return Asset{}, err
	}
	return asset, nil
}

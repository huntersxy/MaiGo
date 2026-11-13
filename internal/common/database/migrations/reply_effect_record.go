package migration

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

const recordBlobMagic = "MRE2Z\x00"

// marshalCompactJSON 输出与 Python json.dumps(ensure_ascii=False, separators=(",",":")) 等价的紧凑 JSON
func marshalCompactJSON(payload map[string]any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		return nil, err
	}
	// Encoder 会追加换行；紧凑格式不含尾随空白
	encoded := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	// Go 总会转义 U+2028/U+2029，Python ensure_ascii=False 原样输出
	encoded = bytes.ReplaceAll(encoded, []byte(`\u2028`), []byte("\u2028"))
	encoded = bytes.ReplaceAll(encoded, []byte(`\u2029`), []byte("\u2029"))
	return encoded, nil
}

// encodeRecordPayload 对齐 reply_effect_record_codec.encode_record_payload
//
// Python 用 json.dumps(ensure_ascii=False, separators=(",",":"), default=str)：不转义
// HTML 字符、不加空格。Go 默认会转义 <>& 与 U+2028/2029，这里关掉并还原行分隔符。
func encodeRecordPayload(payload map[string]any) ([]byte, error) {
	serialized, err := marshalCompactJSON(payload)
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	writer, err := zlib.NewWriterLevel(&buffer, zlib.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := writer.Write(serialized); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return append([]byte(recordBlobMagic), buffer.Bytes()...), nil
}

// decodeRecordPayload 对齐 reply_effect_record_codec.decode_record_payload
func decodeRecordPayload(recordJSON string, recordBlob []byte) (map[string]any, error) {
	serialized := recordJSON
	if len(recordBlob) > 0 {
		if !bytes.HasPrefix(recordBlob, []byte(recordBlobMagic)) {
			return nil, fmt.Errorf("回复效果压缩记录格式标识无效")
		}
		reader, err := zlib.NewReader(bytes.NewReader(recordBlob[len(recordBlobMagic):]))
		if err != nil {
			return nil, fmt.Errorf("回复效果压缩记录损坏: %w", err)
		}
		decoded, err := io.ReadAll(reader)
		closeErr := reader.Close()
		if err != nil {
			return nil, fmt.Errorf("回复效果压缩记录损坏: %w", err)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("回复效果压缩记录损坏: %w", closeErr)
		}
		if !utf8.Valid(decoded) {
			return nil, fmt.Errorf("回复效果压缩记录损坏")
		}
		serialized = string(decoded)
	}
	return decodeRecordObject(serialized)
}

// migrationInstanceRoot 对齐 get_instance_root：优先 MAIBOT_INSTANCE_DIR，否则进程工作目录
func migrationInstanceRoot() string {
	if root := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR")); root != "" {
		if expanded, err := expandUserPath(root); err == nil {
			root = expanded
		}
		if absolute, err := filepath.Abs(root); err == nil {
			return absolute
		}
		return root
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
}

func expandUserPath(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, "~\\") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

func replyEffectMirrorFiles() ([]string, error) {
	base := filepath.Join(migrationInstanceRoot(), "logs", "maisaka_reply_effect")
	return filepath.Glob(filepath.Join(base, "*", "*.json"))
}

func compressReplyEffectMirror(filePath string) error {
	rawPayload, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	var buffer bytes.Buffer
	writer, err := gzip.NewWriterLevel(&buffer, gzip.BestCompression)
	if err != nil {
		return err
	}
	writer.Header.ModTime = time.Time{}
	if _, err := writer.Write(rawPayload); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	compressed := buffer.Bytes()
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return err
	}
	decoded, err := io.ReadAll(reader)
	closeErr := reader.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if !bytes.Equal(decoded, rawPayload) {
		return fmt.Errorf("回复效果镜像压缩校验失败：%s", filePath)
	}
	compressedPath := filePath + ".gz"
	tempPath := filepath.Join(filepath.Dir(compressedPath), "."+filepath.Base(compressedPath)+".tmp")
	if err := os.WriteFile(tempPath, compressed, 0o666); err != nil {
		return err
	}
	if err := os.Rename(tempPath, compressedPath); err != nil {
		if removeErr := os.Remove(compressedPath); removeErr != nil && !os.IsNotExist(removeErr) {
			_ = os.Remove(tempPath)
			return err
		}
		if retryErr := os.Rename(tempPath, compressedPath); retryErr != nil {
			_ = os.Remove(tempPath)
			return retryErr
		}
	}
	return os.Remove(filePath)
}

func recordsEqual(left, right map[string]any) bool {
	return reflect.DeepEqual(left, right)
}

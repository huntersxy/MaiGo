package filesvc

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// ReadText 读取受支持文本文件的字符区间，offset 以字符计
func (s *Service) ReadText(fileRef, sessionID string, admin bool, offset, limit int, encoding string) (map[string]any, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	if !s.AcquireRead() {
		return nil, fmt.Errorf("文件读取并发数已满")
	}
	defer func() { _ = s.ReleaseRead() }()
	if offset < 0 || limit < 1 || limit > 32000 {
		return nil, fmt.Errorf("文本分页参数无效")
	}
	encoding = strings.ToLower(strings.TrimSpace(encoding))
	switch encoding {
	case "utf-8", "utf-8-sig", "utf-16", "gb18030":
	default:
		return nil, fmt.Errorf("不支持的文本编码")
	}
	asset, err := s.store.Get(fileRef, sessionID, admin)
	if err != nil {
		return nil, err
	}
	if s.cfg.MaxTextBytes > 0 && asset.Size > s.cfg.MaxTextBytes {
		return nil, fmt.Errorf("文本解析超过 %d MiB 上限，仍可下载原文件", s.cfg.MaxTextBytes/(1024*1024))
	}
	switch strings.ToLower(filepath.Ext(asset.Name)) {
	case ".txt", ".md", ".markdown", ".csv", ".json":
	default:
		return nil, fmt.Errorf("第一期仅解析 TXT、Markdown、CSV、JSON 文本")
	}
	data, err := s.store.ReadBytes(fileRef, sessionID, admin)
	if err != nil {
		return nil, err
	}
	text, err := decodeText(data, encoding)
	if err != nil {
		_ = s.store.RecordParse(fileRef, encoding, err)
		return nil, err
	}
	if strings.ContainsRune(text, 0) {
		err = fmt.Errorf("文件包含二进制内容")
		_ = s.store.RecordParse(fileRef, encoding, err)
		return nil, err
	}
	_ = s.store.RecordParse(fileRef, encoding, nil)
	runes := []rune(text)
	total := len(runes)
	start := offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	return map[string]any{
		"file_ref": fileRef, "text": string(runes[start:end]), "offset": offset,
		"next_offset": end, "total_characters": total, "untrusted_content": true,
	}, nil
}

// validGB18030 按 GB18030 的字节结构做严格校验
//
// x/text 的 GB18030 decoder 对非法字节会用 U+FFFD 替换而不报错，这里先拒绝
// 结构非法的输入，保持 Python bytes.decode("gb18030") 的严格行为
func validGB18030(data []byte) bool {
	for index := 0; index < len(data); {
		first := data[index]
		switch {
		case first <= 0x7f:
			index++
		case first >= 0x81 && first <= 0xfe:
			if index+1 >= len(data) {
				return false
			}
			second := data[index+1]
			if second >= 0x40 && second <= 0xfe && second != 0x7f {
				index += 2
				continue
			}
			if second >= 0x30 && second <= 0x39 {
				if index+3 >= len(data) {
					return false
				}
				third, fourth := data[index+2], data[index+3]
				if third >= 0x81 && third <= 0xfe && fourth >= 0x30 && fourth <= 0x39 {
					index += 4
					continue
				}
			}
			return false
		default:
			return false
		}
	}
	return true
}

func decodeText(data []byte, encoding string) (string, error) {
	switch encoding {
	case "utf-8-sig":
		data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
		if !utf8.Valid(data) {
			return "", fmt.Errorf("文本不是有效的 UTF-8 编码")
		}
		return string(data), nil
	case "utf-8":
		if !utf8.Valid(data) {
			return "", fmt.Errorf("文本不是有效的 UTF-8 编码")
		}
		return string(data), nil
	case "utf-16":
		// Python 的 utf-16 解码要求 BOM，Go 的 decoder 默认会按小端继续解
		if len(data) < 2 || !((data[0] == 0xff && data[1] == 0xfe) || (data[0] == 0xfe && data[1] == 0xff)) {
			return "", fmt.Errorf("文本不是有效的 UTF-16 编码")
		}
		decoded, _, err := transform.Bytes(unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder(), data)
		if err != nil {
			return "", fmt.Errorf("文本不是有效的 UTF-16 编码")
		}
		return string(decoded), nil
	case "gb18030":
		if !validGB18030(data) {
			return "", fmt.Errorf("文本不是有效的 GB18030 编码")
		}
		decoded, _, err := transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), data)
		if err != nil {
			return "", fmt.Errorf("文本不是有效的 GB18030 编码")
		}
		return string(decoded), nil
	default:
		return "", fmt.Errorf("不支持的文本编码")
	}
}

package filesvc

import (
	"fmt"
	"strings"
)

// RecordParse 记录文本解析结果
func (s *Store) RecordParse(fileRef, encoding string, parseErr error) error {
	if !fileRefPattern.MatchString(fileRef) {
		return ErrInvalidRef
	}
	state := "ready"
	message := ""
	if parseErr != nil {
		state = "failed"
		message = strings.TrimSpace(parseErr.Error())
		if len(message) > 2000 {
			message = message[:2000]
		}
	}
	parse := Parse{
		FileRef: fileRef, ParserVersion: "text-v1", Encoding: encoding,
		State: state, Error: message, UpdatedAt: nowSeconds(),
	}
	if err := s.db.Save(&parse).Error; err != nil {
		return fmt.Errorf("记录文本解析状态失败: %w", err)
	}
	return nil
}

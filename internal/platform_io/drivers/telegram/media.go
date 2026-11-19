package telegram

import (
	"fmt"
)

// PrepareInboundMedia P0 stub：暂不下载媒体，仅保留文本路径
func PrepareInboundMedia(_ map[string]any) ([]map[string]any, error) {
	return nil, nil
}

// MediaPlaceholder 出站媒体占位（P0）
func MediaPlaceholder(kind string) string {
	return fmt.Sprintf("[%s]", kind)
}

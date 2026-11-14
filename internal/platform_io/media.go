package platformio

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// maxSegmentMediaBytes 单段媒体读取上限，避免超大文件占满内存
const maxSegmentMediaBytes = 64 << 20

// SegmentMediaBase64 读取消息段的媒体内容并编码为 base64
// 数据来源优先级：段内 base64 → base64:// 引用 → http(s) 下载 → 本地文件
func SegmentMediaBase64(ctx context.Context, seg Segment) (string, error) {
	if seg.Extra != nil {
		if encoded := strings.TrimSpace(seg.Extra["base64"]); encoded != "" {
			return encoded, nil
		}
	}
	source := strings.TrimSpace(seg.URL)
	if source == "" && seg.Extra != nil {
		source = strings.TrimSpace(seg.Extra["file"])
	}
	if source == "" {
		return "", fmt.Errorf("%s 段缺少可读取的媒体内容", seg.Kind)
	}
	lower := strings.ToLower(source)
	switch {
	case strings.HasPrefix(lower, "base64://"):
		encoded := strings.TrimSpace(source[len("base64://"):])
		if encoded == "" {
			return "", fmt.Errorf("%s 段 base64 内容为空", seg.Kind)
		}
		return encoded, nil
	case strings.HasPrefix(lower, "data:image/"):
		comma := strings.Index(source, ",")
		if comma < 0 {
			return "", fmt.Errorf("%s 段 data URL 缺少数据部分", seg.Kind)
		}
		return strings.TrimSpace(source[comma+1:]), nil
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		data, err := fetchSegmentMedia(ctx, source)
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(data), nil
	case strings.HasPrefix(lower, "file://"):
		source = strings.TrimPrefix(source, "file://")
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return "", fmt.Errorf("读取 %s 段文件失败: %w", seg.Kind, err)
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

func fetchSegmentMedia(ctx context.Context, rawURL string) ([]byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("媒体地址无效: %w", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("下载媒体失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("下载媒体失败: HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxSegmentMediaBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取媒体失败: %w", err)
	}
	if len(data) > maxSegmentMediaBytes {
		return nil, fmt.Errorf("媒体超过 %d 字节上限", maxSegmentMediaBytes)
	}
	return data, nil
}

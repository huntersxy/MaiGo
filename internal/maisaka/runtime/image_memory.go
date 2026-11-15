package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"MaiGo/internal/A_memorix"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
)

// imageMemoryInvoker 是 A_Memorix Host 暴露的通用 component 调用能力
type imageMemoryInvoker interface {
	Invoke(ctx context.Context, component string, args map[string]any, timeoutMs *int) (any, error)
}

// searchImageMemory 按消息与组件路径查询 A_Memorix 图片记忆
func searchImageMemory(ctx context.Context, session *Session, sessionID, messageID, componentPath string, limit int) (map[string]any, error) {
	if session == nil {
		return nil, fmt.Errorf("会话未初始化")
	}
	message, ok := session.FindVisibleMessage(messageID)
	if !ok {
		return nil, fmt.Errorf("没有找到指定图片。")
	}
	index := componentPathIndex(componentPath)
	if index < 0 {
		return nil, fmt.Errorf("没有找到指定图片。")
	}
	image := imageBlockAt(message, index)
	if image == nil {
		return nil, fmt.Errorf("没有找到指定图片。")
	}
	contentHash := imageContentHash(image)
	if contentHash == "" {
		return nil, fmt.Errorf("图片缺少可检索的内容哈希。")
	}
	host := memorix.DefaultRegistry().Host()
	invoker, ok := host.(imageMemoryInvoker)
	if !ok {
		return nil, fmt.Errorf("A_Memorix 图片记忆服务未接线。")
	}
	externalRef := fmt.Sprintf("%s:%s:%s", sessionID, messageID, componentPath)
	result, err := invoker.Invoke(ctx, "image_memory", map[string]any{
		"action":               "search",
		"content_hash":         contentHash,
		"current_external_ref": externalRef,
		"chat_id":              sessionID,
		"session_id":           sessionID,
		"candidate_limit":      limit,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("图片记忆检索失败: %w", err)
	}
	if mapped, ok := result.(map[string]any); ok {
		return mapped, nil
	}
	raw, err := sonic.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("图片记忆结果解析失败: %w", err)
	}
	var mapped map[string]any
	if err := sonic.Unmarshal(raw, &mapped); err != nil {
		return nil, fmt.Errorf("图片记忆结果解析失败: %w", err)
	}
	return mapped, nil
}

// componentPathIndex 从组件路径里取最后一个整数序号
func componentPathIndex(componentPath string) int {
	raw := strings.TrimSpace(componentPath)
	if raw == "" {
		return -1
	}
	end := len(raw)
	for end > 0 && (raw[end-1] < '0' || raw[end-1] > '9') {
		end--
	}
	start := end
	for start > 0 && raw[start-1] >= '0' && raw[start-1] <= '9' {
		start--
	}
	if start == end {
		return -1
	}
	index, err := strconv.Atoi(raw[start:end])
	if err != nil || index < 0 {
		return -1
	}
	return index
}

// imageBlockAt 取消息里第 index 个用户图片块
func imageBlockAt(message *schema.AgenticMessage, index int) *schema.UserInputImage {
	if message == nil || index < 0 {
		return nil
	}
	position := 0
	for _, block := range message.ContentBlocks {
		if block == nil || block.Type != schema.ContentBlockTypeUserInputImage || block.UserInputImage == nil {
			continue
		}
		if position == index {
			return block.UserInputImage
		}
		position++
	}
	return nil
}

// imageContentHash 计算图片内容哈希，优先使用二进制数据
func imageContentHash(image *schema.UserInputImage) string {
	if image == nil {
		return ""
	}
	if raw := strings.TrimSpace(image.Base64Data); raw != "" {
		if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil {
			sum := sha256.Sum256(decoded)
			return hex.EncodeToString(sum[:])
		}
		return raw
	}
	if url := strings.TrimSpace(image.URL); url != "" {
		sum := sha256.Sum256([]byte(url))
		return hex.EncodeToString(sum[:])
	}
	return strings.TrimSpace(image.FileID)
}

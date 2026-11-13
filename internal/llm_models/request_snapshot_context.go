package llm

// 失败快照的 request_items 投影
// 对齐 MaiBot context_item/context_snapshot 的字段命名：
//   - 优先使用 spec.ContextItems（嵌入/音频等非 eino 请求）
//   - 否则从 eino 消息投影，reasoning/正文/工具调用共享 logical_turn_id
//   - replay fragment 不写入；ProviderActivity/ProviderOpaque 由消息投影额外生成
//   - 图片写入 assets/ 记录路径与 sha256，不在快照里内联 base64
//   - 音频/视频/文件为 Go 扩展 item（Python 生成请求只支持 text/image），只记录元数据

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

func serializeSnapshotRequestItems(messages []*schema.AgenticMessage, assets *snapshotAssetCollector, logicalTurnID string) []any {
	items, err := ContextItemsFromMessages(messages)
	if err != nil || len(items) == 0 {
		return serializeSnapshotRequestItemsLegacy(messages, assets, logicalTurnID)
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, snapshotContextItem(item, assets, logicalTurnID))
	}
	return out
}

func snapshotContextItem(item ContextItem, assets *snapshotAssetCollector, logicalTurnID string) map[string]any {
	payload := SerializeContextItemSnapshot(item)
	applySnapshotLogicalTurnID(payload, logicalTurnID)
	if parts, ok := payload["parts"].([]any); ok {
		for _, entry := range parts {
			part, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if part["type"] != "image" {
				continue
			}
			encoded, _ := part["image_base64"].(string)
			if strings.TrimSpace(encoded) == "" {
				continue
			}
			format, _ := part["image_format"].(string)
			mimeType := "image/" + format
			if format == "jpg" {
				mimeType = "image/jpeg"
			}
			if path, sum, writeErr := assets.writeImage(encoded, mimeType); writeErr == nil {
				part["image_path"] = path
				part["image_sha256"] = sum
			} else {
				part["image_error"] = writeErr.Error()
			}
			delete(part, "image_base64")
		}
	}
	return payload
}

func serializeSnapshotRequestItemsLegacy(messages []*schema.AgenticMessage, assets *snapshotAssetCollector, logicalTurnID string) []any {
	items := make([]any, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			continue
		}
		switch message.Role {
		case schema.AgenticRoleTypeSystem:
			items = append(items, snapshotMessageItem("SystemMessageItem", message, assets))
		case schema.AgenticRoleTypeAssistant:
			items = append(items, snapshotAssistantItems(message, assets)...)
		default:
			items = append(items, snapshotUserItems(message, assets)...)
		}
	}
	applySnapshotLogicalTurnID(items, logicalTurnID)
	return items
}

// applySnapshotLogicalTurnID 把同一逻辑轮次的固定标识写入所有 Item meta
func applySnapshotLogicalTurnID(value any, logicalTurnID string) {
	if strings.TrimSpace(logicalTurnID) == "" {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		if meta, ok := typed["meta"].(map[string]any); ok {
			meta["logical_turn_id"] = logicalTurnID
		}
		for _, child := range typed {
			applySnapshotLogicalTurnID(child, logicalTurnID)
		}
	case []any:
		for _, child := range typed {
			applySnapshotLogicalTurnID(child, logicalTurnID)
		}
	}
}

func snapshotMeta(logicalTurnID string) map[string]any {
	if strings.TrimSpace(logicalTurnID) == "" {
		logicalTurnID = uuid.NewString()
	}
	return map[string]any{
		"item_id":         uuid.NewString(),
		"logical_turn_id": logicalTurnID,
		"timestamp":       time.Now().Format(time.RFC3339Nano),
	}
}

func snapshotMessageItem(itemType string, message *schema.AgenticMessage, assets *snapshotAssetCollector) map[string]any {
	return map[string]any{
		"item_type": itemType,
		"meta":      snapshotMeta(""),
		"parts":     snapshotMessageParts(message.ContentBlocks, assets),
	}
}

func snapshotUserItems(message *schema.AgenticMessage, assets *snapshotAssetCollector) []any {
	logicalTurnID := uuid.NewString()
	items := make([]any, 0, 2)
	parts := make([]any, 0, len(message.ContentBlocks))
	flush := func() {
		if len(parts) == 0 {
			return
		}
		items = append(items, map[string]any{
			"item_type": "UserMessageItem",
			"meta":      snapshotMeta(logicalTurnID),
			"parts":     parts,
		})
		parts = nil
	}
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		if block.FunctionToolResult != nil {
			flush()
			items = append(items, snapshotToolResultItem(block.FunctionToolResult, logicalTurnID))
			continue
		}
		if part, ok := snapshotContentPart(block, assets); ok {
			parts = append(parts, part)
		}
	}
	flush()
	return items
}

func snapshotAssistantItems(message *schema.AgenticMessage, assets *snapshotAssetCollector) []any {
	logicalTurnID := uuid.NewString()
	items := make([]any, 0, len(message.ContentBlocks))
	parts := make([]any, 0, len(message.ContentBlocks))
	flush := func() {
		if len(parts) == 0 {
			return
		}
		items = append(items, map[string]any{
			"item_type": "AssistantMessageItem",
			"meta":      snapshotMeta(logicalTurnID),
			"parts":     parts,
		})
		parts = nil
	}
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		switch {
		case block.Reasoning != nil:
			flush()
			text := block.Reasoning.Text
			textParts := make([]any, 0, 1)
			if strings.TrimSpace(text) != "" {
				textParts = append(textParts, map[string]any{"text": text})
			}
			items = append(items, map[string]any{
				"item_type":      "ReasoningItem",
				"meta":           snapshotMeta(logicalTurnID),
				"representation": "raw_text",
				"summary_parts":  []any{},
				"text_parts":     textParts,
			})
		case block.FunctionToolCall != nil:
			flush()
			items = append(items, snapshotFunctionCallItem(block.FunctionToolCall, logicalTurnID))
		default:
			if part, ok := snapshotContentPart(block, assets); ok {
				parts = append(parts, part)
			}
		}
	}
	flush()
	return items
}

func snapshotFunctionCallItem(call *schema.FunctionToolCall, logicalTurnID string) map[string]any {
	args := any(map[string]any{})
	if strings.TrimSpace(call.Arguments) != "" {
		var decoded any
		if err := json.Unmarshal([]byte(call.Arguments), &decoded); err == nil {
			args = decoded
		} else {
			args = map[string]any{"unparsed_arguments": call.Arguments}
		}
	}
	toolCall := map[string]any{
		"call_id":   call.CallID,
		"func_name": call.Name,
		"args":      args,
	}
	return map[string]any{
		"item_type": "FunctionCallItem",
		"meta":      snapshotMeta(logicalTurnID),
		"tool_call": toolCall,
	}
}

func snapshotToolResultItem(result *schema.FunctionToolResult, logicalTurnID string) map[string]any {
	output := chatToolResultText(result)
	if strings.TrimSpace(output) == "" && functionToolResultHasMedia(result) {
		output = "[工具返回了非文本内容]"
	}
	return map[string]any{
		"item_type": "FunctionCallOutputItem",
		"meta":      snapshotMeta(logicalTurnID),
		"call_id":   result.CallID,
		"output":    output,
		"tool_name": result.Name,
	}
}

func functionToolResultHasMedia(result *schema.FunctionToolResult) bool {
	if result == nil {
		return false
	}
	for _, block := range result.Content {
		if block == nil {
			continue
		}
		if block.Image != nil || block.Audio != nil || block.Video != nil || block.File != nil {
			return true
		}
	}
	return false
}

func snapshotMessageParts(blocks []*schema.ContentBlock, assets *snapshotAssetCollector) []any {
	parts := make([]any, 0, len(blocks))
	for _, block := range blocks {
		if block == nil {
			continue
		}
		if part, ok := snapshotContentPart(block, assets); ok {
			parts = append(parts, part)
		}
	}
	return parts
}

func snapshotContentPart(block *schema.ContentBlock, assets *snapshotAssetCollector) (any, bool) {
	switch {
	case block.UserInputText != nil:
		return map[string]any{"type": "text", "text": block.UserInputText.Text}, true
	case block.AssistantGenText != nil:
		return map[string]any{"type": "text", "text": block.AssistantGenText.Text}, true
	case block.UserInputImage != nil:
		return snapshotImagePart(block.UserInputImage, assets), true
	case block.AssistantGenImage != nil:
		return snapshotAssistantImagePart(block.AssistantGenImage, assets), true
	case block.UserInputAudio != nil:
		return snapshotMediaPart("audio", block.UserInputAudio.URL, block.UserInputAudio.Base64Data, block.UserInputAudio.FileID, block.UserInputAudio.MIMEType, assets), true
	case block.UserInputVideo != nil:
		return snapshotMediaPart("video", block.UserInputVideo.URL, block.UserInputVideo.Base64Data, block.UserInputVideo.FileID, block.UserInputVideo.MIMEType, assets), true
	case block.UserInputFile != nil:
		return snapshotMediaPart("file", block.UserInputFile.URL, block.UserInputFile.Base64Data, block.UserInputFile.FileID, block.UserInputFile.MIMEType, assets), true
	default:
		return nil, false
	}
}

func snapshotImagePart(image *schema.UserInputImage, assets *snapshotAssetCollector) map[string]any {
	part := map[string]any{
		"type":         "image",
		"image_format": snapshotImageExtension(image.MIMEType),
	}
	if image.URL != "" {
		part["image_url"] = image.URL
	}
	if image.FileID != "" {
		part["file_id"] = image.FileID
	}
	if image.MIMEType != "" {
		part["mime_type"] = image.MIMEType
	}
	if image.Detail != "" {
		part["detail"] = string(image.Detail)
	}
	if strings.TrimSpace(image.Base64Data) != "" {
		if path, sum, err := assets.writeImage(image.Base64Data, image.MIMEType); err == nil {
			part["image_path"] = path
			part["image_sha256"] = sum
		} else {
			part["image_error"] = err.Error()
		}
	}
	return part
}

func snapshotAssistantImagePart(image *schema.AssistantGenImage, assets *snapshotAssetCollector) map[string]any {
	part := map[string]any{
		"type":         "image",
		"image_format": snapshotImageExtension(image.MIMEType),
	}
	if image.URL != "" {
		part["image_url"] = image.URL
	}
	if image.MIMEType != "" {
		part["mime_type"] = image.MIMEType
	}
	if strings.TrimSpace(image.Base64Data) != "" {
		if path, sum, err := assets.writeMedia(image.Base64Data, image.MIMEType, "images"); err == nil {
			part["image_path"] = path
			part["image_sha256"] = sum
		} else {
			part["image_error"] = err.Error()
		}
	}
	return part
}

func snapshotMediaPart(kind, rawURL, base64Data, fileID, mimeType string, assets *snapshotAssetCollector) map[string]any {
	part := map[string]any{"type": kind}
	if rawURL != "" {
		part["url"] = rawURL
	}
	if fileID != "" {
		part["file_id"] = fileID
	}
	if mimeType != "" {
		part["mime_type"] = mimeType
	}
	if strings.TrimSpace(base64Data) != "" {
		if path, sum, err := assets.writeMedia(base64Data, mimeType, kind); err == nil {
			part["media_path"] = path
			part["media_sha256"] = sum
		} else {
			part["media_error"] = err.Error()
		}
	}
	return part
}

func (c *snapshotAssetCollector) writeMedia(data, mimeType, kind string) (string, string, error) {
	decoded, err := decodeSnapshotBase64(data)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(decoded)
	digest := hex.EncodeToString(sum[:])
	extension := snapshotMediaExtension(mimeType, kind)
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return "", "", err
	}
	name := digest + "." + extension
	path := filepath.Join(c.dir, name)
	if _, statErr := os.Stat(path); statErr != nil {
		if writeErr := os.WriteFile(path, decoded, 0o600); writeErr != nil {
			return "", "", writeErr
		}
	}
	return filepath.ToSlash(filepath.Join("assets", name)), digest, nil
}

func snapshotMediaExtension(mimeType, kind string) string {
	normalized := strings.ToLower(strings.TrimSpace(mimeType))
	prefix := kind + "/"
	if strings.HasPrefix(normalized, prefix) {
		subtype := strings.TrimPrefix(normalized, prefix)
		if index := strings.IndexByte(subtype, ';'); index >= 0 {
			subtype = subtype[:index]
		}
		subtype = strings.TrimSpace(subtype)
		switch subtype {
		case "mpeg":
			return "mp3"
		case "quicktime":
			return "mov"
		case "x-wav", "wave":
			return "wav"
		case "plain":
			return "txt"
		}
		if subtype != "" {
			return subtype
		}
	}
	return "bin"
}

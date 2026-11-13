package webuiapi

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

type replayItemMeta struct {
	ItemID        string
	LogicalTurnID *string
	Timestamp     time.Time
}

type replayContentPart struct {
	Type        string
	Text        string
	ImageFormat string
	ImageBase64 string
	Refusal     string
}

type replayToolCall struct {
	CallID       string
	FuncName     string
	Args         map[string]any
	ExtraContent map[string]any
}

type replayContextItem struct {
	ItemType       string
	Meta           replayItemMeta
	Parts          []replayContentPart
	Phase          string
	Representation string
	SummaryParts   []string
	TextParts      []string
	ToolCall       *replayToolCall
	CallID         string
	Output         string
	ToolName       string
	Success        bool
}

func deserializeReplayItems(rawItems []json.RawMessage, instanceDir string) ([]replayContextItem, error) {
	items := make([]replayContextItem, 0, len(rawItems))
	for index, raw := range rawItems {
		item, err := deserializeReplayItem(raw, instanceDir)
		if err != nil {
			return nil, fmt.Errorf("第 %d 个 Item 无效: %w", index+1, err)
		}
		items = append(items, item)
	}
	return items, nil
}

func deserializeReplayItem(raw json.RawMessage, instanceDir string) (replayContextItem, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return replayContextItem{}, fmt.Errorf("Context Item 不是合法 JSON 对象")
	}
	itemType, _ := payload["item_type"].(string)
	itemType = strings.TrimSpace(itemType)
	if itemType == "" {
		return replayContextItem{}, fmt.Errorf("快照中的 item_type 必须是非空字符串")
	}
	meta, err := deserializeReplayMeta(payload["meta"])
	if err != nil {
		return replayContextItem{}, err
	}
	item := replayContextItem{ItemType: itemType, Meta: meta}
	switch itemType {
	case "SystemMessageItem", "UserMessageItem", "AssistantMessageItem":
		parts, err := deserializeReplayParts(payload["parts"], instanceDir)
		if err != nil {
			return replayContextItem{}, err
		}
		item.Parts = parts
		if itemType == "AssistantMessageItem" && payload["phase"] != nil {
			phase, ok := payload["phase"].(string)
			if !ok {
				return replayContextItem{}, fmt.Errorf("AssistantMessageItem phase 必须是字符串或 null")
			}
			item.Phase = phase
		}
	case "ReasoningItem":
		representation := strings.TrimSpace(stringValue(payload["representation"]))
		switch representation {
		case "raw_text", "summary", "opaque":
		default:
			return replayContextItem{}, fmt.Errorf("ReasoningItem representation 不合法: %s", representation)
		}
		item.Representation = representation
		item.SummaryParts, err = replayStringList(payload["summary_parts"], "summary_parts")
		if err != nil {
			return replayContextItem{}, err
		}
		item.TextParts, err = replayStringList(payload["text_parts"], "text_parts")
		if err != nil {
			return replayContextItem{}, err
		}
		if err := validateReplayReasoning(item); err != nil {
			return replayContextItem{}, err
		}
	case "FunctionCallItem":
		call, err := deserializeReplayToolCall(payload["tool_call"])
		if err != nil {
			return replayContextItem{}, err
		}
		item.ToolCall = call
	case "FunctionCallOutputItem":
		item.CallID = stringValue(payload["call_id"])
		item.Output = stringValue(payload["output"])
		item.ToolName = stringValue(payload["tool_name"])
		success, ok := payload["success"].(bool)
		if item.CallID == "" || item.ToolName == "" || !ok {
			return replayContextItem{}, fmt.Errorf("FunctionCallOutputItem 需要字符串 call_id、tool_name 和布尔 success")
		}
		if _, ok := payload["output"].(string); !ok {
			return replayContextItem{}, fmt.Errorf("FunctionCallOutputItem output 必须是字符串")
		}
		item.Success = success
	case "ProviderActivityItem", "ProviderOpaqueItem":
		return replayContextItem{}, fmt.Errorf("重放暂不支持 %s：该 Item 携带供应商私有活动数据", itemType)
	default:
		return replayContextItem{}, fmt.Errorf("不支持的 Context Item 快照类型: %s", itemType)
	}
	return item, nil
}

func deserializeReplayMeta(raw any) (replayItemMeta, error) {
	meta, ok := raw.(map[string]any)
	if !ok {
		return replayItemMeta{}, fmt.Errorf("快照中的 item.meta 必须是字典")
	}
	itemID, _ := meta["item_id"].(string)
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return replayItemMeta{}, fmt.Errorf("快照中的 item.meta.item_id 必须是非空字符串")
	}
	rawLogical, exists := meta["logical_turn_id"]
	if !exists {
		return replayItemMeta{}, fmt.Errorf("快照中的 item.meta 缺少 logical_turn_id")
	}
	var logicalTurnID *string
	if rawLogical != nil {
		value, ok := rawLogical.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return replayItemMeta{}, fmt.Errorf("快照中的 item.meta.logical_turn_id 必须是非空字符串或 null")
		}
		logicalTurnID = &value
	}
	rawTimestamp, ok := meta["timestamp"].(string)
	if !ok || strings.TrimSpace(rawTimestamp) == "" {
		return replayItemMeta{}, fmt.Errorf("快照中的 item.meta.timestamp 必须是非空字符串")
	}
	timestamp, err := parseReplayTimestamp(rawTimestamp)
	if err != nil {
		return replayItemMeta{}, fmt.Errorf("快照中的 item.meta.timestamp 不是合法 ISO 时间")
	}
	return replayItemMeta{ItemID: itemID, LogicalTurnID: logicalTurnID, Timestamp: timestamp}, nil
}

func parseReplayTimestamp(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	formats := []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05"}
	for _, layout := range formats {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid timestamp")
}

func deserializeReplayParts(raw any, instanceDir string) ([]replayContentPart, error) {
	rawParts, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("快照中的 item.parts 必须是列表")
	}
	parts := make([]replayContentPart, 0, len(rawParts))
	for _, rawPart := range rawParts {
		partMap, ok := rawPart.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("快照中的 item part 必须是字典")
		}
		partType := strings.ToLower(strings.TrimSpace(stringValue(partMap["type"])))
		switch partType {
		case "text":
			text, ok := partMap["text"].(string)
			if !ok {
				return nil, fmt.Errorf("文本 part 缺少 text 字段")
			}
			if text == "" {
				return nil, fmt.Errorf("文本内容片段不能为空字符串")
			}
			parts = append(parts, replayContentPart{Type: "text", Text: text})
		case "image":
			imageFormat := strings.ToLower(strings.TrimSpace(stringValue(partMap["image_format"])))
			imageBase64 := stringValue(partMap["image_base64"])
			if imageBase64 == "" {
				hydratedFormat, hydratedBase64, err := hydrateReplayImage(partMap, instanceDir)
				if err != nil {
					return nil, err
				}
				imageFormat, imageBase64 = hydratedFormat, hydratedBase64
			}
			if imageFormat == "" || imageBase64 == "" {
				return nil, fmt.Errorf("图片 part 缺少 image_format 或 image_base64")
			}
			if !supportedReplayImageFormat(imageFormat) {
				return nil, fmt.Errorf("不受支持的图片格式: %s", imageFormat)
			}
			parts = append(parts, replayContentPart{Type: "image", ImageFormat: normalizeReplayImageFormat(imageFormat), ImageBase64: imageBase64})
		case "refusal":
			refusal, ok := partMap["refusal"].(string)
			if !ok || refusal == "" {
				return nil, fmt.Errorf("拒答 part 缺少 refusal 字段")
			}
			parts = append(parts, replayContentPart{Type: "refusal", Refusal: refusal})
		default:
			return nil, fmt.Errorf("不支持的快照 Item part 类型: %s", partType)
		}
	}
	return parts, nil
}

func hydrateReplayImage(part map[string]any, instanceDir string) (string, string, error) {
	imageURL := part["image_url"]
	if nested, ok := imageURL.(map[string]any); ok {
		imageURL = nested["url"]
	}
	if dataURI := stringValue(imageURL); strings.HasPrefix(dataURI, "data:image/") {
		comma := strings.Index(dataURI, ",")
		if comma < 0 {
			return "", "", fmt.Errorf("图片 data URI 缺少 base64 分隔符")
		}
		header := dataURI[:comma]
		meta := strings.TrimPrefix(header, "data:image/")
		meta = strings.TrimSuffix(meta, ";base64")
		return meta, dataURI[comma+1:], nil
	}
	rawRef := stringValue(part["image_path"])
	reference, _ := part["image_reference"].(map[string]any)
	if rawRef == "" && reference != nil {
		rawRef = stringValue(reference["image_path"])
	}
	if rawRef == "" {
		rawRef = stringValue(part["image_uri"])
	}
	if rawRef == "" && reference != nil {
		rawRef = stringValue(reference["image_uri"])
	}
	if rawRef == "" {
		return "", "", fmt.Errorf("图片片段缺少可用于重放的 image_path 或 file:// image_uri")
	}
	path, err := resolveReplayImagePath(rawRef, instanceDir)
	if err != nil {
		return "", "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("读取重放图片失败: %w", err)
	}
	if len(data) == 0 {
		return "", "", fmt.Errorf("重放图片为空: %s", rawRef)
	}
	imageFormat := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	if imageFormat == "" {
		imageFormat = "png"
	}
	return imageFormat, base64.StdEncoding.EncodeToString(data), nil
}

func resolveReplayImagePath(raw string, instanceDir string) (string, error) {
	path := strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(path), "file:") {
		path = strings.TrimPrefix(path, "file:")
		path = strings.TrimPrefix(path, "///")
		path = strings.TrimPrefix(path, "//")
		path = strings.ReplaceAll(path, "/", string(filepath.Separator))
		if len(path) > 2 && path[0] == filepath.Separator && path[2] == ':' {
			path = path[1:]
		}
	}
	if !filepath.IsAbs(path) {
		if strings.TrimSpace(instanceDir) == "" {
			return "", fmt.Errorf("重放图片路径必须是绝对路径或 file:// URI: %s", raw)
		}
		path = filepath.Join(instanceDir, path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("图片引用文件不存在: %s", raw)
	}
	root := strings.TrimSpace(instanceDir)
	if root == "" {
		return "", fmt.Errorf("无法校验图片路径：实例目录未配置")
	}
	allowedRoots := []string{
		filepath.Join(root, "data", "images"),
		filepath.Join(root, "data", "emoji"),
		filepath.Join(root, "data", "prompt_imgs"),
		filepath.Join(root, "data", "html_imgs"),
	}
	for _, allowed := range allowedRoots {
		resolvedRoot, err := filepath.EvalSymlinks(allowed)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(resolvedRoot, resolved); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			info, statErr := os.Stat(resolved)
			if statErr != nil || info.IsDir() {
				return "", fmt.Errorf("图片引用不是文件: %s", raw)
			}
			return resolved, nil
		}
	}
	return "", fmt.Errorf("图片引用路径不在允许目录内: %s", raw)
}

func supportedReplayImageFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "png", "jpeg", "jpg", "webp", "gif":
		return true
	default:
		return false
	}
}

func normalizeReplayImageFormat(format string) string {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "jpg" {
		return "jpeg"
	}
	return format
}

func replayStringList(raw any, field string) ([]string, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("快照中的 %s 必须是字符串列表", field)
	}
	out := make([]string, 0, len(list))
	for _, value := range list {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("快照中的 %s 必须是字符串列表", field)
		}
		out = append(out, text)
	}
	return out, nil
}

func validateReplayReasoning(item replayContextItem) error {
	for _, part := range append(append([]string{}, item.SummaryParts...), item.TextParts...) {
		if part == "" {
			return fmt.Errorf("reasoning 内容片段不能为空字符串")
		}
	}
	switch item.Representation {
	case "summary":
		if len(item.SummaryParts) == 0 {
			return fmt.Errorf("SUMMARY reasoning 必须包含 summary_parts")
		}
	case "raw_text":
		if len(item.TextParts) == 0 {
			return fmt.Errorf("RAW_TEXT reasoning 必须包含 text_parts")
		}
	}
	return nil
}

func deserializeReplayToolCall(raw any) (*replayToolCall, error) {
	payload, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("FunctionCallItem 快照缺少 tool_call")
	}
	callID := stringValue(payload["call_id"])
	funcName := stringValue(payload["func_name"])
	if callID == "" || funcName == "" {
		return nil, fmt.Errorf("FunctionCallItem tool_call 缺少字符串 call_id 或 func_name")
	}
	args, ok := payload["args"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("FunctionCallItem tool_call.args 必须是字典")
	}
	var extra map[string]any
	if payload["extra_content"] != nil {
		extra, ok = payload["extra_content"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("FunctionCallItem tool_call.extra_content 必须是字典或 null")
		}
	}
	return &replayToolCall{CallID: callID, FuncName: funcName, Args: args, ExtraContent: extra}, nil
}

func replayMessages(items []replayContextItem) ([]*schema.AgenticMessage, error) {
	messages := make([]*schema.AgenticMessage, 0, len(items))
	for _, item := range items {
		switch item.ItemType {
		case "SystemMessageItem", "UserMessageItem", "AssistantMessageItem":
			role := schema.AgenticRoleTypeUser
			if item.ItemType == "SystemMessageItem" {
				role = schema.AgenticRoleTypeSystem
			} else if item.ItemType == "AssistantMessageItem" {
				role = schema.AgenticRoleTypeAssistant
			}
			blocks := make([]*schema.ContentBlock, 0, len(item.Parts))
			for _, part := range item.Parts {
				switch part.Type {
				case "text":
					if role == schema.AgenticRoleTypeAssistant {
						blocks = append(blocks, schema.NewContentBlock(&schema.AssistantGenText{Text: part.Text}))
					} else {
						blocks = append(blocks, schema.NewContentBlock(&schema.UserInputText{Text: part.Text}))
					}
				case "image":
					if role != schema.AgenticRoleTypeUser {
						return nil, fmt.Errorf("%s 暂不支持图片重放", item.ItemType)
					}
					blocks = append(blocks, schema.NewContentBlock(&schema.UserInputImage{Base64Data: part.ImageBase64, MIMEType: "image/" + part.ImageFormat}))
				case "refusal":
					return nil, fmt.Errorf("重放暂不支持 refusal 内容片段")
				}
			}
			if len(blocks) == 0 {
				continue
			}
			messages = append(messages, &schema.AgenticMessage{Role: role, ContentBlocks: blocks})
		case "FunctionCallItem":
			if item.ToolCall == nil {
				return nil, fmt.Errorf("FunctionCallItem 缺少 tool_call")
			}
			args, err := json.Marshal(item.ToolCall.Args)
			if err != nil {
				return nil, fmt.Errorf("序列化工具参数失败: %w", err)
			}
			messages = append(messages, &schema.AgenticMessage{
				Role: schema.AgenticRoleTypeAssistant,
				ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolCall{
					CallID:    item.ToolCall.CallID,
					Name:      item.ToolCall.FuncName,
					Arguments: string(args),
				})},
			})
		case "FunctionCallOutputItem":
			messages = append(messages, &schema.AgenticMessage{
				Role: schema.AgenticRoleTypeUser,
				ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.FunctionToolResult{
					CallID: item.CallID,
					Name:   item.ToolName,
					Content: []*schema.FunctionToolResultContentBlock{{
						Type: schema.FunctionToolResultContentBlockTypeText,
						Text: &schema.UserInputText{Text: item.Output},
					}},
				})},
			})
		case "ReasoningItem":
			text := strings.Join(item.TextParts, "\n")
			if text == "" {
				text = strings.Join(item.SummaryParts, "\n")
			}
			if text == "" {
				continue
			}
			messages = append(messages, &schema.AgenticMessage{
				Role:          schema.AgenticRoleTypeAssistant,
				ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.Reasoning{Text: text})},
			})
		default:
			return nil, fmt.Errorf("重放暂不支持 %s", item.ItemType)
		}
	}
	return messages, nil
}

func replayOutputItems(message *schema.AgenticMessage, now time.Time) ([]map[string]any, error) {
	if message == nil {
		return []map[string]any{}, nil
	}
	output := make([]map[string]any, 0, len(message.ContentBlocks))
	assistantParts := make([]any, 0)
	flushAssistant := func() {
		if len(assistantParts) == 0 {
			return
		}
		output = append(output, map[string]any{
			"item_type": "AssistantMessageItem",
			"meta":      newReplayMeta(now),
			"parts":     assistantParts,
		})
		assistantParts = nil
	}
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		if block.Reasoning != nil && strings.TrimSpace(block.Reasoning.Text) != "" {
			flushAssistant()
			output = append(output, map[string]any{
				"item_type":      "ReasoningItem",
				"meta":           newReplayMeta(now),
				"representation": "raw_text",
				"summary_parts":  []string{},
				"text_parts":     []string{block.Reasoning.Text},
			})
			continue
		}
		if block.AssistantGenText != nil && block.AssistantGenText.Text != "" {
			assistantParts = append(assistantParts, map[string]any{"type": "text", "text": block.AssistantGenText.Text})
			continue
		}
		if block.FunctionToolCall != nil {
			flushAssistant()
			args := map[string]any{}
			if strings.TrimSpace(block.FunctionToolCall.Arguments) != "" {
				if err := json.Unmarshal([]byte(block.FunctionToolCall.Arguments), &args); err != nil {
					return nil, fmt.Errorf("模型返回的工具参数不是 JSON 对象: %w", err)
				}
			}
			output = append(output, map[string]any{
				"item_type": "FunctionCallItem",
				"meta":      newReplayMeta(now),
				"tool_call": map[string]any{
					"call_id":       block.FunctionToolCall.CallID,
					"func_name":     block.FunctionToolCall.Name,
					"args":          args,
					"extra_content": nil,
				},
			})
			continue
		}
		if block.FunctionToolResult != nil || block.ServerToolCall != nil || block.ServerToolResult != nil ||
			block.AssistantGenImage != nil || block.AssistantGenAudio != nil || block.AssistantGenVideo != nil ||
			block.MCPToolCall != nil || block.MCPToolResult != nil {
			return nil, fmt.Errorf("模型返回了暂不支持的内容块: %s", block.Type)
		}
	}
	flushAssistant()
	return output, nil
}

func newReplayMeta(now time.Time) map[string]any {
	return map[string]any{
		"item_id":         randomReplayID(),
		"logical_turn_id": nil,
		"timestamp":       now.Format(time.RFC3339Nano),
	}
}

func randomReplayID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return hex.EncodeToString(buf)
}

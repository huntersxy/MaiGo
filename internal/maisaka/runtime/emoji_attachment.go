package runtime

// 表情包附件解析与发送，对齐 src/maisaka/builtin_tool/context.py 的
// _resolve_emoji_attachment / load_context_emoji / load_emoji_by_description。

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"MaiGo/internal/common/media"
	"MaiGo/internal/emoji_system"
	"MaiGo/internal/maisaka"
	"MaiGo/internal/maisaka/builtin_tool"

	"go.uber.org/zap"
)

func (a *Agent) resolveEmojiAttachment(ctx context.Context, session *Session, sessionID string, reference any) (tools.EmojiAttachment, error) {
	switch value := reference.(type) {
	case string:
		return loadEmojiByDescription(ctx, value, "", sessionID)
	case map[string]any:
		if _, hasEmotion := value["emotion"]; hasEmotion {
			return resolveSemanticEmojiReference(ctx, value, sessionID)
		}
		if _, hasContent := value["content"]; hasContent {
			return resolveSemanticEmojiReference(ctx, value, sessionID)
		}
		if _, hasDescription := value["description"]; hasDescription {
			return tools.EmojiAttachment{}, fmt.Errorf("语义表情引用只能填写非空的 emotion 和 content 文本")
		}
		return a.resolveContextEmojiReference(ctx, value, session)
	case map[string]string:
		converted := make(map[string]any, len(value))
		for key, item := range value {
			converted[key] = item
		}
		return a.resolveEmojiAttachment(ctx, session, sessionID, converted)
	default:
		return tools.EmojiAttachment{}, fmt.Errorf("attach_emoji 需要上下文表情引用、情绪或画面描述")
	}
}

func resolveSemanticEmojiReference(ctx context.Context, value map[string]any, sessionID string) (tools.EmojiAttachment, error) {
	for key := range value {
		if key != "emotion" && key != "content" {
			return tools.EmojiAttachment{}, fmt.Errorf("语义表情引用只能填写非空的 emotion 和 content 文本")
		}
	}
	emotion, _ := value["emotion"].(string)
	content, _ := value["content"].(string)
	if strings.TrimSpace(emotion) == "" && strings.TrimSpace(content) == "" {
		return tools.EmojiAttachment{}, fmt.Errorf("语义表情引用只能填写非空的 emotion 和 content 文本")
	}
	if (value["emotion"] != nil && strings.TrimSpace(emotion) == "") ||
		(value["content"] != nil && strings.TrimSpace(content) == "") {
		return tools.EmojiAttachment{}, fmt.Errorf("语义表情引用只能填写非空的 emotion 和 content 文本")
	}
	return loadEmojiByDescription(ctx, strings.TrimSpace(emotion), strings.TrimSpace(content), sessionID)
}

func (a *Agent) resolveContextEmojiReference(ctx context.Context, value map[string]any, session *Session) (tools.EmojiAttachment, error) {
	for key := range value {
		if key != "msg_id" && key != "index" {
			return tools.EmojiAttachment{}, fmt.Errorf("上下文表情引用需要提供 msg_id，可选 index")
		}
	}
	sourceID, _ := value["msg_id"].(string)
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return tools.EmojiAttachment{}, fmt.Errorf("上下文表情引用需要提供 msg_id，可选 index")
	}
	index := 0
	if raw, ok := value["index"]; ok && raw != nil {
		switch typed := raw.(type) {
		case float64:
			if typed != float64(int(typed)) {
				return tools.EmojiAttachment{}, fmt.Errorf("上下文表情包序号无效")
			}
			index = int(typed)
		case int:
			index = typed
		case int64:
			index = int(typed)
		default:
			return tools.EmojiAttachment{}, fmt.Errorf("上下文表情包序号无效")
		}
	}
	return a.loadContextEmoji(ctx, session, sourceID, index)
}

func loadEmojiByDescription(ctx context.Context, emotion, content string, sessionID string) (tools.EmojiAttachment, error) {
	if strings.TrimSpace(sessionID) == "" {
		return tools.EmojiAttachment{}, fmt.Errorf("表情语义匹配缺少会话")
	}
	emojiHash, err := emojisystem.MatchEmojiDescription(ctx, emotion, sessionID, content)
	if err != nil {
		return tools.EmojiAttachment{}, err
	}
	return loadEmojiByHash(ctx, emojiHash)
}

func loadEmojiByHash(ctx context.Context, emojiHash string) (tools.EmojiAttachment, error) {
	registry := emojisystem.DefaultRegistry()
	mgr := registry.Manager()
	if mgr == nil {
		return tools.EmojiAttachment{}, fmt.Errorf("表情包管理器未初始化")
	}
	emoji, err := mgr.GetByHash(ctx, strings.TrimSpace(emojiHash))
	if err != nil {
		return tools.EmojiAttachment{}, err
	}
	if emoji == nil {
		return tools.EmojiAttachment{}, fmt.Errorf("表情包 %s 已不可用", emojiHash)
	}
	return *emojiAttachmentFrom(emoji), nil
}

func (a *Agent) loadContextEmoji(ctx context.Context, session *Session, sourceID string, index int) (tools.EmojiAttachment, error) {
	if session == nil {
		return tools.EmojiAttachment{}, fmt.Errorf("上下文中找不到所引用的表情包消息")
	}
	runtime := session.MaisakaRuntime()
	if runtime == nil {
		return tools.EmojiAttachment{}, fmt.Errorf("上下文中找不到所引用的表情包消息")
	}
	history := runtime.History()
	ids := make([]string, 0, len(history))
	for _, message := range history {
		if message.MessageID != "" {
			ids = append(ids, message.MessageID)
		}
	}
	sourceID = strings.TrimSpace(sourceID)
	if expanded, ok := maisaka.ExpandMessageIDAliases(sourceID, maisaka.BuildAliasMap(ids)).(string); ok && expanded != "" {
		sourceID = expanded
	}
	for position := len(history) - 1; position >= 0; position-- {
		message := history[position]
		if message.MessageID != sourceID {
			continue
		}
		return a.contextEmojiComponent(ctx, message, index)
	}
	return tools.EmojiAttachment{}, fmt.Errorf("上下文中找不到所引用的表情包消息")
}

func (a *Agent) contextEmojiComponent(ctx context.Context, message maisaka.ContextMessage, index int) (tools.EmojiAttachment, error) {
	if index < 0 || index >= len(message.Emojis) {
		return tools.EmojiAttachment{}, fmt.Errorf("上下文表情包序号无效，该消息共有 %d 张表情包", len(message.Emojis))
	}
	component := message.Emojis[index]
	content := strings.TrimSpace(component.Description)
	if content == "" {
		content = "[表情包]"
	} else {
		content = "[表情包: " + content + "]"
	}
	attachment := tools.EmojiAttachment{Content: content}
	ref := strings.TrimSpace(component.MediaRef)
	if ref == "" {
		return attachment, fmt.Errorf("上下文表情包文件为空")
	}
	if registry := emojisystem.DefaultRegistry(); registry.Manager() != nil {
		if emoji, err := registry.Manager().GetByHash(ctx, ref); err == nil && emoji != nil {
			attachment = *emojiAttachmentFrom(emoji)
			attachment.Content = content
			return attachment, nil
		}
	}
	if visual := a.agentVisual(); visual != nil && visual.manager != nil {
		if image, err := visual.manager.GetEmojiFromDB(ctx, ref); err == nil && image != nil && strings.TrimSpace(image.FullPath) != "" {
			attachment.Hash = ref
			attachment.Path = image.FullPath
			return attachment, nil
		}
	}
	if info, err := os.Stat(ref); err == nil && !info.IsDir() {
		attachment.Path = ref
		return attachment, nil
	}
	encoded := ref
	if strings.HasPrefix(encoded, "data:") {
		if comma := strings.Index(encoded, ","); comma >= 0 {
			encoded = encoded[comma+1:]
		}
	} else if strings.HasPrefix(encoded, "base64://") {
		encoded = strings.TrimPrefix(encoded, "base64://")
	}
	if len(encoded) >= 64 {
		if decoded, err := base64.StdEncoding.DecodeString(encoded); err == nil && len(decoded) > 64 {
			attachment.Binary = decoded
			return attachment, nil
		}
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		if manager := a.currentMedia(); manager != nil {
			downloaded, err := manager.Ensure(ctx, media.Source{URL: ref, Sticker: true})
			if err == nil && strings.TrimSpace(downloaded.Path) != "" {
				attachment.Path = downloaded.Path
				attachment.Hash = downloaded.Hash
				return attachment, nil
			}
		}
	}
	return attachment, fmt.Errorf("上下文表情包文件为空")
}

func emojiAttachmentFrom(emoji *emojisystem.Emoji) *tools.EmojiAttachment {
	if emoji == nil {
		return &tools.EmojiAttachment{}
	}
	mgr := emojisystem.DefaultRegistry().Manager()
	path := ""
	if mgr != nil {
		path = mgr.EmojiFilePath(emoji)
	}
	content := "[表情包]"
	if description := strings.TrimSpace(emoji.Description); description != "" {
		content = "[表情包: " + description + "]"
	}
	return &tools.EmojiAttachment{Hash: emoji.FileHash, Path: path, Content: content}
}

func (a *Agent) sendEmojiAttachment(ctx context.Context, kind string, targetID int64, attachment tools.EmojiAttachment) error {
	path := strings.TrimSpace(attachment.Path)
	cleanup := false
	if path == "" && len(attachment.Binary) > 0 {
		file, err := os.CreateTemp("", "maigo-emoji-*")
		if err != nil {
			return err
		}
		path = file.Name()
		if _, err := file.Write(attachment.Binary); err != nil {
			file.Close()
			os.Remove(path)
			return err
		}
		if err := file.Close(); err != nil {
			os.Remove(path)
			return err
		}
		cleanup = true
	}
	if path == "" {
		return fmt.Errorf("表情包文件为空")
	}
	if cleanup {
		defer os.Remove(path)
	}
	if _, _, err := a.sendConversationImage(ctx, kind, targetID, path, true); err != nil {
		return err
	}
	if hash := strings.TrimSpace(attachment.Hash); hash != "" {
		if _, err := emojisystem.DefaultRegistry().RecordUsageByHash(ctx, hash); err != nil {
			zap.L().Warn("记录表情包使用次数失败", zap.String("emoji_hash", hash), zap.Error(err))
		}
	}
	return nil
}

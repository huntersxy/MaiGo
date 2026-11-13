package capabilities

import (
	"context"
	"encoding/base64"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"maibot-go/internal/common/database"
	"maibot-go/internal/emoji_system"
)

func pluginStickerEmojis(ctx context.Context, mem *memory.Manager) ([]memory.EmojiRecord, error) {
	if mem == nil {
		return nil, nil
	}
	rows, err := mem.ListRegisteredEmojiRecords(ctx)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func pluginEmojiFromSticker(sticker memory.EmojiRecord) emojisystem.Emoji {
	return emojisystem.Emoji{
		ID:          sticker.ID,
		FileHash:    sticker.ImageHash,
		FullPath:    sticker.FullPath,
		FileName:    filepath.Base(filepath.FromSlash(sticker.FullPath)),
		Description: sticker.Description,
		Emotions:    emojisystem.NormalizeEmotionTags(sticker.Description),
		QueryCount:  sticker.QueryCount,
	}
}

func pluginSerializeEmoji(manager *emojisystem.Manager, emoji *emojisystem.Emoji, fallbackEmotion string) map[string]any {
	if emoji == nil || manager == nil {
		return nil
	}
	serialized := manager.SerializeForHook(emoji)
	path := stringValue(serialized["full_path"])
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	emotion := ""
	if tags := emojisystem.NormalizeEmotionTags(emoji.Description); len(tags) > 0 {
		emotion = tags[0]
	}
	if emotion == "" {
		emotion = fallbackEmotion
	}
	return map[string]any{
		"base64":      base64.StdEncoding.EncodeToString(raw),
		"description": emoji.Description,
		"emotion":     emotion,
	}
}

func stringListValue(raw any) []string {
	switch value := raw.(type) {
	case []string:
		return append([]string(nil), value...)
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func makeEmojiGetByDescriptionHandler(deps HostCapabilityDeps) CapabilityHandler {
	return func(ctx context.Context, _, _ string, args map[string]any) (any, error) {
		description := strings.TrimSpace(coalesceString(args, "description"))
		if description == "" {
			return capabilityFailure("缺少必要参数 description"), nil
		}
		if deps.Emojis == nil {
			return capabilityFailure("表情包管理器未就绪"), nil
		}
		emoji, _, err := deps.Emojis.SelectForEmotion(description)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		if emoji == nil {
			return map[string]any{"success": true, "emoji": nil}, nil
		}
		return map[string]any{"success": true, "emoji": pluginSerializeEmoji(deps.Emojis, emoji, "")}, nil
	}
}

func makeEmojiGetRandomHandler(deps HostCapabilityDeps) CapabilityHandler {
	return func(ctx context.Context, _, _ string, args map[string]any) (any, error) {
		count := int(coalesceFloat(args, "count", 1))
		if count < 0 {
			return capabilityFailure("count 不能为负数"), nil
		}
		if count == 0 || deps.Emojis == nil || deps.Memory == nil {
			return map[string]any{"success": true, "emojis": []map[string]any{}}, nil
		}
		stickers, err := pluginStickerEmojis(ctx, deps.Memory)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		if len(stickers) == 0 {
			return map[string]any{"success": true, "emojis": []map[string]any{}}, nil
		}
		indices := rand.Perm(len(stickers))
		if count < len(indices) {
			indices = indices[:count]
		}
		out := make([]map[string]any, 0, len(indices))
		for _, index := range indices {
			emoji := pluginEmojiFromSticker(stickers[index])
			if serialized := pluginSerializeEmoji(deps.Emojis, &emoji, "随机表情"); serialized != nil {
				out = append(out, serialized)
			}
		}
		return map[string]any{"success": true, "emojis": out}, nil
	}
}

func makeEmojiGetCountHandler(deps HostCapabilityDeps) CapabilityHandler {
	return func(_ context.Context, _, _ string, _ map[string]any) (any, error) {
		if deps.Emojis == nil {
			return capabilityFailure("表情包管理器未就绪"), nil
		}
		return map[string]any{"success": true, "count": deps.Emojis.Count()}, nil
	}
}

func makeEmojiGetEmotionsHandler(deps HostCapabilityDeps) CapabilityHandler {
	return func(ctx context.Context, _, _ string, _ map[string]any) (any, error) {
		stickers, err := pluginStickerEmojis(ctx, deps.Memory)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		seen := map[string]struct{}{}
		for _, sticker := range stickers {
			for _, tag := range emojisystem.NormalizeEmotionTags(sticker.Description) {
				if strings.TrimSpace(tag) != "" {
					seen[strings.TrimSpace(tag)] = struct{}{}
				}
			}
		}
		emotions := make([]string, 0, len(seen))
		for tag := range seen {
			emotions = append(emotions, tag)
		}
		sort.Strings(emotions)
		return map[string]any{"success": true, "emotions": emotions}, nil
	}
}

func makeEmojiGetAllHandler(deps HostCapabilityDeps) CapabilityHandler {
	return func(ctx context.Context, _, _ string, _ map[string]any) (any, error) {
		if deps.Emojis == nil {
			return capabilityFailure("表情包管理器未就绪"), nil
		}
		stickers, err := pluginStickerEmojis(ctx, deps.Memory)
		if err != nil {
			return capabilityFailure("%s", err.Error()), nil
		}
		out := make([]map[string]any, 0, len(stickers))
		for _, sticker := range stickers {
			emoji := pluginEmojiFromSticker(sticker)
			if serialized := pluginSerializeEmoji(deps.Emojis, &emoji, "随机表情"); serialized != nil {
				out = append(out, serialized)
			}
		}
		return map[string]any{"success": true, "emojis": out}, nil
	}
}

func makeEmojiGetInfoHandler(deps HostCapabilityDeps) CapabilityHandler {
	return func(_ context.Context, _, _ string, _ map[string]any) (any, error) {
		if deps.Emojis == nil {
			return capabilityFailure("表情包管理器未就绪"), nil
		}
		count := deps.Emojis.Count()
		maxCount := 0
		if deps.EmojiAdmin != nil {
			maxCount = deps.EmojiAdmin.MaxCount(context.Background())
		}
		return map[string]any{"success": true, "info": map[string]any{
			"current_count":    count,
			"max_count":        maxCount,
			"available_emojis": count,
		}}, nil
	}
}

func makeEmojiRegisterHandler(deps HostCapabilityDeps) CapabilityHandler {
	return func(ctx context.Context, pluginID, _ string, args map[string]any) (any, error) {
		raw := strings.TrimSpace(coalesceString(args, "emoji_base64"))
		if raw == "" {
			return capabilityFailure("缺少必要参数 emoji_base64"), nil
		}
		png, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return map[string]any{"success": false, "message": "无法保存图片文件", "description": nil, "emotions": nil, "replaced": nil, "hash": nil}, nil
		}
		if deps.EmojiAdmin == nil {
			return pluginPortNotWired("emoji.register"), nil
		}
		return deps.EmojiAdmin.Register(ctx, pluginID, PluginEmojiRegisterRequest{
			ImagePNG:    png,
			Description: coalesceString(args, "description"),
			Emotions:    stringListValue(args["emotions"]),
			Replace:     truthyPayloadValue(args["replace"]),
			SourceName:  coalesceString(args, "file_name"),
		})
	}
}

func makeEmojiDeleteHandler(deps HostCapabilityDeps) CapabilityHandler {
	return func(ctx context.Context, pluginID, _ string, args map[string]any) (any, error) {
		hash := strings.TrimSpace(coalesceString(args, "emoji_hash"))
		if hash == "" {
			return capabilityFailure("缺少必要参数 emoji_hash"), nil
		}
		if deps.EmojiAdmin == nil {
			return pluginPortNotWired("emoji.delete"), nil
		}
		keepDescription := false
		if deps.Emojis != nil {
			if emoji, err := deps.Emojis.GetByHash(ctx, hash); err == nil && emoji != nil {
				keepDescription = strings.TrimSpace(emoji.Description) != ""
			}
		}
		if raw, exists := args["keep_desc"]; exists && raw != nil {
			keepDescription = truthyPayloadValue(raw)
		}
		return deps.EmojiAdmin.Delete(ctx, pluginID, hash, keepDescription)
	}
}

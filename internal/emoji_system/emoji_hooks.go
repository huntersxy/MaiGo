package emojisystem

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"strings"

	"MaiGo/internal/plugin_runtime/hooks"

	"go.uber.org/zap"
)

const (
	hookEmojiReceiveBeforeCache            = "emoji.receive.before_cache"
	hookEmojiMaintenanceBeforeRegister     = "emoji.maintenance.before_register"
	hookEmojiRegisterAfterBuildDescription = "emoji.register.after_build_description"
)

// emojiReceiveAllowed 对齐 emoji.receive.before_cache：插件可中止缓存与内置识别
func emojiReceiveAllowed(ctx context.Context, hash string, content []byte, sessionID string) bool {
	result := hooks.EmitGlobal(ctx, hookEmojiReceiveBeforeCache, map[string]any{
		"emoji_hash":         hash,
		"binary_data_base64": base64.StdEncoding.EncodeToString(content),
		"session_id":         sessionID,
	})
	logEmojiHookResult(hookEmojiReceiveBeforeCache, result)
	if result.Aborted {
		zap.L().Info("插件跳过聊天表情缓存", zap.String("emoji_hash", hash))
		return false
	}
	return true
}

// emojiMaintenanceAllowed 对齐 emoji.maintenance.before_register：插件可跳过当前候选
func emojiMaintenanceAllowed(ctx context.Context, fileName string, fileSize int64) bool {
	result := hooks.EmitGlobal(ctx, hookEmojiMaintenanceBeforeRegister, map[string]any{
		"file_name": fileName,
		"file_size": fileSize,
	})
	logEmojiHookResult(hookEmojiMaintenanceBeforeRegister, result)
	if result.Aborted {
		zap.L().Info("插件跳过自动注册表情", zap.String("file_name", fileName))
		return false
	}
	return true
}

// logEmojiHookResult 记录 Hook 执行期错误（对齐 MaiBot logger.warning）
func logEmojiHookResult(hookName string, result hooks.DispatchResult) {
	for _, msg := range result.Errors {
		zap.L().Warn("表情 Hook 执行失败", zap.String("hook", hookName), zap.String("error", msg))
	}
}

// applyRegisterDescriptionHook 对齐 emoji_manager._build_emoji_description 的 Hook 段：
// abort 时放弃注册；否则读回 description 覆盖值（可能为空，由调用方拒绝）。
func applyRegisterDescriptionHook(ctx context.Context, mgr *Manager, emoji *Emoji, description, imageFormat string) (string, error) {
	result := hooks.EmitGlobal(ctx, hookEmojiRegisterAfterBuildDescription, map[string]any{
		"emoji":        mgr.SerializeForHook(emoji),
		"description":  description,
		"image_format": strings.ToLower(stringsTrim(imageFormat)),
	})
	logEmojiHookResult(hookEmojiRegisterAfterBuildDescription, result)
	if result.Aborted {
		return "", errDescriptionHookAborted
	}
	return hookStringValue(result.Kwargs, "description", description), nil
}

// emojiImageFormat 对齐 ImageDataModel.image_format：小写文件扩展名
func emojiImageFormat(fileName string) string {
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(stringsTrim(fileName))), ".")
}

func hookStringValue(kwargs map[string]any, key, fallback string) string {
	if kwargs == nil {
		return fallback
	}
	if value, ok := kwargs[key].(string); ok {
		return value
	}
	return fallback
}

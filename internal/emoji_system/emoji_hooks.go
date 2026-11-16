package emojisystem

import (
	"context"
	"path/filepath"
	"strings"

	"MaiGo/internal/plugin_runtime/hooks"

	"go.uber.org/zap"
)

const hookEmojiRegisterAfterBuildDescription = "emoji.register.after_build_description"

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

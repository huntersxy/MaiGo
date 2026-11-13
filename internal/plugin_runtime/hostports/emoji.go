package hostports

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strings"

	"maibot-go/internal/common/database"
	"maibot-go/internal/emoji_system"
	"maibot-go/internal/plugin_runtime"
)

// EmojiAdmin 插件表情管理端口
type EmojiAdmin struct {
	webUI    func() *emojisystem.WebUIService
	memory   *memory.Manager
	maxCount func() int
}

// NewEmojiAdmin 构造表情管理端口
func NewEmojiAdmin(webUI func() *emojisystem.WebUIService, mem *memory.Manager, maxCount func() int) *EmojiAdmin {
	return &EmojiAdmin{webUI: webUI, memory: mem, maxCount: maxCount}
}

func (a *EmojiAdmin) service() (*emojisystem.WebUIService, error) {
	if a == nil || a.webUI == nil {
		return nil, fmt.Errorf("表情包 Dashboard 服务未就绪")
	}
	service := a.webUI()
	if service == nil {
		return nil, fmt.Errorf("表情包 Dashboard 服务未就绪")
	}
	return service, nil
}

// Register 注册插件上传的表情包
func (a *EmojiAdmin) Register(ctx context.Context, _ string, req pluginruntime.PluginEmojiRegisterRequest) (map[string]any, error) {
	service, err := a.service()
	if err != nil {
		return map[string]any{"success": false, "message": err.Error()}, nil
	}
	if len(req.ImagePNG) == 0 {
		return map[string]any{"success": false, "message": "表情包图片为空"}, nil
	}
	existing, _ := a.findByHash(ctx, req.ImagePNG)
	if existing != nil && !req.Replace {
		return map[string]any{
			"success":     true,
			"message":     "表情包已注册，已跳过本次注册",
			"description": nil,
			"emotions":    nil,
			"replaced":    false,
			"hash":        nil,
		}, nil
	}
	upload := emojisystem.WebUIEmojiUpload{
		Content:      req.ImagePNG,
		FileName:     strings.TrimSpace(req.SourceName),
		Description:  strings.TrimSpace(req.Description),
		Emotion:      strings.Join(req.Emotions, ","),
		IsRegistered: true,
	}
	item, message, existed, err := service.Upload(ctx, upload)
	if err != nil {
		return map[string]any{
			"success": false, "message": "表情包注册失败，可能因为重复、格式不支持或审核未通过",
			"description": nil, "emotions": nil, "replaced": nil, "hash": nil,
		}, nil
	}
	replaced := existed || req.Replace
	return map[string]any{
		"success":     true,
		"message":     message,
		"description": item.Description,
		"emotions":    emojisystem.NormalizeEmotionTags(item.Description),
		"replaced":    replaced,
		"hash":        item.Hash,
	}, nil
}

// Delete 删除表情包
func (a *EmojiAdmin) Delete(ctx context.Context, _ string, emojiHash string, keepDescription bool) (map[string]any, error) {
	service, err := a.service()
	if err != nil {
		return map[string]any{"success": false, "message": err.Error()}, nil
	}
	if a.memory == nil {
		return map[string]any{"success": false, "message": "记忆管理器未就绪", "hash": emojiHash}, nil
	}
	sticker, err := a.memory.GetEmojiRecordByHash(ctx, strings.TrimSpace(emojiHash))
	if err != nil || sticker == nil {
		return map[string]any{"success": false, "message": "未找到表情包: " + emojiHash, "hash": emojiHash}, nil
	}
	if _, err := service.Delete(ctx, sticker.ID); err != nil {
		return map[string]any{"success": false, "message": "删除表情包失败: " + err.Error(), "hash": emojiHash}, nil
	}
	return map[string]any{
		"success":   true,
		"message":   "成功删除表情包: " + emojiHash,
		"hash":      emojiHash,
		"keep_desc": keepDescription,
	}, nil
}

// findByHash 按图片内容的 md5 前缀定位已有表情包记录
func (a *EmojiAdmin) findByHash(ctx context.Context, content []byte) (*memory.EmojiRecord, error) {
	if a.memory == nil || len(content) == 0 {
		return nil, nil
	}
	sum := md5.Sum(content)
	return a.memory.GetEmojiRecordByHash(ctx, hex.EncodeToString(sum[:]))
}

// MaxCount 返回表情包上限
func (a *EmojiAdmin) MaxCount(context.Context) int {
	if a == nil || a.maxCount == nil {
		return 0
	}
	return a.maxCount()
}

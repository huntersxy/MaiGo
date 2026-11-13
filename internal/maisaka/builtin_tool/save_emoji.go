package tools

import (
	"context"
	"fmt"
	"os"
	"strings"

	"maibot-go/internal/common/database"
	"maibot-go/internal/common/media"
	projectutils "maibot-go/internal/common/utils"
	"maibot-go/internal/emoji_system"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

// SaveEmojiInput 从近期消息图片/表情或 URL 偷存表情包
type SaveEmojiInput struct {
	Hash         string `json:"hash,omitempty" jsonschema:"description=消息里图片或表情包的短哈希标识"`
	URL          string `json:"url,omitempty" jsonschema:"description=图片 HTTP/HTTPS 地址；与 hash 二选一"`
	File         string `json:"file,omitempty" jsonschema:"description=OneBot 图片 file 字段，与 url 联用可复用媒体缓存"`
	Description  string `json:"description,omitempty" jsonschema:"description=可选中文备注，写入 images.description"`
	AutoDescribe *bool  `json:"auto_describe,omitempty" jsonschema:"description=保存后 description 为空时是否尝试 VLM 打标，默认 true"`
}

// SaveEmojiOutput 偷存结果
type SaveEmojiOutput struct {
	Success     bool     `json:"success"`
	Message     string   `json:"message,omitempty"`
	StickerID   uint     `json:"sticker_id,omitempty"`
	FileHash    string   `json:"file_hash,omitempty"`
	FileName    string   `json:"file_name,omitempty"`
	Description string   `json:"description,omitempty"`
	Emotions    []string `json:"emotions,omitempty"`
	Duplicate   bool     `json:"duplicate,omitempty"`
}

// NewSaveEmojiTool Maisaka 偷表情：消息图片/表情或 URL → MAIBOT_STICKER_DIR + images 表（EMOJI）
func NewSaveEmojiTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"save_emoji",
		"把近期消息里的图片或表情包收藏进表情包库（偷表情）。提供消息短哈希 hash，或图片 url（可选 file 复用缓存）；可选备注与 VLM 自动打标。",
		saveEmojiFunc,
	)
}

func saveEmojiFunc(ctx context.Context, input *SaveEmojiInput) (*SaveEmojiOutput, error) {
	tc := GetToolContext(ctx)
	if tc == nil || tc.MemoryMgr == nil {
		return nil, NewTerminalToolError(fmt.Errorf("表情包收藏能力未初始化"))
	}
	ref, err := resolveMediaForSaveEmoji(ctx, tc, input)
	if err != nil {
		return nil, err
	}
	if ref.Hash == "" {
		return nil, fmt.Errorf("媒体缺少内容哈希，无法入库")
	}

	desc := ""
	if input != nil {
		desc = strings.TrimSpace(input.Description)
	}
	content, err := os.ReadFile(ref.Path)
	if err != nil {
		return nil, fmt.Errorf("读取待收藏表情失败: %w", err)
	}
	item, _, err := emojisystem.EnsureEmojiSaved(ctx, content, "")
	if err != nil {
		return nil, err
	}
	status, err := emojisystem.DefaultRegistry().RegisterEmoji(ctx, item, desc)
	if err != nil {
		return nil, err
	}
	duplicate := status != memory.EmojiRegisterRegistered
	if desc != "" {
		if _, err := emojisystem.DefaultRegistry().SetEmojiDescription(ctx, item.FileHash, desc); err != nil {
			return nil, fmt.Errorf("写入表情包描述失败: %w", err)
		}
	}
	savedHash := item.FileHash
	_ = emojisystem.DefaultRegistry().Reload(ctx)

	autoDescribe := true
	if input != nil && input.AutoDescribe != nil {
		autoDescribe = *input.AutoDescribe
	}
	if autoDescribe && desc == "" {
		emojisystem.TryDescribeIfEmpty(ctx, savedHash)
	}

	row, getErr := tc.MemoryMgr.GetEmojiRecordByHash(ctx, savedHash)
	if getErr != nil || row == nil {
		return &SaveEmojiOutput{
			Success: true, Message: saveEmojiMessage(duplicate), FileHash: savedHash, FileName: item.FileName, Duplicate: duplicate,
		}, nil
	}
	out := &SaveEmojiOutput{
		Success: true, Message: saveEmojiMessage(duplicate), StickerID: row.ID,
		FileHash: row.ImageHash, FileName: item.FileName, Description: row.Description, Duplicate: duplicate,
	}
	if row.Description != "" {
		out.Emotions = emojisystem.NormalizeEmotionTags(row.Description)
	}
	return out, nil
}

func saveEmojiMessage(duplicate bool) string {
	if duplicate {
		return "表情已在库中，未重复写入"
	}
	return "已偷存表情到表情包库"
}

func resolveMediaForSaveEmoji(ctx context.Context, tc *ToolContext, input *SaveEmojiInput) (media.Ref, error) {
	hash := ""
	url := ""
	file := ""
	if input != nil {
		hash = strings.TrimSpace(input.Hash)
		url = strings.TrimSpace(input.URL)
		file = strings.TrimSpace(input.File)
	}
	if hash == "" && url == "" {
		return media.Ref{}, fmt.Errorf("请提供 hash 或 url")
	}
	if hash != "" {
		if tc.Session == nil {
			return media.Ref{}, fmt.Errorf("会话媒体索引未初始化")
		}
		ref, ok := tc.Session.ResolveMediaByHash(hash)
		if !ok {
			return media.Ref{}, fmt.Errorf("没有找到标识为 %s 的图片，可能已被清理或不在当前会话", hash)
		}
		return ref, nil
	}
	if tc.Media == nil {
		return media.Ref{}, NewTerminalToolError(fmt.Errorf("媒体下载能力未初始化"))
	}
	if _, err := projectutils.ParseHTTPURL(url); err != nil {
		return media.Ref{}, fmt.Errorf("图片 URL 无效，仅支持不带凭据的 HTTP/HTTPS 地址: %w", err)
	}
	ref, err := tc.Media.Ensure(ctx, media.Source{URL: url, File: file, Sticker: true})
	if err != nil {
		return media.Ref{}, fmt.Errorf("下载并缓存图片失败: %w", err)
	}
	return ref, nil
}

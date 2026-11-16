package emojisystem

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"MaiGo/internal/llm_models"
	"MaiGo/internal/prompt"

	"go.uber.org/zap"
)

// DescribeResult VLM 描述结果
type DescribeResult struct {
	Hash        string   `json:"file_hash"`
	Description string   `json:"description"`
	Emotions    []string `json:"emotions,omitempty"`
	Stored      bool     `json:"stored"`
}

// Describe 读取本地表情文件并调用 TaskVLM 生成中文标签描述，写入 images.description
func Describe(ctx context.Context, hash string) (DescribeResult, error) {
	return DefaultRegistry().Describe(ctx, hash)
}

// Describe 在指定 Registry 上生成并写入表情包描述
func (r *Registry) Describe(ctx context.Context, hash string) (DescribeResult, error) {
	mgr, stickerDir, prompts, router, err := r.describeDeps()
	if err != nil {
		return DescribeResult{}, err
	}
	hash = stringsTrim(hash)
	if hash == "" {
		return DescribeResult{}, errInvalidHash
	}
	st, err := mgr.GetByHash(ctx, hash)
	if err != nil {
		return DescribeResult{}, fmt.Errorf("emojisystem: 查无表情 %s: %w", hash, err)
	}
	path := stickerFilePath(stickerDir, st.FileName)
	if path == "" {
		return DescribeResult{}, errStickerFileMissing
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DescribeResult{}, errStickerFileMissing
		}
		return DescribeResult{}, fmt.Errorf("emojisystem: 读取表情文件失败: %w", err)
	}
	b64, mime, imageType, err := prepareVLMPayload(raw, st.FileName)
	if err != nil {
		return DescribeResult{}, err
	}
	desc, err := generateEmojiDescription(ctx, router, prompts, b64, mime, imageType)
	if err != nil {
		return DescribeResult{}, err
	}
	normalizedDescription := stringsTrim(desc)
	if normalizedDescription == "" {
		return DescribeResult{}, errEmptyVLMDescription
	}
	normalizedDescription, err = applyRegisterDescriptionHook(ctx, mgr, st, normalizedDescription, emojiImageFormat(st.FileName))
	if err != nil {
		if errors.Is(err, errDescriptionHookAborted) {
			zap.L().Info("[构建描述] 表情包描述被 Hook 中止注册", zap.String("hash", hash))
		}
		return DescribeResult{}, err
	}
	normalizedDescription = stringsTrim(normalizedDescription)
	if normalizedDescription == "" {
		zap.L().Warn("[构建描述] Hook 返回空描述，拒绝注册", zap.String("hash", hash))
		return DescribeResult{}, errHookEmptyDescription
	}
	tags := NormalizeEmotionTags(normalizedDescription)
	if len(tags) == 0 {
		return DescribeResult{}, errEmptyVLMDescription
	}
	normalized := strings.Join(tags, ",")
	result := DescribeResult{Hash: hash, Description: normalized, Emotions: tags}
	if mgr.mem == nil {
		return result, nil
	}
	if err := mgr.mem.UpdateEmojiDescription(ctx, st.ID, normalized); err != nil {
		zap.L().Warn("表情包 VLM 描述写库失败", zap.String("hash", hash), zap.Error(err))
		return result, nil
	}
	result.Stored = true
	if reloadErr := mgr.ReloadFromDB(ctx); reloadErr != nil {
		zap.L().Warn("表情包 VLM 描述后刷新内存池失败", zap.Error(reloadErr))
	}
	return result, nil
}

// AnalyzeEmojiImage 用 VLM 分析表情图片，返回指定 prompt 的原始文本
// 描述与分组标签共用静态图/GIF 抽帧流程
func (r *Registry) AnalyzeEmojiImage(ctx context.Context, emoji *Emoji, promptName string) (string, error) {
	if r == nil || emoji == nil {
		return "", errStickerFileMissing
	}
	mgr, _, prompts, router, err := r.describeDeps()
	if err != nil {
		return "", err
	}
	path := mgr.EmojiFilePath(emoji)
	if path == "" {
		return "", errStickerFileMissing
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", errStickerFileMissing
		}
		return "", fmt.Errorf("emojisystem: 读取表情文件失败: %w", err)
	}
	b64, mime, imageType, err := prepareVLMPayload(raw, path)
	if err != nil {
		return "", err
	}
	return generateEmojiVLM(ctx, router, prompts, promptName, map[string]string{"image_type": imageType}, b64, mime)
}

// TryDescribeIfEmpty 描述为空且本地有文件时 best-effort 调用 Describe，失败不阻断主流程
func TryDescribeIfEmpty(ctx context.Context, hash string) {
	hash = stringsTrim(hash)
	if hash == "" {
		return
	}
	reg := DefaultRegistry()
	if !reg.canDescribe() {
		return
	}
	mgr := reg.Manager()
	if mgr == nil {
		return
	}
	item, err := mgr.GetByHash(ctx, hash)
	if err != nil || item == nil || strings.TrimSpace(item.Description) != "" {
		return
	}
	if _, err := Describe(ctx, hash); err != nil && !errors.Is(err, errVLMNotConfigured) {
		zap.L().Debug("表情包自动 VLM 描述跳过", zap.String("hash", hash), zap.Error(err))
	}
}

func (r *Registry) canDescribe() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.stickerDir != "" && r.prompts != nil && r.router != nil
}

func (r *Registry) describeDeps() (*Manager, string, *prompt.Manager, *llm.Router, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.manager == nil {
		return nil, "", nil, nil, errMemoryUnavailable
	}
	if strings.TrimSpace(r.stickerDir) == "" || r.prompts == nil || r.router == nil {
		return nil, "", nil, nil, errDescribeDeps
	}
	return r.manager, r.stickerDir, r.prompts, r.router, nil
}

package emojisystem

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"
)

// EmojiDescription 表情包描述与情绪标签
type EmojiDescription struct {
	Description string
	Emotions    []string
}

// GetEmojiDescription 对齐 get_emoji_description：内存/DB 命中直接返回，未命中按需缓存与构建
//
// wait=false 时只确保文件入库并调度后台描述任务，对齐 MaiBot 的 wait_for_build=False
func (r *Registry) GetEmojiDescription(ctx context.Context, content []byte, hash string, wait bool) (EmojiDescription, bool, error) {
	return r.GetEmojiDescriptionForSession(ctx, content, hash, wait, "")
}

// GetEmojiDescriptionForSession 携带来源会话，供 emoji.receive.before_cache Hook 使用
func (r *Registry) GetEmojiDescriptionForSession(ctx context.Context, content []byte, hash string, wait bool, sessionID string) (EmojiDescription, bool, error) {
	if r == nil {
		return EmojiDescription{}, false, errMemoryUnavailable
	}
	hash = stringsTrim(hash)
	if hash == "" {
		if len(content) == 0 {
			return EmojiDescription{}, false, fmt.Errorf("获取表情包描述失败: 既没有提供表情包字节数据，也没有提供表情包哈希值")
		}
		hash = sha256Bytes(content)
	}
	mgr := r.Manager()
	if mgr == nil {
		return EmojiDescription{}, false, errMemoryUnavailable
	}
	item, err := mgr.GetByHash(ctx, hash)
	found := err == nil
	if err != nil && !errors.Is(err, errEmojiNotFound) {
		return EmojiDescription{}, false, err
	}
	if found {
		missingFile := !fileExists(stickerFilePath(mgr.stickerDirectory(), item.FileName))
		if len(content) > 0 && missingFile {
			if _, _, ensureErr := r.EnsureEmojiSaved(ctx, content, hash); ensureErr != nil {
				zap.L().Warn("表情包缓存命中但本地文件缺失，回填失败", zap.String("hash", hash), zap.Error(ensureErr))
			}
		}
		desc := item.Description
		if !wait && len(content) > 0 && stringsTrim(desc) == "" {
			r.scheduleDescriptionBuild(hash)
		}
		return EmojiDescription{Description: desc, Emotions: NormalizeEmotionTags(desc)}, true, nil
	}
	if len(content) == 0 {
		return EmojiDescription{}, false, nil
	}
	if !emojiReceiveAllowed(ctx, hash, content, sessionID) {
		return EmojiDescription{}, false, nil
	}
	if !r.canDescribe() {
		if _, _, ensureErr := r.EnsureEmojiSaved(ctx, content, hash); ensureErr != nil {
			return EmojiDescription{}, false, ensureErr
		}
		zap.L().Info("未配置 VLM 模型，跳过表情包识别、打标签和审核")
		return EmojiDescription{}, false, nil
	}
	if !wait {
		if _, _, ensureErr := r.EnsureEmojiSaved(ctx, content, hash); ensureErr != nil {
			return EmojiDescription{}, false, ensureErr
		}
		r.scheduleDescriptionBuild(hash)
		return EmojiDescription{}, false, nil
	}
	if _, _, ensureErr := r.EnsureEmojiSaved(ctx, content, hash); ensureErr != nil {
		return EmojiDescription{}, false, ensureErr
	}
	result, err := r.Describe(ctx, hash)
	if err != nil {
		return EmojiDescription{}, false, err
	}
	return EmojiDescription{Description: result.Description, Emotions: result.Emotions}, true, nil
}

// IsDescriptionPending 判断表情包是否正在生成描述。
func (r *Registry) IsDescriptionPending(hash string) bool {
	_, pending := r.descTasks.Load(hash)
	return pending
}

// scheduleDescriptionBuild 后台构建描述，同一哈希只保留一个任务
func (r *Registry) scheduleDescriptionBuild(hash string) {
	if r == nil || !r.canDescribe() {
		return
	}
	if _, loaded := r.descTasks.LoadOrStore(hash, struct{}{}); loaded {
		return
	}
	go func() {
		defer r.descTasks.Delete(hash)
		if _, err := r.Describe(context.Background(), hash); err != nil {
			logDescriptionBuildError(hash, err)
		}
	}()
}

func logDescriptionBuildError(hash string, err error) {
	zap.L().Debug("表情包后台描述任务失败", zap.String("hash", hash), zap.Error(err))
}

// RecordUsageByHash 对齐 update_emoji_usage_by_hash
func (r *Registry) RecordUsageByHash(ctx context.Context, hash string) (bool, error) {
	if r == nil {
		return false, errMemoryUnavailable
	}
	mgr := r.Manager()
	if mgr == nil {
		return false, errMemoryUnavailable
	}
	return mgr.RecordUsageByHash(ctx, hash)
}

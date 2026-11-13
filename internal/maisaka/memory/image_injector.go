package memory

import (
	"context"
	"fmt"
	"strings"
)

// ImageMemoryReferenceMarker 图片记忆参考标记
const ImageMemoryReferenceMarker = "【图片记忆-内部参考】"

// ImageMemoryHit 图片记忆命中
type ImageMemoryHit struct {
	Similarity      float64
	MatchKind       string
	Observations    []string
	RelatedMemories []ImageRelatedMemory
}

// ImageRelatedMemory 关联记忆条目，去重键依赖 target_type/target_id
type ImageRelatedMemory struct {
	Content    string
	TargetType string
	TargetID   string
}

// ImageMemorySearchResult 图片记忆检索结果
type ImageMemorySearchResult struct {
	Success bool
	Error   string
	Hits    []ImageMemoryHit
}

// ImageMemoryService 图片记忆检索端口
type ImageMemoryService interface {
	SearchImageMemory(ctx context.Context, contentHash, externalRef, sessionID string, candidateLimit int, similarityThreshold float64) (ImageMemorySearchResult, error)
}

// ImageMemoryInjector 对当前消息中的图片执行同图与相似图召回
type ImageMemoryInjector struct {
	Service             ImageMemoryService
	Enabled             bool
	CandidateLimit      int
	SimilarityThreshold float64
}

// NewImageMemoryInjector 创建图片记忆注入器
func NewImageMemoryInjector(service ImageMemoryService, enabled bool) *ImageMemoryInjector {
	return &ImageMemoryInjector{Service: service, Enabled: enabled}
}

// ImageQuerySource 一张待检索图片
type ImageQuerySource struct {
	BinaryHash string
	MediaRef   string
}

// BuildInjectionMessage 构造图片记忆内部参考
func (i *ImageMemoryInjector) BuildInjectionMessage(ctx context.Context, sessionID string, sourceMessages [][]ContextImageSource, messageIDs []string) (string, error) {
	if i == nil || !i.Enabled || i.Service == nil {
		return "", nil
	}
	queries := make([]struct{ hash, ref string }, 0, 3)
	for index := len(sourceMessages) - 1; index >= 0; index-- {
		messageID := ""
		if index < len(messageIDs) {
			messageID = messageIDs[index]
		}
		for _, component := range sourceMessages[index] {
			contentHash := strings.TrimSpace(component.BinaryHash)
			if contentHash == "" {
				continue
			}
			ref := buildChatExternalRef(sessionID, messageID, component.ComponentPath)
			queries = append(queries, struct{ hash, ref string }{contentHash, ref})
		}
		if len(queries) > 0 {
			break
		}
	}
	if len(queries) == 0 {
		return "", nil
	}
	if len(queries) > 3 {
		queries = queries[:3]
	}
	lines := []string{ImageMemoryReferenceMarker, "以下内容来自当前图片的历史同图或相似图记录，仅作为回答依据。"}
	seen := map[string]struct{}{}
	for _, query := range queries {
		result, err := i.Service.SearchImageMemory(ctx, query.hash, query.ref, sessionID, i.CandidateLimit, i.SimilarityThreshold)
		if err != nil {
			return "", err
		}
		if !result.Success {
			message := result.Error
			if message == "" {
				message = "图片记忆检索失败"
			}
			return "", fmt.Errorf("%s", message)
		}
		for _, hit := range result.Hits {
			matchKind := "相似图片"
			if hit.MatchKind == "exact_hash" {
				matchKind = "同一图片"
			}
			for _, observation := range hit.Observations {
				text := strings.Join(strings.Fields(observation), " ")
				key := "observation:" + text
				if text == "" {
					continue
				}
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				lines = append(lines, fmt.Sprintf("- %s %.3f：%s", matchKind, hit.Similarity, text))
			}
			for _, memory := range hit.RelatedMemories {
				text := strings.Join(strings.Fields(memory.Content), " ")
				// 对齐 Python：去重键用 target_type/target_id，同文不同源分别保留
				key := "memory:" + memory.TargetType + ":" + memory.TargetID
				if text == "" {
					continue
				}
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				lines = append(lines, "- 关联记忆："+text)
			}
		}
	}
	if len(lines) <= 2 {
		return "", nil
	}
	return strings.Join(lines, "\n"), nil
}

// ContextImageSource 图片组件来源
type ContextImageSource struct {
	BinaryHash    string
	ComponentPath string
}

func buildChatExternalRef(chatID, messageID, componentPath string) string {
	return fmt.Sprintf("%s:%s:%s", chatID, messageID, componentPath)
}

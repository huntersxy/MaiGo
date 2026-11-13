package services

// 聊天流摘要服务，对齐 Python core/runtime/services/summary_service.py

import (
	"context"
	"fmt"
	"strings"
)

// SummarizeChatStreamParams 对齐 summarize_chat_stream 的关键字参数
// 与内核 kernel_services_runtime.go 中的同名结构字段保持一致
type SummarizeChatStreamParams struct {
	// ChatID 聊天流 ID
	ChatID string
	// ContextLength 摘要上下文长度
	ContextLength *int
	// IncludePersonality 是否包含人格设定
	IncludePersonality *bool
	// TimeEnd 摘要截止时间
	TimeEnd *float64
	// Metadata 附加元数据
	Metadata map[string]any
}

// SummaryImportParams 对齐 summary_importer.import_from_stream 的关键字参数
type SummaryImportParams struct {
	// StreamID 聊天流 ID
	StreamID string
	// ContextLength 摘要上下文长度
	ContextLength *int
	// IncludePersonality 是否包含人格设定
	IncludePersonality *bool
	// TimeEnd 摘要截止时间
	TimeEnd *float64
	// Metadata 附加元数据
	Metadata map[string]any
}

// SummaryImportResult 对齐 summary_importer.import_from_stream 的返回对象
type SummaryImportResult struct {
	// Success 是否导入成功
	Success bool
	// Detail 结果说明
	Detail string
	// ParagraphHash 写入的段落哈希
	ParagraphHash string
	// Skipped 是否按空内容跳过
	Skipped bool
	// Source episode 来源
	Source string
}

// SummaryDeps 摘要服务依赖的导入与持久化能力
type SummaryDeps struct {
	// Initialize 确保运行时已初始化
	Initialize func(ctx context.Context) error
	// ImportFromStream 从聊天流导入摘要
	ImportFromStream func(ctx context.Context, params SummaryImportParams) (*SummaryImportResult, error)
	// Persist 持久化写入结果，对齐 self._persist()
	Persist func()
	// BuildSource 构造 episode 来源，对齐 build_source
	BuildSource func(sourceType string, chatID string, personIDs []string) string
}

// SummaryService 对齐 MemorySummaryService
type SummaryService struct {
	Deps SummaryDeps
}

// NewSummaryService 构造摘要服务
func NewSummaryService(deps SummaryDeps) *SummaryService { return &SummaryService{Deps: deps} }

// SummarizeChatStream 对齐 summarize_chat_stream
func (s *SummaryService) SummarizeChatStream(ctx context.Context, params SummarizeChatStreamParams) (map[string]any, error) {
	if s.Deps.Initialize != nil {
		if err := s.Deps.Initialize(ctx); err != nil {
			return nil, err
		}
	}
	if s.Deps.ImportFromStream == nil {
		return nil, fmt.Errorf("聊天摘要导入器未接入")
	}
	result, err := s.Deps.ImportFromStream(ctx, SummaryImportParams{
		StreamID:           strings.TrimSpace(params.ChatID),
		ContextLength:      params.ContextLength,
		IncludePersonality: params.IncludePersonality,
		TimeEnd:            params.TimeEnd,
		Metadata:           params.Metadata,
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		result = &SummaryImportResult{}
	}
	success := result.Success
	detail := result.Detail
	paragraphHash := strings.TrimSpace(result.ParagraphHash)
	source := strings.TrimSpace(result.Source)
	if source == "" {
		source = s.buildSource("chat_summary", params.ChatID)
	}
	storedIDs := make([]string, 0, 1)
	skippedIDs := make([]string, 0, 1)
	if success {
		if paragraphHash != "" {
			storedIDs = append(storedIDs, paragraphHash)
			if s.Deps.Persist != nil {
				s.Deps.Persist()
			}
		} else if result.Skipped {
			externalID := ""
			if params.Metadata != nil {
				externalID = strings.TrimSpace(asString(params.Metadata["external_id"]))
			}
			if externalID != "" {
				skippedIDs = append(skippedIDs, externalID)
			}
		} else {
			return nil, fmt.Errorf("聊天摘要导入成功但未返回 paragraph_hash")
		}
	}
	payload := map[string]any{"success": success, "detail": detail, "episode_source": source}
	if len(storedIDs) > 0 {
		payload["stored_ids"] = storedIDs
	}
	if len(skippedIDs) > 0 {
		payload["skipped_ids"] = skippedIDs
	}
	return payload, nil
}

// buildSource 调用注入的来源构造器，未注入时按固定规则拼接
func (s *SummaryService) buildSource(sourceType string, chatID string) string {
	if s.Deps.BuildSource != nil {
		return s.Deps.BuildSource(sourceType, chatID, nil)
	}
	if chatID == "" {
		return sourceType
	}
	return sourceType + ":" + chatID
}

package utils

// Web 导入运行时自检适配，对齐 _ensure_embedding_runtime_ready

import (
	"context"
	"fmt"
	"strings"
)

// webImportVectorSnapshot 把 WebImportVectorStore 适配为自检快照
type webImportVectorSnapshot struct {
	store WebImportVectorStore
}

// Dimension 返回向量维度
func (s webImportVectorSnapshot) Dimension() int {
	if s.store == nil {
		return 0
	}
	return s.store.Dimension()
}

// NumVectors 返回向量数量，未提供时按 0 处理
func (s webImportVectorSnapshot) NumVectors() int {
	if provider, ok := s.store.(interface{ NumVectors() int }); ok {
		return provider.NumVectors()
	}
	return 0
}

// HasData 返回是否有持久化数据
func (s webImportVectorSnapshot) HasData() bool {
	return s.store != nil && s.store.HasData()
}

// ensureEmbeddingRuntimeReady 对齐 _ensure_embedding_runtime_ready
func (m *WebImportManager) ensureEmbeddingRuntimeReady(ctx context.Context) error {
	var report RuntimeSelfCheckReport
	if m.deps.SelfCheck != nil {
		report = m.deps.SelfCheck(ctx)
	} else {
		params := SelfCheckParams{
			Config:               m.deps.Config,
			VectorStore:          webImportVectorSnapshot{store: m.paragraphVectorStore()},
			ParagraphVectorStore: webImportVectorSnapshot{store: m.paragraphVectorStore()},
			GraphVectorStore:     webImportVectorSnapshot{store: m.graphVectorStore()},
			Logger:               m.deps.Logger,
		}
		if m.deps.Embedding != nil {
			params.EmbeddingManager = m.deps.Embedding
		}
		report = EnsureRuntimeSelfCheck(ctx, &params, &m.selfCheckCache, false)
	}
	if report.OK {
		return nil
	}
	message := strings.TrimSpace(report.Message)
	if message == "" {
		message = "unknown"
	}
	if m.allowMetadataOnlyWrite() {
		m.deps.Logger.Warning("web_import embedding runtime self-check 失败，进入 metadata-only 回退模式: " + message)
		return nil
	}
	return fmt.Errorf("embedding runtime self-check failed: %s (configured=%d, store=%d, encoded=%d)",
		message, report.ConfiguredDimension, report.VectorStoreDimension, report.EncodedDimension)
}

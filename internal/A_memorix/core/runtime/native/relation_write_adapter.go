package native

// 关系写入适配器：把 core/write 的写入实现接到内核关系写入契约上

import (
	"context"
	"fmt"

	"MaiGo/internal/A_memorix/core/embedding"
	memruntime "MaiGo/internal/A_memorix/core/runtime"
	"MaiGo/internal/A_memorix/core/write"
)

// relationWriteAdapter 包装 write.RelationWriteService 并转换结果类型
type relationWriteAdapter struct {
	inner *write.RelationWriteService
}

// UpsertRelationsWithVectors 批量写入关系并转换为内核结果类型
func (a *relationWriteAdapter) UpsertRelationsWithVectors(ctx context.Context, relations [][3]string, confidence float64, sourceParagraph string, metadata map[string]any, writeVector bool) ([]memruntime.RelationWriteResult, error) {
	results, err := a.inner.UpsertRelationsWithVectors(ctx, relations, confidence, sourceParagraph, metadata, writeVector)
	if err != nil {
		return nil, err
	}
	out := make([]memruntime.RelationWriteResult, 0, len(results))
	for _, result := range results {
		out = append(out, toRuntimeRelationResult(result))
	}
	return out, nil
}

// UpsertRelationWithVector 写入单条关系并转换为内核结果类型
func (a *relationWriteAdapter) UpsertRelationWithVector(ctx context.Context, subject, predicate, object string, confidence float64, sourceParagraph string, metadata map[string]any, writeVector bool) (memruntime.RelationWriteResult, error) {
	result, err := a.inner.UpsertRelationWithVector(ctx, subject, predicate, object, confidence, sourceParagraph, metadata, writeVector)
	if err != nil {
		return memruntime.RelationWriteResult{}, err
	}
	return toRuntimeRelationResult(result), nil
}

// EnsureRelationVector 补齐单条关系向量并转换为内核结果类型
func (a *relationWriteAdapter) EnsureRelationVector(ctx context.Context, hashValue, subject, predicate, object string, typedID bool) (memruntime.RelationWriteResult, error) {
	result, err := a.inner.EnsureRelationVector(ctx, hashValue, subject, predicate, object, memruntime.RelationWriteServiceErrorMaxLen, typedID, nil)
	if err != nil {
		return memruntime.RelationWriteResult{}, err
	}
	return toRuntimeRelationResult(result), nil
}

// toRuntimeRelationResult 转换结果结构，字段语义与 Python RelationWriteResult 一致
func toRuntimeRelationResult(result write.RelationWriteResult) memruntime.RelationWriteResult {
	return memruntime.RelationWriteResult{
		HashValue:           result.HashValue,
		VectorWritten:       result.VectorWritten,
		VectorAlreadyExists: result.VectorAlreadyExists,
		VectorState:         result.VectorState,
	}
}

// embeddingTextAdapter 把嵌入适配器接到 write.TextEmbedder 的五参数接口
type embeddingTextAdapter struct {
	inner *embedding.EmbeddingAPIAdapter
}

// Encode 批量编码，batchSize/showProgress/normalize 由适配器默认配置接管
func (a *embeddingTextAdapter) Encode(ctx context.Context, texts []string, batchSize int, showProgress, normalize bool) ([][]float32, error) {
	if a == nil || a.inner == nil {
		return nil, fmt.Errorf("嵌入管理器未初始化")
	}
	return a.inner.Encode(ctx, texts, nil)
}

// EncodeOne 单条编码
func (a *embeddingTextAdapter) EncodeOne(ctx context.Context, text string, batchSize int, showProgress, normalize bool) ([]float32, error) {
	if a == nil || a.inner == nil {
		return nil, fmt.Errorf("嵌入管理器未初始化")
	}
	return a.inner.EncodeOne(ctx, text, nil)
}

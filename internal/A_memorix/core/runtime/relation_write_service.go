package memruntime

// 关系写入服务契约与纯函数，对齐 utils/relation_write_service.py
// 说明：具体写入实现由内核注入，本包只保留内核需要的调用面与文本构造语义

import (
	"context"
	"strings"
)

// RelationWriteResult 对齐 RelationWriteResult
type RelationWriteResult struct {
	HashValue           string
	VectorWritten       bool
	VectorAlreadyExists bool
	VectorState         string
}

// RelationWriteService 对齐 RelationWriteService 的公开写入面
type RelationWriteService interface {
	UpsertRelationsWithVectors(ctx context.Context, relations [][3]string, confidence float64, sourceParagraph string, metadata map[string]any, writeVector bool) ([]RelationWriteResult, error)
	UpsertRelationWithVector(ctx context.Context, subject string, predicate string, object string, confidence float64, sourceParagraph string, metadata map[string]any, writeVector bool) (RelationWriteResult, error)
	EnsureRelationVector(ctx context.Context, hashValue string, subject string, predicate string, object string, typedID bool) (RelationWriteResult, error)
}

// BuildRelationVectorText 双表达关系向量文本，对齐 build_relation_vector_text
func BuildRelationVectorText(subject string, predicate string, object string) string {
	s := strings.TrimSpace(subject)
	p := strings.TrimSpace(predicate)
	o := strings.TrimSpace(object)
	return s + " " + p + " " + o + "\n" + s + "和" + o + "的关系是" + p
}

// RelationVectorID 对齐 relation_vector_id
func RelationVectorID(hashValue string) string {
	return "relation:" + strings.TrimSpace(hashValue)
}

// RelationWriteServiceErrorMaxLen 对齐 ERROR_MAX_LEN
const RelationWriteServiceErrorMaxLen = 500

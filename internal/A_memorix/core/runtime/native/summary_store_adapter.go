package native

// 聊天摘要导入的存储适配，把 storage.Store 对齐 utils.SummaryImportStore 与 FactDedupStore

import (
	"strings"

	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

// summaryStoreAdapter 包装元数据存储，补齐摘要导入所需的窄接口
type summaryStoreAdapter struct {
	store *storage.Store
}

var _ utils.SummaryImportStore = summaryStoreAdapter{}

// GetDBPath 对齐 Python get_db_path
func (a summaryStoreAdapter) GetDBPath() (string, error) {
	return a.store.DBPath()
}

// Query 透传只读查询
func (a summaryStoreAdapter) Query(sql string, params ...any) ([]map[string]any, error) {
	return a.store.Query(sql, params...)
}

// GetParagraph 读取段落
func (a summaryStoreAdapter) GetParagraph(hashValue string) (map[string]any, error) {
	return a.store.GetParagraph(hashValue)
}

// GetFactClaim 读取事实 claim
func (a summaryStoreAdapter) GetFactClaim(claimID string) (map[string]any, error) {
	return a.store.GetFactClaim(claimID)
}

// GetFactEvidence 读取事实证据
func (a summaryStoreAdapter) GetFactEvidence(claimID string) ([]map[string]any, error) {
	return a.store.GetFactEvidence(claimID)
}

// UpsertFactClaim 写入事实 claim 并返回 claim_id
func (a summaryStoreAdapter) UpsertFactClaim(in utils.FactClaimUpsert) (string, error) {
	row, err := a.store.UpsertFactClaim(storage.FactClaimUpsert{
		ScopeType: in.ScopeType, ScopeID: in.ScopeID, FactKey: in.FactKey, ValueText: in.ValueText,
		Polarity: in.Polarity, Cardinality: in.Cardinality, Stability: in.Stability,
		ProfileSection: in.ProfileSection, Authority: in.Authority, Confidence: in.Confidence,
		ValidFrom: in.ValidFrom, ValidTo: in.ValidTo, EvidenceType: in.EvidenceType,
		EvidenceID: in.EvidenceID, EvidenceStance: in.EvidenceStance, EvidenceWeight: in.EvidenceWeight,
		EvidenceMetadata: in.EvidenceMetadata, SupersedesClaimIDs: in.SupersedesClaimIDs,
		Reason: in.Reason, ObservedAt: in.ObservedAt,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(utils.PyStr(row["claim_id"])), nil
}

// AddFactEvidence 写入事实证据并返回摘要所需字段
func (a summaryStoreAdapter) AddFactEvidence(claimID string, in utils.FactEvidenceUpsert) (utils.FactEvidenceResult, error) {
	row, err := a.store.AddFactEvidence(claimID, storage.FactEvidenceUpsert{
		EvidenceType: in.EvidenceType, EvidenceID: in.EvidenceID, Stance: in.Stance,
		Weight: in.Weight, Metadata: in.Metadata, Reason: in.Reason, ObservedAt: in.ObservedAt,
	})
	if err != nil {
		return utils.FactEvidenceResult{}, err
	}
	result := utils.FactEvidenceResult{}
	if row != nil {
		result.Created = utils.PyBool(row["created"])
		result.ObservedAt = utils.OptionalFloat(row["observed_at"])
	}
	return result, nil
}

// Transaction 在事务内执行去重写入
func (a summaryStoreAdapter) Transaction(immediate bool, fn func(tx utils.FactDedupTx) error) error {
	return a.store.Transaction(immediate, func(tx *storage.Tx) error {
		return fn(summaryTxAdapter{tx: tx})
	})
}

// GetSummaryCheckpoint 读取摘要检查点
func (a summaryStoreAdapter) GetSummaryCheckpoint(externalID string) (map[string]any, error) {
	return a.store.GetSummaryCheckpoint(externalID)
}

// RecordSummaryCheckpoint 记录摘要检查点
func (a summaryStoreAdapter) RecordSummaryCheckpoint(externalID, chatID string, triggerMessageCount int, phase string, result map[string]any) error {
	return a.store.RecordSummaryCheckpoint(externalID, chatID, triggerMessageCount, phase, result)
}

// GetExternalMemoryRef 读取外部记忆映射
func (a summaryStoreAdapter) GetExternalMemoryRef(externalID string) (map[string]any, error) {
	return a.store.GetExternalMemoryRef(externalID)
}

// UpsertExternalMemoryRef 写入外部记忆映射
func (a summaryStoreAdapter) UpsertExternalMemoryRef(externalID, paragraphHash, sourceType string, metadata map[string]any) (map[string]any, error) {
	return a.store.UpsertExternalMemoryRef(externalID, paragraphHash, sourceType, metadata)
}

// GetChatSummaryHistory 按聊天流读取摘要段落，委托元数据存储
func (a summaryStoreAdapter) GetChatSummaryHistory(chatID string) ([]map[string]any, error) {
	return a.store.GetChatSummaryHistory(chatID)
}

// AddParagraph 写入段落
func (a summaryStoreAdapter) AddParagraph(content string, vectorIndex *int, source string, metadata map[string]any, knowledgeType string, timeMeta map[string]any) (string, error) {
	return a.store.AddParagraph(content, vectorIndex, source, metadata, knowledgeType, timeMeta)
}

// AddEntitiesForParagraph 事务内写入段落实体
func (a summaryStoreAdapter) AddEntitiesForParagraph(names []string, sourceParagraph string) ([]string, error) {
	return a.store.AddEntitiesBatch(names, nil, sourceParagraph, nil)
}

// AddRelationsForParagraph 事务内写入段落关系
func (a summaryStoreAdapter) AddRelationsForParagraph(relations [][3]string, confidence float64, sourceParagraph string) ([]string, error) {
	return a.store.AddRelationsBatch(relations, nil, confidence, sourceParagraph, nil)
}

// UpdateParagraphMetadata 更新段落元数据
func (a summaryStoreAdapter) UpdateParagraphMetadata(paragraphHash string, patch map[string]any, merge bool) (map[string]any, error) {
	return a.store.UpdateParagraphMetadata(paragraphHash, patch, merge)
}

// ComputeRelationHash 计算关系哈希
func (a summaryStoreAdapter) ComputeRelationHash(subject, predicate, object string) (string, error) {
	return a.store.ComputeRelationHash(subject, predicate, object)
}

// EnqueueEpisodeSourceRebuild 入队 Episode 重建
func (a summaryStoreAdapter) EnqueueEpisodeSourceRebuild(source, reason string) bool {
	return a.store.EnqueueEpisodeSourceRebuild(source, reason, storage.EpisodeEnqueueOptions{})
}

// EnqueueParagraphVectorBackfill 入队段落向量回填
func (a summaryStoreAdapter) EnqueueParagraphVectorBackfill(paragraphHash, errorText string) error {
	return a.store.EnqueueParagraphVectorBackfill(paragraphHash, nil, errorText, nil)
}

// EnqueuePersonProfileRefresh 入队人物画像刷新
func (a summaryStoreAdapter) EnqueuePersonProfileRefresh(personID, reason string) error {
	_, err := a.store.EnqueuePersonProfileRefresh(storage.PersonProfileRefreshEnqueueInput{PersonID: personID, Reason: reason})
	return err
}

// GetImageOccurrenceByRef 读取图片出现记录
func (a summaryStoreAdapter) GetImageOccurrenceByRef(externalRef string) (map[string]any, error) {
	return a.store.GetImageOccurrenceByRef(externalRef)
}

// ListImageObservations 读取图片观察
func (a summaryStoreAdapter) ListImageObservations(occurrenceIDs []string) ([]map[string]any, error) {
	return a.store.ListImageObservations(occurrenceIDs)
}

// UpsertImageMemoryLink 写入图片记忆关联
func (a summaryStoreAdapter) UpsertImageMemoryLink(link utils.SummaryImageMemoryLink) (map[string]any, error) {
	return a.store.UpsertImageMemoryLink(storage.ImageMemoryLinkInput{
		OccurrenceID: link.OccurrenceID, TargetType: link.TargetType, TargetID: link.TargetID,
		LinkKind: link.LinkKind, Evidence: link.Evidence,
	})
}

// summaryTxAdapter 把 storage.Tx 适配为 utils.FactDedupTx
type summaryTxAdapter struct {
	tx *storage.Tx
}

// Execute 执行事务语句
func (a summaryTxAdapter) Execute(sql string, args ...any) error {
	_, err := a.tx.Exec(sql, args...)
	return err
}

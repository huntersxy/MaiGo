// Package write 收口 A_Memorix 的关系元数据、图投影与关系向量写入。
//
// 对齐 A_memorix/core/utils/relation_write_service.py：
// 1. 元数据是主数据源，向量是次级索引；
// 2. 先写元数据，再写图边，最后按需写向量；
// 3. 向量写失败不回滚元数据，交给状态机与回填任务修复。
package write

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"MaiGo/internal/A_memorix/core/storage"
)

// RelationWriteResult 对齐 Python dataclass RelationWriteResult
type RelationWriteResult struct {
	HashValue           string
	VectorWritten       bool
	VectorAlreadyExists bool
	VectorState         string
}

// RelationVectorRecord 一次待写入的关系向量记录
type RelationVectorRecord struct {
	HashValue string
	Subject   string
	Predicate string
	Object    string
}

// RelationMetadataStore 关系写入需要的元数据存储子集
type RelationMetadataStore interface {
	Transaction(immediate bool, fn func(tx *storage.Tx) error) error
	AddRelationOn(conn *storage.Conn, subject, predicate, object string, vectorIndex *int, confidence float64, sourceParagraph string, metadata map[string]any) (string, error)
	AddRelationsBatchOn(conn *storage.Conn, relations [][3]string, vectorIndex *int, confidence float64, sourceParagraph string, metadata map[string]any) ([]string, error)
	GetRelationStatusBatch(hashes []string) (map[string]map[string]any, error)
	GetRelationStatusBatchOn(conn *storage.Conn, hashes []string) (map[string]map[string]any, error)
	SetRelationVectorState(hashValue, state string, errText *string, bumpRetry bool) (bool, error)
	SetRelationVectorStateOn(conn *storage.Conn, hashValue, state string, errText *string, bumpRetry bool) (bool, error)
}

// RelationGraphStore 关系写入需要的图存储子集
type RelationGraphStore interface {
	BatchUpdate(fn func() error) error
	AddEdges(edges []storage.Edge, weights []float64, relationHashes []string) (int, error)
}

// RelationVectorStore 关系向量存储子集，对齐 VectorStore 的成员与恢复接口
type RelationVectorStore interface {
	Contains(hashValue string) bool
	Restore(ids []string) (int, error)
	IsTombstoned(hashValue string) (bool, error)
	Add(vectors [][]float32, ids []string) (int, error)
}

// TextEmbedder 关系文本编码器，对齐 EmbeddingManager 的 encode/encode_batch
type TextEmbedder interface {
	Encode(ctx context.Context, texts []string, batchSize int, showProgress bool, normalize bool) ([][]float32, error)
	EncodeOne(ctx context.Context, text string, batchSize int, showProgress bool, normalize bool) ([]float32, error)
}

// RelationWriteConfig 构造参数，对应 Python __init__ 的关键字参数
type RelationWriteConfig struct {
	// GraphVectorStore 为 typed_id 路径使用的图向量池；nil 时沿用 VectorStore
	GraphVectorStore RelationVectorStore
	// UseTypedRelationIDs 为 true 时向量 id 使用 relation:<hash>
	UseTypedRelationIDs bool
	// RecordEmbeddingInput 在关系向量写入后记录编码输入
	RecordEmbeddingInput func(source, hashValue, text string)
	// EmbeddingBatchSize / EmbeddingMaxConcurrent 对齐管理器属性，用于计算单轮写入上限
	EmbeddingBatchSize     int
	EmbeddingMaxConcurrent int
}

// RelationWriteService 关系写入收口服务
type RelationWriteService struct {
	metadata          RelationMetadataStore
	graph             RelationGraphStore
	vectorStore       RelationVectorStore
	graphVectorStore  RelationVectorStore
	embedder          TextEmbedder
	typedRelationIDs  bool
	recordEmbeddingIn func(source, hashValue, text string)
	batchSize         int
	maxConcurrent     int
}

// NewRelationWriteService 构造关系写入服务
func NewRelationWriteService(
	metadata RelationMetadataStore,
	graph RelationGraphStore,
	vectorStore RelationVectorStore,
	embedder TextEmbedder,
	config RelationWriteConfig,
) *RelationWriteService {
	graphVectorStore := config.GraphVectorStore
	if graphVectorStore == nil {
		graphVectorStore = vectorStore
	}
	return &RelationWriteService{
		metadata:          metadata,
		graph:             graph,
		vectorStore:       vectorStore,
		graphVectorStore:  graphVectorStore,
		embedder:          embedder,
		typedRelationIDs:  config.UseTypedRelationIDs,
		recordEmbeddingIn: config.RecordEmbeddingInput,
		batchSize:         config.EmbeddingBatchSize,
		maxConcurrent:     config.EmbeddingMaxConcurrent,
	}
}

// BuildRelationVectorText 构造双表达关系向量文本，对齐 build_relation_vector_text
func BuildRelationVectorText(subject, predicate, object string) string {
	s := strings.TrimSpace(subject)
	p := strings.TrimSpace(predicate)
	o := strings.TrimSpace(object)
	return fmt.Sprintf("%s %s %s\n%s和%s的关系是%s", s, p, o, s, o, p)
}

// RelationVectorID 对齐 relation_vector_id
func RelationVectorID(hashValue string) string {
	return "relation:" + strings.TrimSpace(hashValue)
}

func (s *RelationWriteService) vectorID(hashValue string, typed bool) string {
	if typed {
		return RelationVectorID(hashValue)
	}
	return hashValue
}

func (s *RelationWriteService) embeddingWriteBatchSize() int {
	batchSize := s.batchSize
	if batchSize <= 0 {
		batchSize = 32
	}
	maxConcurrent := s.maxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	limit := batchSize * maxConcurrent
	if limit > 512 {
		limit = 512
	}
	return limit
}

// EnsureRelationVectors 批量确保关系向量存在并统一更新关系向量状态
func (s *RelationWriteService) EnsureRelationVectors(
	ctx context.Context,
	records []RelationVectorRecord,
	typedID bool,
) ([]RelationWriteResult, error) {
	if len(records) == 0 {
		return []RelationWriteResult{}, nil
	}

	targetStore := s.vectorStore
	if typedID {
		targetStore = s.graphVectorStore
	}
	if targetStore == nil || s.embedder == nil {
		errorText := "vector_channel_unavailable"
		err := s.metadata.Transaction(true, func(tx *storage.Tx) error {
			for _, record := range records {
				if _, err := s.metadata.SetRelationVectorStateOn(tx.Conn(), record.HashValue, "failed", &errorText, true); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		results := make([]RelationWriteResult, 0, len(records))
		for _, record := range records {
			results = append(results, RelationWriteResult{
				HashValue: record.HashValue, VectorState: "failed",
			})
		}
		return results, nil
	}

	vectorIDs := make(map[string]string, len(records))
	for _, record := range records {
		vectorIDs[record.HashValue] = s.vectorID(record.HashValue, typedID)
	}
	uniqueRecords := dedupeRelationVectorRecords(records)
	existingHashes := make(map[string]struct{}, len(uniqueRecords))
	pendingRecords := make([]RelationVectorRecord, 0, len(uniqueRecords))
	for _, record := range uniqueRecords {
		if targetStore.Contains(vectorIDs[record.HashValue]) {
			existingHashes[record.HashValue] = struct{}{}
		} else {
			pendingRecords = append(pendingRecords, record)
		}
	}

	restoredHashes := map[string]struct{}{}
	if len(pendingRecords) > 0 {
		ids := make([]string, 0, len(pendingRecords))
		for _, record := range pendingRecords {
			ids = append(ids, vectorIDs[record.HashValue])
		}
		if _, err := targetStore.Restore(ids); err != nil {
			return nil, err
		}
		stillPending := make([]RelationVectorRecord, 0, len(pendingRecords))
		for _, record := range pendingRecords {
			if targetStore.Contains(vectorIDs[record.HashValue]) {
				restoredHashes[record.HashValue] = struct{}{}
			} else {
				stillPending = append(stillPending, record)
			}
		}
		pendingRecords = stillPending
	}

	if err := s.metadata.Transaction(true, func(tx *storage.Tx) error {
		for _, record := range uniqueRecords {
			state := "pending"
			if _, ok := existingHashes[record.HashValue]; ok {
				state = "ready"
			}
			if _, ok := restoredHashes[record.HashValue]; ok {
				state = "ready"
			}
			if _, err := s.metadata.SetRelationVectorStateOn(tx.Conn(), record.HashValue, state, nil, false); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	if len(pendingRecords) == 0 {
		results := make([]RelationWriteResult, 0, len(records))
		for _, record := range records {
			_, restored := restoredHashes[record.HashValue]
			_, existing := existingHashes[record.HashValue]
			results = append(results, RelationWriteResult{
				HashValue:           record.HashValue,
				VectorWritten:       restored,
				VectorAlreadyExists: existing,
				VectorState:         "ready",
			})
		}
		return results, nil
	}

	addedHashes := make(map[string]struct{}, len(restoredHashes))
	for hashValue := range restoredHashes {
		addedHashes[hashValue] = struct{}{}
	}
	failedErrors := map[string]string{}
	writeBatchSize := s.embeddingWriteBatchSize()
	for offset := 0; offset < len(pendingRecords); offset += writeBatchSize {
		end := offset + writeBatchSize
		if end > len(pendingRecords) {
			end = len(pendingRecords)
		}
		batch := pendingRecords[offset:end]
		if err := s.ensureRelationVectorBatch(ctx, targetStore, vectorIDs, batch, existingHashes, addedHashes); err != nil {
			message := truncateRunes(err.Error(), 500)
			for _, record := range batch {
				failedErrors[record.HashValue] = message
			}
		}
	}

	if err := s.metadata.Transaction(true, func(tx *storage.Tx) error {
		for _, record := range pendingRecords {
			if message, failed := failedErrors[record.HashValue]; failed {
				errorText := message
				if _, err := s.metadata.SetRelationVectorStateOn(tx.Conn(), record.HashValue, "failed", &errorText, true); err != nil {
					return err
				}
			} else if _, err := s.metadata.SetRelationVectorStateOn(tx.Conn(), record.HashValue, "ready", nil, false); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	successCount := len(pendingRecords) - len(failedErrors)
	if successCount > 0 {
		zap.L().Info("关系向量写入成功",
			zap.Int("metric.relation_vector_write_success_count", successCount))
	}
	if len(failedErrors) > 0 {
		firstError := ""
		for _, message := range failedErrors {
			firstError = message
			break
		}
		zap.L().Warn("关系向量写入失败",
			zap.Int("metric.relation_vector_write_fail_count", len(failedErrors)),
			zap.String("err", firstError))
	}

	results := make([]RelationWriteResult, 0, len(records))
	for _, record := range records {
		_, added := addedHashes[record.HashValue]
		_, existing := existingHashes[record.HashValue]
		state := "ready"
		if _, failed := failedErrors[record.HashValue]; failed {
			state = "failed"
		}
		results = append(results, RelationWriteResult{
			HashValue:           record.HashValue,
			VectorWritten:       added,
			VectorAlreadyExists: existing,
			VectorState:         state,
		})
	}
	return results, nil
}

func (s *RelationWriteService) ensureRelationVectorBatch(
	ctx context.Context,
	targetStore RelationVectorStore,
	vectorIDs map[string]string,
	batch []RelationVectorRecord,
	existingHashes map[string]struct{},
	addedHashes map[string]struct{},
) error {
	texts := make([]string, 0, len(batch))
	for _, record := range batch {
		texts = append(texts, BuildRelationVectorText(record.Subject, record.Predicate, record.Object))
	}
	embeddings, err := s.embedder.Encode(ctx, texts, 0, false, true)
	if err != nil {
		return err
	}
	if len(embeddings) != len(batch) {
		return fmt.Errorf("关系批量向量数量不匹配: %d vs %d", len(embeddings), len(batch))
	}

	recordsToAdd := make([]RelationVectorRecord, 0, len(batch))
	vectorsToAdd := make([][]float32, 0, len(batch))
	for index, record := range batch {
		if targetStore.Contains(vectorIDs[record.HashValue]) {
			existingHashes[record.HashValue] = struct{}{}
			continue
		}
		recordsToAdd = append(recordsToAdd, record)
		vectorsToAdd = append(vectorsToAdd, embeddings[index])
	}
	if len(recordsToAdd) == 0 {
		return nil
	}
	ids := make([]string, 0, len(recordsToAdd))
	for _, record := range recordsToAdd {
		ids = append(ids, vectorIDs[record.HashValue])
	}
	if _, err := targetStore.Add(vectorsToAdd, ids); err != nil {
		return err
	}
	missing := 0
	for _, record := range recordsToAdd {
		if !targetStore.Contains(vectorIDs[record.HashValue]) {
			missing++
		}
	}
	if missing > 0 {
		return fmt.Errorf("关系批量向量写入不完整: missing=%d", missing)
	}
	if s.recordEmbeddingIn != nil {
		for _, record := range recordsToAdd {
			s.recordEmbeddingIn("relation", record.HashValue,
				BuildRelationVectorText(record.Subject, record.Predicate, record.Object))
		}
	}
	for _, record := range recordsToAdd {
		addedHashes[record.HashValue] = struct{}{}
	}
	return nil
}

// UpsertRelationsWithVectors 在一个元数据事务和一个图批次中写入多条关系
func (s *RelationWriteService) UpsertRelationsWithVectors(
	ctx context.Context,
	relations [][3]string,
	confidence float64,
	sourceParagraph string,
	metadata map[string]any,
	writeVector bool,
) ([]RelationWriteResult, error) {
	normalized := make([][3]string, 0, len(relations))
	for _, relation := range relations {
		subject := strings.TrimSpace(relation[0])
		predicate := strings.TrimSpace(relation[1])
		object := strings.TrimSpace(relation[2])
		if subject == "" || predicate == "" || object == "" {
			continue
		}
		normalized = append(normalized, [3]string{subject, predicate, object})
	}
	if len(normalized) == 0 {
		return []RelationWriteResult{}, nil
	}
	if metadata == nil {
		metadata = map[string]any{}
	}

	var relationHashes []string
	err := s.metadata.Transaction(true, func(tx *storage.Tx) error {
		var err error
		relationHashes, err = s.metadata.AddRelationsBatchOn(tx.Conn(), normalized, nil, confidence, sourceParagraph, metadata)
		return err
	})
	if err != nil {
		return nil, err
	}
	statuses, err := s.metadata.GetRelationStatusBatch(relationHashes)
	if err != nil {
		return nil, err
	}
	if s.graph != nil {
		edges := make([]storage.Edge, 0, len(relationHashes))
		hashes := make([]string, 0, len(relationHashes))
		for index, relationHash := range relationHashes {
			status := statuses[relationHash]
			if relationStatusInactive(status) {
				continue
			}
			edges = append(edges, storage.Edge{Source: normalized[index][0], Target: normalized[index][2]})
			hashes = append(hashes, relationHash)
		}
		if len(edges) > 0 {
			if err := s.graph.BatchUpdate(func() error {
				_, addErr := s.graph.AddEdges(edges, nil, hashes)
				return addErr
			}); err != nil {
				zap.L().Warn("关系元数据已写入，但图谱投影失败", zap.Error(err))
			}
		}
	}

	records := make([]RelationVectorRecord, 0, len(relationHashes))
	for index, relationHash := range relationHashes {
		records = append(records, RelationVectorRecord{
			HashValue: relationHash,
			Subject:   normalized[index][0],
			Predicate: normalized[index][1],
			Object:    normalized[index][2],
		})
	}
	if !writeVector {
		results := make([]RelationWriteResult, 0, len(records))
		for _, record := range records {
			results = append(results, RelationWriteResult{
				HashValue: record.HashValue, VectorState: "none",
			})
		}
		return results, nil
	}
	return s.EnsureRelationVectors(ctx, records, s.typedRelationIDs)
}

// EnsureRelationVector 为已有关系确保向量存在并更新状态，对齐 ensure_relation_vector
func (s *RelationWriteService) EnsureRelationVector(
	ctx context.Context,
	hashValue, subject, predicate, object string,
	maxErrorLen int,
	typedID bool,
	beforeVectorWrite func(),
) (RelationWriteResult, error) {
	vectorID := strings.TrimSpace(hashValue)
	if typedID {
		vectorID = RelationVectorID(hashValue)
	}
	targetStore := s.vectorStore
	if typedID {
		targetStore = s.graphVectorStore
	}
	if targetStore == nil || s.embedder == nil {
		errorText := "vector_channel_unavailable"
		if _, err := s.metadata.SetRelationVectorState(hashValue, "failed", &errorText, true); err != nil {
			return RelationWriteResult{}, err
		}
		return RelationWriteResult{HashValue: hashValue, VectorState: "failed"}, nil
	}
	if targetStore.Contains(vectorID) {
		if _, err := s.metadata.SetRelationVectorState(hashValue, "ready", nil, false); err != nil {
			return RelationWriteResult{}, err
		}
		return RelationWriteResult{
			HashValue:           hashValue,
			VectorAlreadyExists: true,
			VectorState:         "ready",
		}, nil
	}
	if _, err := s.metadata.SetRelationVectorState(hashValue, "pending", nil, false); err != nil {
		return RelationWriteResult{}, err
	}

	if maxErrorLen <= 0 {
		maxErrorLen = 500
	}
	tombstoned, err := targetStore.IsTombstoned(vectorID)
	var embedding []float32
	var vectorText string
	if err == nil && !tombstoned {
		vectorText = BuildRelationVectorText(subject, predicate, object)
		embedding, err = s.embedder.EncodeOne(ctx, vectorText, 0, false, true)
	}
	if err != nil {
		errorText := truncateRunes(err.Error(), maxErrorLen)
		if _, stateErr := s.metadata.SetRelationVectorState(hashValue, "failed", &errorText, true); stateErr != nil {
			return RelationWriteResult{}, stateErr
		}
		zap.L().Warn("关系向量写入失败",
			zap.Int("metric.relation_vector_write_fail_count", 1),
			zap.String("hash", truncateRunes(hashValue, 16)),
			zap.String("err", errorText))
		return RelationWriteResult{HashValue: hashValue, VectorState: "failed"}, nil
	}

	// 授权回调必须在所有 await 结束后、真正变更向量池前同步执行
	if beforeVectorWrite != nil {
		beforeVectorWrite()
	}

	if tombstoned {
		restored, restoreErr := targetStore.Restore([]string{vectorID})
		if restoreErr == nil && (restored != 1 || !targetStore.Contains(vectorID)) {
			restoreErr = fmt.Errorf("关系向量恢复后成员校验失败")
		}
		if restoreErr != nil {
			errorText := truncateRunes(restoreErr.Error(), maxErrorLen)
			if _, stateErr := s.metadata.SetRelationVectorState(hashValue, "failed", &errorText, true); stateErr != nil {
				return RelationWriteResult{}, stateErr
			}
			zap.L().Warn("关系向量写入失败",
				zap.Int("metric.relation_vector_write_fail_count", 1),
				zap.String("hash", truncateRunes(hashValue, 16)),
				zap.String("err", errorText))
			return RelationWriteResult{HashValue: hashValue, VectorState: "failed"}, nil
		}
		if _, stateErr := s.metadata.SetRelationVectorState(hashValue, "ready", nil, false); stateErr != nil {
			return RelationWriteResult{}, stateErr
		}
		zap.L().Info("关系向量恢复成功",
			zap.Int("metric.relation_vector_restore_success", 1),
			zap.String("hash", truncateRunes(hashValue, 16)))
		return RelationWriteResult{
			HashValue:     hashValue,
			VectorWritten: true,
			VectorState:   "ready",
		}, nil
	}

	if embedding == nil {
		errorText := truncateRunes("关系向量 embedding 未生成", maxErrorLen)
		if _, stateErr := s.metadata.SetRelationVectorState(hashValue, "failed", &errorText, true); stateErr != nil {
			return RelationWriteResult{}, stateErr
		}
		return RelationWriteResult{HashValue: hashValue, VectorState: "failed"}, nil
	}
	added, addErr := targetStore.Add([][]float32{embedding}, []string{vectorID})
	if addErr == nil && !targetStore.Contains(vectorID) {
		addErr = fmt.Errorf("关系向量写入后成员校验失败")
	}
	if addErr != nil {
		errorText := truncateRunes(addErr.Error(), maxErrorLen)
		if _, stateErr := s.metadata.SetRelationVectorState(hashValue, "failed", &errorText, true); stateErr != nil {
			return RelationWriteResult{}, stateErr
		}
		zap.L().Warn("关系向量写入失败",
			zap.Int("metric.relation_vector_write_fail_count", 1),
			zap.String("hash", truncateRunes(hashValue, 16)),
			zap.String("err", errorText))
		return RelationWriteResult{HashValue: hashValue, VectorState: "failed"}, nil
	}
	if s.recordEmbeddingIn != nil {
		s.recordEmbeddingIn("relation", hashValue, vectorText)
	}
	if _, stateErr := s.metadata.SetRelationVectorState(hashValue, "ready", nil, false); stateErr != nil {
		return RelationWriteResult{}, stateErr
	}
	zap.L().Info("关系向量写入成功",
		zap.Int("metric.relation_vector_write_success_count", 1),
		zap.String("hash", truncateRunes(hashValue, 16)))
	return RelationWriteResult{
		HashValue:           hashValue,
		VectorWritten:       added > 0,
		VectorAlreadyExists: added == 0,
		VectorState:         "ready",
	}, nil
}

// UpsertRelationWithVector 单条关系统一写入：metadata -> graph edge -> vector
func (s *RelationWriteService) UpsertRelationWithVector(
	ctx context.Context,
	subject, predicate, object string,
	confidence float64,
	sourceParagraph string,
	metadata map[string]any,
	writeVector bool,
) (RelationWriteResult, error) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	var relationHash string
	err := s.metadata.Transaction(true, func(tx *storage.Tx) error {
		hashValue, err := s.metadata.AddRelationOn(tx.Conn(), subject, predicate, object, nil, confidence, sourceParagraph, metadata)
		if err != nil {
			return err
		}
		relationHash = hashValue
		if s.graph == nil {
			return nil
		}
		return s.graph.BatchUpdate(func() error {
			statuses, err := s.metadata.GetRelationStatusBatchOn(tx.Conn(), []string{hashValue})
			if err != nil {
				return err
			}
			if relationStatusInactive(statuses[hashValue]) {
				return nil
			}
			_, err = s.graph.AddEdges([]storage.Edge{{Source: subject, Target: object}}, nil, []string{hashValue})
			return err
		})
	})
	if err != nil {
		return RelationWriteResult{}, err
	}
	if !writeVector {
		return RelationWriteResult{HashValue: relationHash, VectorState: "none"}, nil
	}
	return s.EnsureRelationVector(ctx, relationHash, subject, predicate, object, 500, s.typedRelationIDs, nil)
}

func relationStatusInactive(status map[string]any) bool {
	if status == nil {
		return false
	}
	value, exists := status["is_inactive"]
	if !exists || value == nil {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	case string:
		return typed != "" && typed != "0" && !strings.EqualFold(typed, "false")
	}
	return false
}

func dedupeRelationVectorRecords(records []RelationVectorRecord) []RelationVectorRecord {
	seen := map[string]struct{}{}
	order := make([]string, 0, len(records))
	values := map[string]RelationVectorRecord{}
	for _, record := range records {
		if _, exists := seen[record.HashValue]; !exists {
			seen[record.HashValue] = struct{}{}
			order = append(order, record.HashValue)
		}
		// Python dict 推导保留最后一个值，但顺序按首次出现
		values[record.HashValue] = record
	}
	out := make([]RelationVectorRecord, 0, len(order))
	for _, hashValue := range order {
		out = append(out, values[hashValue])
	}
	return out
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

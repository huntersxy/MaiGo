package services

// 记忆写入服务，逐段对齐 ingest_service.py 的 MemoryIngestService

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"MaiGo/internal/A_memorix/core/storage"
	"MaiGo/internal/A_memorix/core/utils"
)

// IngestTextParams 对齐 ingest_text 的关键字参数（本地镜像，供 services 层使用）
type IngestTextParams struct {
	ExternalID    string
	SourceType    string
	Text          string
	ChatID        string
	PersonIDs     []string
	Participants  []string
	Timestamp     *float64
	TimeStart     *float64
	TimeEnd       *float64
	Tags          []string
	Metadata      map[string]any
	Entities      []string
	Relations     []map[string]any
	RespectFilter bool
	UserID        string
	GroupID       string
}

// IngestSummaryParams 对齐 ingest_summary 的关键字参数（本地镜像）
type IngestSummaryParams struct {
	ExternalID    string
	ChatID        string
	Text          string
	Participants  []string
	TimeStart     *float64
	TimeEnd       *float64
	Tags          []string
	Metadata      map[string]any
	RespectFilter bool
	UserID        string
	GroupID       string
}

// IngestDeps 写入服务的依赖端口
type IngestDeps struct {
	IsChatFiltered                 func(respectFilter bool, streamID string, groupID string, userID string) bool
	Initialize                     func(ctx context.Context) error
	Metadata                       func() *storage.Store
	Summarize                      func(ctx context.Context, params map[string]any) (map[string]any, error)
	Persist                        func()
	MarkPersonActive               func(personID string)
	EnqueuePersonProfileRefresh    func(personID string, reason string)
	EnqueueParagraphVectorBackfill func(paragraphHash string, errorText string)
	AllowMetadataOnlyWrite         func() bool
	EmbeddingFallbackEnabled       func() bool
	IsEmbeddingDegraded            func() bool
	SetEmbeddingDegraded           func(active bool, reason string, checkedAt *float64)
	EmbeddingEncode                func(ctx context.Context, texts []string) ([][]float32, error)
	ParagraphStore                 func() *storage.VectorStore
	GraphVectorStore               func() *storage.VectorStore
	VectorStore                    func() *storage.VectorStore
	DualVectorPoolsEnabled         func(ctx context.Context) bool
	GraphVectorID                  func(kind string, hashValue string) string
	RecordVectorInput              func(pool string, hashValue string, text string)
	RelationVectorsEnabled         func() bool
	UpsertRelationWithVector       func(ctx context.Context, subject, predicate, object string, confidence float64, sourceParagraph string, metadata map[string]any, writeVector bool) (string, error)
	EnsureRelationVector           func(ctx context.Context, hashValue, subject, predicate, object string, typedID bool, beforeVectorWrite func()) (vectorWritten bool, already bool, err error)
	BuildRelationVectorText        func(subject, predicate, object string) string
	PlanSourceRebuild              func(ctx context.Context, source string, generation any) (map[string]any, error)
	GenerationSignature            func() any
	GenerationHash                 func(generation any) string
	ShouldAutoEnqueueEpisode       func(sourceType string) bool
	// NewFactDeduplicator 对齐 Python FactDeduplicator(...)，未注入时 person_fact 语义去重不可用
	NewFactDeduplicator func() *utils.FactDeduplicator
}

// IngestService 记忆写入服务
type IngestService struct {
	*Base
	deps IngestDeps
}

// NewIngestService 构造记忆写入服务
func NewIngestService(base *Base, deps IngestDeps) *IngestService {
	return &IngestService{Base: base, deps: deps}
}

func (s *IngestService) store() *storage.Store {
	if s.deps.Metadata == nil {
		return nil
	}
	return s.deps.Metadata()
}

func (s *IngestService) initialize(ctx context.Context) error {
	if s.deps.Initialize != nil {
		return s.deps.Initialize(ctx)
	}
	return nil
}

func (s *IngestService) persist() {
	if s.deps.Persist != nil {
		s.deps.Persist()
	}
}

// IngestSummary 对齐 ingest_summary
func (s *IngestService) IngestSummary(ctx context.Context, params IngestSummaryParams) (map[string]any, error) {
	// Python compute_hash 用原始 text（未 strip）；过滤/空判定仍用 strip 后正文
	externalToken := strings.TrimSpace(params.ExternalID)
	if externalToken == "" {
		hashValue, err := utils.ComputeHash(fmt.Sprintf("chat_summary:%s:%s", params.ChatID, params.Text))
		if err != nil {
			return nil, err
		}
		externalToken = hashValue
	}
	text := strings.TrimSpace(params.Text)
	if s.deps.IsChatFiltered != nil && s.deps.IsChatFiltered(params.RespectFilter, params.ChatID, params.GroupID, params.UserID) {
		return map[string]any{"success": true, "stored_ids": []string{}, "skipped_ids": []string{externalToken}, "detail": "chat_filtered"}, nil
	}
	summaryMeta := cloneAnyMap(params.Metadata)
	if _, ok := summaryMeta["kind"]; !ok {
		summaryMeta["kind"] = "chat_summary"
	}
	generateFromChat := pyTruthy(summaryMeta["generate_from_chat"])
	if text == "" || generateFromChat {
		metadata := cloneAnyMap(summaryMeta)
		metadata["external_id"] = externalToken
		metadata["chat_id"] = strings.TrimSpace(params.ChatID)
		metadata["source_type"] = "chat_summary"
		if s.deps.Summarize == nil {
			return nil, fmt.Errorf("聊天摘要生成器未注入")
		}
		var contextLength any
		if parsed := utils.OptionalInt(summaryMeta["context_length"]); parsed != nil {
			contextLength = *parsed
		}
		result, err := s.deps.Summarize(ctx, map[string]any{
			"chat_id": params.ChatID, "context_length": contextLength,
			"include_personality": summaryMeta["include_personality"],
			"time_end":            params.TimeEnd, "metadata": metadata,
		})
		if err != nil {
			return nil, err
		}
		if _, ok := result["external_id"]; !ok {
			result["external_id"] = params.ExternalID
		}
		if _, ok := result["chat_id"]; !ok {
			result["chat_id"] = params.ChatID
		}
		return result, nil
	}
	return s.IngestText(ctx, IngestTextParams{
		ExternalID: params.ExternalID, SourceType: "chat_summary", Text: params.Text, ChatID: params.ChatID,
		Participants: params.Participants, TimeStart: params.TimeStart, TimeEnd: params.TimeEnd,
		Tags: params.Tags, Metadata: summaryMeta, RespectFilter: params.RespectFilter,
		UserID: params.UserID, GroupID: params.GroupID,
	})
}

// IngestText 对齐 ingest_text
func (s *IngestService) IngestText(ctx context.Context, params IngestTextParams) (map[string]any, error) {
	metadata := cloneAnyMap(params.Metadata)
	personTokens := stringTokens(params.PersonIDs)
	// 只有宿主已确认目标人物和实际来源的自动写回参与语义去重
	if strings.TrimSpace(params.SourceType) == "person_fact" &&
		!pyTruthy(metadata["dedup_managed"]) &&
		pyTruthy(metadata["evidence_message_ids"]) &&
		len(personTokens) == 1 &&
		asString(metadata["writeback_source"]) == "memory_flow_service" {
		if s.deps.IsChatFiltered != nil && s.deps.IsChatFiltered(params.RespectFilter, params.ChatID, params.GroupID, params.UserID) {
			return map[string]any{"success": true, "stored_ids": []string{}, "detail": "chat_filtered"}, nil
		}
		if err := s.initialize(ctx); err != nil {
			return nil, err
		}
		store := s.store()
		if store == nil {
			return nil, fmt.Errorf("元数据存储未初始化")
		}
		if s.deps.NewFactDeduplicator == nil {
			return nil, fmt.Errorf("人物事实去重器未注入")
		}
		dedup := s.deps.NewFactDeduplicator()
		if dedup == nil {
			return nil, fmt.Errorf("人物事实去重器未注入")
		}
		release, err := utils.AcquireFactScopeLock(ctx, ingestFactDedupStore{store: store}, "person", personTokens[0])
		if err != nil {
			return nil, err
		}
		defer release()
		return s.ingestPersonFact(ctx, params, metadata, personTokens[0], dedup)
	}
	return s.ingestTextRaw(ctx, params, metadata)
}

func (s *IngestService) ingestTextRaw(ctx context.Context, params IngestTextParams, metadata map[string]any) (map[string]any, error) {
	content := utils.NormalizeText(params.Text)
	externalToken := strings.TrimSpace(params.ExternalID)
	if externalToken == "" {
		hashValue, err := utils.ComputeHash(fmt.Sprintf("%s:%s:%s", params.SourceType, params.ChatID, content))
		if err != nil {
			return nil, err
		}
		externalToken = hashValue
	}
	if s.deps.IsChatFiltered != nil && s.deps.IsChatFiltered(params.RespectFilter, params.ChatID, params.GroupID, params.UserID) {
		return map[string]any{"success": true, "stored_ids": []string{}, "skipped_ids": []string{externalToken}, "detail": "chat_filtered"}, nil
	}
	if err := s.initialize(ctx); err != nil {
		return nil, err
	}
	store := s.store()
	if store == nil {
		return nil, fmt.Errorf("元数据存储未初始化")
	}
	if content == "" {
		return map[string]any{"stored_ids": []string{}, "skipped_ids": []string{externalToken}, "reason": "empty_text"}, nil
	}
	existingRef, err := store.GetExternalMemoryRef(externalToken)
	if err != nil {
		return nil, err
	}
	if existingRef != nil && asString(asMap(existingRef["metadata"])["dedup_phase"]) != "pending" {
		if strings.TrimSpace(params.SourceType) == "person_fact" {
			paragraphHash := asString(existingRef["paragraph_hash"])
			paragraph, err := store.GetParagraph(paragraphHash)
			if err != nil {
				return nil, err
			}
			if paragraph == nil || utils.NormalizeText(asString(paragraph["content"])) != content {
				return nil, fmt.Errorf("person_fact external_id=%s 与已保存的段落正文不一致", externalToken)
			}
			claimIDs, err := s.writePersonFactClaims(store, paragraphHash, content, stringTokens(params.PersonIDs), metadata, params.Timestamp)
			if err != nil {
				return nil, err
			}
			if evidence, ok := metadata["evidence_message_ids"]; ok && pyTruthy(evidence) {
				if _, err := store.UpdateParagraphMetadata(paragraphHash, map[string]any{"evidence_message_ids": evidence}, true); err != nil {
					return nil, err
				}
			}
			for _, personID := range stringTokens(params.PersonIDs) {
				if s.deps.EnqueuePersonProfileRefresh != nil {
					s.deps.EnqueuePersonProfileRefresh(personID, "person_fact_reverified")
				}
			}
			s.persist()
			return map[string]any{"success": true, "stored_ids": []string{paragraphHash}, "skipped_ids": []string{}, "fact_claim_ids": claimIDs, "detail": "person_fact_reverified"}, nil
		}
		return map[string]any{"stored_ids": []string{}, "skipped_ids": []string{asString(existingRef["paragraph_hash"])}, "reason": "exists"}, nil
	}

	personTokens := stringTokens(params.PersonIDs)
	personSet := make(map[string]struct{}, len(personTokens))
	for _, item := range personTokens {
		personSet[item] = struct{}{}
	}
	participantTokens := make([]string, 0)
	for _, item := range stringTokens(params.Participants) {
		if _, ok := personSet[item]; !ok {
			participantTokens = append(participantTokens, item)
		}
	}
	entityTokens := utils.MergeTokens(params.Entities, participantTokens)
	source := utils.BuildSource(params.SourceType, params.ChatID, personTokens)
	paragraphMeta := cloneAnyMap(metadata)
	receiptMetadata := asMap(paragraphMeta["dedup_receipt"])
	delete(paragraphMeta, "dedup_receipt")
	paragraphMeta["external_id"] = externalToken
	paragraphMeta["source_type"] = strings.TrimSpace(params.SourceType)
	paragraphMeta["chat_id"] = strings.TrimSpace(params.ChatID)
	paragraphMeta["person_ids"] = personTokens
	paragraphMeta["participants"] = participantTokens
	paragraphMeta["tags"] = stringTokens(params.Tags)
	warnings := make([]string, 0)

	timeMeta := map[string]any{}
	if params.Timestamp != nil {
		timeMeta["event_time"] = *params.Timestamp
	}
	if params.TimeStart != nil {
		timeMeta["event_time_start"] = *params.TimeStart
	}
	if params.TimeEnd != nil {
		timeMeta["event_time_end"] = *params.TimeEnd
	}
	if len(timeMeta) > 0 {
		timeMeta["time_granularity"] = "minute"
		timeMeta["time_confidence"] = 0.95
	}
	paragraphHash, err := store.AddParagraph(content, nil, source, paragraphMeta, resolveKnowledgeType(params.SourceType), timeMeta)
	if err != nil {
		return nil, err
	}
	vectorResult, err := s.writeParagraphVectorOrEnqueue(ctx, paragraphHash, content, "ingest_text")
	if err != nil {
		return nil, err
	}
	if warning := strings.TrimSpace(asString(vectorResult["warning"])); warning != "" {
		warnings = append(warnings, warning)
	}
	for _, name := range entityTokens {
		entityHash, err := store.AddEntity(name, nil, paragraphHash, nil)
		if err != nil {
			return nil, err
		}
		if _, err := s.EnsureEntityVector(ctx, map[string]any{"hash": entityHash, "name": name}); err != nil {
			return nil, err
		}
	}
	storedRelations := make([]string, 0)
	for _, raw := range params.Relations {
		row := asMap(raw)
		subject := strings.TrimSpace(asString(row["subject"]))
		predicate := strings.TrimSpace(asString(row["predicate"]))
		object := strings.TrimSpace(asString(row["object"]))
		if subject == "" || predicate == "" || object == "" {
			continue
		}
		confidenceValue := 1.0
		if value, ok := row["confidence"]; ok && value != nil {
			confidenceValue, _ = asFloat(value)
		}
		relationMetadata := asMap(row["metadata"])
		if relationMetadata == nil {
			relationMetadata = map[string]any{"external_id": externalToken, "source_type": params.SourceType}
		}
		if s.deps.UpsertRelationWithVector == nil {
			return nil, fmt.Errorf("关系写入服务未注入")
		}
		relationHash, err := s.deps.UpsertRelationWithVector(ctx, subject, predicate, object, confidenceValue, paragraphHash, relationMetadata, s.relationVectorsEnabled())
		if err != nil {
			return nil, err
		}
		if _, err := store.LinkParagraphRelation(paragraphHash, relationHash); err != nil {
			return nil, err
		}
		storedRelations = append(storedRelations, relationHash)
	}
	factClaimIDs := make([]string, 0)
	if strings.TrimSpace(params.SourceType) == "person_fact" && !pyTruthy(paragraphMeta["dedup_managed"]) {
		claimIDs, err := s.writePersonFactClaims(store, paragraphHash, content, personTokens, paragraphMeta, params.Timestamp)
		if err != nil {
			return nil, err
		}
		factClaimIDs = claimIDs
		if len(claimIDs) > 0 {
			if _, err := store.UpdateParagraphMetadata(paragraphHash, map[string]any{"fact_claim_ids": claimIDs}, true); err != nil {
				return nil, err
			}
		}
	}
	receiptPayload := map[string]any{"chat_id": params.ChatID, "person_ids": personTokens}
	for key, value := range receiptMetadata {
		receiptPayload[key] = value
	}
	if asString(paragraphMeta["dedup_phase"]) == "pending" {
		receiptPayload["dedup_phase"] = "pending"
	}
	if _, err := store.UpsertExternalMemoryRef(externalToken, paragraphHash, params.SourceType, receiptPayload); err != nil {
		return nil, err
	}
	s.persist()
	for _, personID := range personTokens {
		if s.deps.MarkPersonActive != nil {
			s.deps.MarkPersonActive(personID)
		}
		if s.deps.EnqueuePersonProfileRefresh != nil {
			reason := strings.TrimSpace(params.SourceType)
			if reason == "" {
				reason = "ingest_text"
			}
			s.deps.EnqueuePersonProfileRefresh(personID, reason)
		}
	}
	storedIDs := append([]string{paragraphHash}, storedRelations...)
	payload := map[string]any{"stored_ids": storedIDs, "skipped_ids": []string{}, "fact_claim_ids": factClaimIDs}
	if len(warnings) > 0 {
		payload["warnings"] = warnings
		payload["detail"] = "vector_degraded_write"
	}
	return payload, nil
}

// writePersonFactClaims 对齐 _write_person_fact_claims
func (s *IngestService) writePersonFactClaims(store *storage.Store, paragraphHash string, content string, personIDs []string, metadata map[string]any, timestamp *float64) ([]string, error) {
	paragraphToken := strings.TrimSpace(paragraphHash)
	statement := strings.TrimSpace(content)
	if paragraphToken == "" || statement == "" {
		return nil, fmt.Errorf("人物事实 claim 缺少段落证据")
	}
	claimSpec := asMap(metadata["fact_claim"])
	confidence := 1.0
	if claimSpec != nil && claimSpec["confidence"] != nil {
		confidence, _ = asFloat(claimSpec["confidence"])
	}
	evidenceSource := strings.TrimSpace(asString(metadata["evidence_source"]))
	trust := strings.ToLower(strings.TrimSpace(asString(claimSpec["trust"])))
	defaultAuthority := "summary_derived"
	switch trust {
	case "manual_confirmed":
		defaultAuthority = "manual"
	case "server_verified":
		defaultAuthority = "direct_user"
	case "trusted_import":
		defaultAuthority = "imported"
	default:
		if evidenceSource == "user_supported" {
			defaultAuthority = "direct_user"
		}
	}
	claimIDs := make([]string, 0)
	for _, personID := range personIDs {
		personToken := strings.TrimSpace(personID)
		if personToken == "" {
			continue
		}
		factKey := asString(claimSpec["fact_key"])
		if factKey == "" {
			factKey = "statement:" + paragraphToken
		}
		polarity := asString(claimSpec["polarity"])
		if polarity == "" {
			polarity = "positive"
		}
		cardinality := asString(claimSpec["cardinality"])
		if cardinality == "" {
			cardinality = "set"
		}
		stability := asString(claimSpec["stability"])
		if stability == "" {
			stability = "stable"
		}
		profileSection := asString(claimSpec["profile_section"])
		if profileSection == "" {
			profileSection = "stable_facts"
		}
		authority := asString(claimSpec["authority"])
		if authority == "" {
			authority = defaultAuthority
		}
		supersedes := make([]string, 0)
		for _, item := range asList(claimSpec["supersedes_claim_ids"]) {
			if text := strings.TrimSpace(asString(item)); text != "" {
				supersedes = append(supersedes, text)
			}
		}
		result, err := store.UpsertFactClaim(storage.FactClaimUpsert{
			ScopeType: "person", ScopeID: personToken, FactKey: factKey, ValueText: statement,
			Polarity: polarity, Cardinality: cardinality, Stability: stability, ProfileSection: profileSection,
			Authority: authority, Confidence: &confidence,
			ValidFrom: optionalFloat(claimSpec["valid_from"]), ValidTo: optionalFloat(claimSpec["valid_to"]),
			EvidenceType: "paragraph", EvidenceID: paragraphToken, EvidenceStance: "support", EvidenceWeight: floatPtr(1.0),
			EvidenceMetadata: map[string]any{
				"source_type": "person_fact", "evidence_message_ids": asList(metadata["evidence_message_ids"]),
				"evidence_source": evidenceSource, "trust": trust,
			},
			SupersedesClaimIDs: supersedes,
			Reason:             ingestFirstNonEmpty(asString(claimSpec["reason"]), "person_fact_ingest"),
			ObservedAt:         timestamp,
		})
		if err != nil {
			return nil, err
		}
		if pyTruthy(result["idempotent"]) &&
			(asString(result["stability"]) != stability || asString(result["profile_section"]) != profileSection) {
			if _, err := store.UpdateFactClaimClassification(asString(result["claim_id"]), storage.FactClaimClassification{
				Stability:      stability,
				ProfileSection: profileSection,
				Authority:      defaultAuthority,
				Confidence:     confidence,
				ValidFrom:      optionalFloat(result["valid_from"]),
				ValidTo:        optionalFloat(result["valid_to"]),
				Reason:         "person_fact_reverified",
			}); err != nil {
				return nil, err
			}
		}
		claimIDs = append(claimIDs, asString(result["claim_id"]))
	}
	return claimIDs, nil
}

// WriteParagraphVectorOrEnqueue 对齐 _write_paragraph_vector_or_enqueue
func (s *IngestService) WriteParagraphVectorOrEnqueue(ctx context.Context, paragraphHash string, content string, contextText string) (map[string]any, error) {
	return s.writeParagraphVectorOrEnqueue(ctx, paragraphHash, content, contextText)
}

func (s *IngestService) writeParagraphVectorOrEnqueue(ctx context.Context, paragraphHash string, content string, contextText string) (map[string]any, error) {
	token := strings.TrimSpace(paragraphHash)
	text := strings.TrimSpace(content)
	if token == "" || text == "" {
		return map[string]any{"success": false, "vector_written": false, "queued": false, "warning": "", "detail": "invalid_paragraph_input"}, nil
	}
	allowMetadataOnly := s.allowMetadataOnly()
	var targetStore *storage.VectorStore
	if s.deps.ParagraphStore != nil {
		targetStore = s.deps.ParagraphStore()
	}
	if targetStore == nil {
		if !allowMetadataOnly {
			return nil, fmt.Errorf("向量写入依赖未初始化")
		}
		s.enqueueBackfill(token, "vector_runtime_components_missing")
		return map[string]any{"success": true, "vector_written": false, "queued": true, "warning": "vector_degraded_write", "detail": "vector_runtime_components_missing"}, nil
	}
	if targetStore.Contains(token) {
		return map[string]any{"success": true, "vector_written": true, "queued": false, "warning": "", "detail": "vector_already_exists"}, nil
	}
	restored, restoreErr := targetStore.Restore([]string{token})
	if restoreErr == nil && restored > 0 {
		if !targetStore.Contains(token) {
			restoreErr = fmt.Errorf("向量恢复后成员校验失败")
		} else {
			return map[string]any{"success": true, "vector_written": true, "queued": false, "warning": "", "detail": "vector_restored"}, nil
		}
	}
	if restoreErr != nil {
		errorText := restoreErr.Error()
		if !allowMetadataOnly {
			s.LoggerWarn("段落向量恢复失败: %s", errorText)
			return nil, restoreErr
		}
		s.enqueueBackfill(token, errorText)
		return map[string]any{"success": true, "vector_written": false, "queued": true, "warning": "vector_degraded_write", "detail": fmt.Sprintf("%s vector restore failed: %s", ingestFirstNonEmpty(contextText, "paragraph"), errorText)}, nil
	}
	if s.deps.EmbeddingEncode == nil {
		if !allowMetadataOnly {
			return nil, fmt.Errorf("embedding 依赖未初始化")
		}
		s.enqueueBackfill(token, "embedding_runtime_component_missing")
		return map[string]any{"success": true, "vector_written": false, "queued": true, "warning": "vector_degraded_write", "detail": "embedding_runtime_component_missing"}, nil
	}
	if s.deps.IsEmbeddingDegraded != nil && s.deps.IsEmbeddingDegraded() {
		if !allowMetadataOnly {
			return nil, fmt.Errorf("embedding 处于降级态，metadata-only 写入已禁用")
		}
		s.enqueueBackfill(token, "embedding_degraded")
		return map[string]any{"success": true, "vector_written": false, "queued": true, "warning": "vector_degraded_write", "detail": "embedding_degraded"}, nil
	}
	vectors, err := s.deps.EmbeddingEncode(ctx, []string{text})
	if err == nil {
		if _, addErr := targetStore.Add(vectors, []string{token}); addErr != nil {
			err = addErr
		}
	}
	if err == nil {
		if s.deps.RecordVectorInput != nil {
			s.deps.RecordVectorInput("paragraph", token, text)
		}
		if !targetStore.Contains(token) {
			err = fmt.Errorf("段落向量写入后成员校验失败")
		}
	}
	if err == nil {
		return map[string]any{"success": true, "vector_written": true, "queued": false, "warning": "", "detail": ""}, nil
	}
	errorText := err.Error()
	if s.deps.EmbeddingFallbackEnabled != nil && s.deps.EmbeddingFallbackEnabled() && s.deps.SetEmbeddingDegraded != nil {
		now := float64(time.Now().UnixNano()) / 1e9
		reason := errorText
		if len([]rune(reason)) > 500 {
			reason = string([]rune(reason)[:500])
		}
		s.deps.SetEmbeddingDegraded(true, reason, &now)
	}
	if !allowMetadataOnly {
		return nil, err
	}
	s.enqueueBackfill(token, errorText)
	return map[string]any{"success": true, "vector_written": false, "queued": true, "warning": "vector_degraded_write", "detail": fmt.Sprintf("%s vector write failed: %s", ingestFirstNonEmpty(contextText, "paragraph"), errorText)}, nil
}

// EnsureVectorForText 对齐 _ensure_vector_for_text
func (s *IngestService) EnsureVectorForText(ctx context.Context, params map[string]any) (bool, error) {
	var targetStore *storage.VectorStore
	if raw, ok := params["vector_store"].(*storage.VectorStore); ok {
		targetStore = raw
	} else if s.deps.VectorStore != nil {
		targetStore = s.deps.VectorStore()
	}
	if targetStore == nil {
		return false, nil
	}
	token := strings.TrimSpace(asString(params["item_hash"]))
	content := strings.TrimSpace(asString(params["text"]))
	if token == "" || content == "" {
		return false, nil
	}
	if targetStore.Contains(token) {
		return true, nil
	}
	var before func()
	switch raw := params["before_vector_write"].(type) {
	case func():
		before = raw
	case func() error:
		before = func() { _ = raw() }
	}
	runBefore := func() {
		if before != nil {
			before()
		}
	}
	tombstoned, err := targetStore.IsTombstoned(token)
	if err != nil {
		s.LoggerWarn("检查待恢复向量状态失败: %v", err)
		return false, nil
	}
	if tombstoned {
		runBefore()
		restored, restoreErr := targetStore.Restore([]string{token})
		if restoreErr != nil || restored != 1 || !targetStore.Contains(token) {
			if restoreErr != nil {
				s.LoggerWarn("重建向量失败: %v", restoreErr)
			} else {
				s.LoggerWarn("重建向量失败: 向量恢复后成员校验失败")
			}
			return false, nil
		}
		return true, nil
	}
	if s.deps.EmbeddingEncode == nil {
		return false, nil
	}
	vectors, err := s.deps.EmbeddingEncode(ctx, []string{content})
	if err != nil || len(vectors) == 0 || len(vectors[0]) == 0 {
		if err != nil {
			s.LoggerWarn("生成待恢复向量失败: %v", err)
		}
		return false, nil
	}
	runBefore()
	if _, err := targetStore.Add(vectors, []string{token}); err != nil {
		s.LoggerWarn("重建向量失败: %v", err)
		return false, nil
	}
	if !targetStore.Contains(token) {
		s.LoggerWarn("重建向量失败: 向量写入后成员校验失败")
		return false, nil
	}
	return true, nil
}

// EnsureRelationVector 对齐 _ensure_relation_vector
func (s *IngestService) EnsureRelationVector(ctx context.Context, payload map[string]any) (bool, error) {
	if !s.relationVectorsEnabled() {
		return false, nil
	}
	hashValue := asString(payload["hash"])
	subject := strings.TrimSpace(asString(payload["subject"]))
	predicate := strings.TrimSpace(asString(payload["predicate"]))
	object := strings.TrimSpace(asString(payload["object"]))
	typedID := s.deps.DualVectorPoolsEnabled != nil && s.deps.DualVectorPoolsEnabled(ctx)
	var before func()
	if raw, ok := payload["before_vector_write"].(func()); ok {
		before = raw
	}
	if s.deps.EnsureRelationVector != nil {
		written, already, err := s.deps.EnsureRelationVector(ctx, hashValue, subject, predicate, object, typedID, before)
		return written || already, err
	}
	text := fmt.Sprintf("%s %s %s", subject, predicate, object)
	if s.deps.BuildRelationVectorText != nil {
		text = s.deps.BuildRelationVectorText(subject, predicate, object)
	}
	params := map[string]any{"item_hash": hashValue, "text": text, "vector_store": s.deps.VectorStore()}
	if before != nil {
		params["before_vector_write"] = before
	}
	return s.EnsureVectorForText(ctx, params)
}

// EnsureParagraphVector 对齐 _ensure_paragraph_vector
func (s *IngestService) EnsureParagraphVector(ctx context.Context, payload map[string]any) (bool, error) {
	vectorStore := s.paragraphStore()
	return s.EnsureVectorForText(ctx, map[string]any{
		"item_hash": asString(payload["hash"]), "text": asString(payload["content"]), "vector_store": vectorStore,
	})
}

// EnsureEntityVector 对齐 _ensure_entity_vector
func (s *IngestService) EnsureEntityVector(ctx context.Context, payload map[string]any) (bool, error) {
	hashValue := asString(payload["hash"])
	name := asString(payload["name"])
	if s.deps.DualVectorPoolsEnabled != nil && s.deps.DualVectorPoolsEnabled(ctx) {
		itemHash := hashValue
		if s.deps.GraphVectorID != nil {
			itemHash = s.deps.GraphVectorID("entity", hashValue)
		}
		var vectorStore *storage.VectorStore
		if s.deps.GraphVectorStore != nil {
			vectorStore = s.deps.GraphVectorStore()
		}
		return s.EnsureVectorForText(ctx, map[string]any{"item_hash": itemHash, "text": name, "vector_store": vectorStore})
	}
	return s.EnsureVectorForText(ctx, map[string]any{"item_hash": hashValue, "text": name, "vector_store": s.deps.VectorStore()})
}

// RestoreRelationHashes 为给定关系补齐向量
func (s *IngestService) RestoreRelationHashes(ctx context.Context, payload map[string]any) (map[string]any, error) {
	store := s.store()
	if store == nil {
		return nil, fmt.Errorf("元数据存储未初始化")
	}
	hashes := stringTokens(payload["hashes"])
	restored := make([]string, 0)
	skipped := make([]string, 0)
	for _, hashValue := range hashes {
		relation, err := store.GetRelation(hashValue, true)
		if err != nil {
			return nil, err
		}
		if relation == nil {
			skipped = append(skipped, hashValue)
			continue
		}
		ok, err := s.EnsureRelationVector(ctx, map[string]any{
			"hash": hashValue, "subject": relation["subject"], "predicate": relation["predicate"], "object": relation["object"],
		})
		if err != nil {
			return nil, err
		}
		if ok {
			restored = append(restored, hashValue)
		} else {
			skipped = append(skipped, hashValue)
		}
	}
	return map[string]any{"success": true, "restored": restored, "skipped": skipped}, nil
}

// ProcessEpisodeSourceRebuildBatch 对齐 process_episode_source_rebuild_batch
func (s *IngestService) ProcessEpisodeSourceRebuildBatch(ctx context.Context, params map[string]any) (map[string]any, error) {
	sourcesValue, hasSources := params["sources"]
	maxRetry := intOr(params["max_retry"], 3, math.MinInt)
	if maxRetry < 1 {
		return nil, fmt.Errorf("max_retry 必须至少为1")
	}
	limit := intOr(params["limit"], 20, 1)
	// 缺省值对齐 Python：lease_seconds=1800.0、max_wait_seconds=60.0
	leaseSeconds := mathMax(1.0, spaceFloatOrDefault(params["lease_seconds"], 1800.0))
	maxWaitSeconds := mathMax(0.0, spaceFloatOrDefault(params["max_wait_seconds"], 60.0))
	if err := s.initialize(ctx); err != nil {
		return nil, err
	}
	store := s.store()
	if store == nil {
		return nil, fmt.Errorf("元数据存储未初始化")
	}
	generation := any(nil)
	if s.deps.GenerationSignature != nil {
		generation = s.deps.GenerationSignature()
	}
	generationHash := ""
	if s.deps.GenerationHash != nil {
		generationHash = s.deps.GenerationHash(generation)
	}
	sourceScope := make([]string, 0)
	if hasSources {
		for _, item := range stringTokens(sourcesValue) {
			if !containsValue(sourceScope, item) {
				sourceScope = append(sourceScope, item)
			}
		}
		if len(sourceScope) == 0 {
			return emptyRebuildBatchResult(), nil
		}
	}
	result := emptyRebuildBatchResult()
	rebuiltItems := make([]map[string]any, 0)
	failures := make([]map[string]any, 0)
	unfinished := make([]map[string]any, 0)
	superseded := 0
	fallbackCount := 0
	episodeCount := 0
	for round := 0; round < limit; round++ {
		claims, err := store.ClaimEpisodeSourceRebuildBatch(storage.EpisodeClaimOptions{
			GenerationHash: generationHash, Sources: sourcesOrNil(hasSources, sourceScope),
			Limit: 1, MaxRetry: maxRetry, LeaseSeconds: leaseSeconds, MaxWaitSeconds: maxWaitSeconds,
		}, nil)
		if err != nil {
			return nil, err
		}
		if len(claims) == 0 {
			break
		}
		claim := claims[0]
		source := asString(claim["source"])
		if hasSources {
			sourceScope = removeValue(sourceScope, source)
		}
		leaseToken := asString(claim["lease_token"])
		claimedRevision := intOr(claim["claimed_revision"], 0, math.MinInt)
		plan := map[string]any{}
		sourceType := strings.SplitN(source, ":", 2)[0]
		shouldPlan := s.deps.ShouldAutoEnqueueEpisode == nil || s.deps.ShouldAutoEnqueueEpisode(sourceType)
		markEpisodeFail := func(err error) {
			errorText := truncateRunes(err.Error(), 500)
			retryCount := intOr(claim["retry_count"], 0, math.MinInt)
			backoff := mathMin(300.0, 5.0*math.Pow(2, float64(retryCount)))
			if _, failErr := store.FailEpisodeSourceRebuild(storage.EpisodeFailOptions{
				Source: source, LeaseToken: leaseToken, ClaimedRevision: claimedRevision,
				Error: errorText, RetryBackoffSeconds: backoff,
			}, nil); failErr != nil {
				s.LoggerWarn("Episode 来源失败状态回写异常: source=%s, error=%v", source, failErr)
			}
			failures = append(failures, map[string]any{"source": source, "error": errorText})
		}
		if shouldPlan && s.deps.PlanSourceRebuild != nil {
			planValue, planErr := s.planSourceRebuildWithLeaseHeartbeat(
				ctx, store, source, leaseToken, claimedRevision, generationHash, generation, leaseSeconds,
			)
			if planErr != nil {
				markEpisodeFail(planErr)
				continue
			}
			plan = planValue
		} else {
			plan = map[string]any{
				"source": source, "payloads": []any{}, "episode_count": 0, "fallback_count": 0,
				"group_count": 0, "paragraph_count": 0, "reused_group_count": 0, "reused_episode_count": 0,
				"recomputed_group_count": 0, "generation_hash": generationHash,
			}
		}
		publishResult, err := store.PublishEpisodeSourceRebuild(storage.EpisodePublishOptions{
			Source: source, LeaseToken: leaseToken, ClaimedRevision: claimedRevision, GenerationHash: generationHash,
			EpisodesPayloads: asMapList(plan["payloads"]), EmptyGroupFingerprints: stringTokens(plan["empty_group_fingerprints"]),
		}, nil)
		if err != nil {
			markEpisodeFail(err)
			continue
		}
		if !pyTruthy(publishResult["published"]) {
			if pyTruthy(publishResult["superseded"]) {
				superseded++
				unfinished = append(unfinished, map[string]any{"source": source, "reason": "superseded"})
			} else {
				unfinished = append(unfinished, map[string]any{"source": source, "reason": "lease_lost_or_claim_mismatch"})
			}
			continue
		}
		item := map[string]any{}
		for key, value := range plan {
			if key == "payloads" {
				continue
			}
			item[key] = value
		}
		for key, value := range publishResult {
			item[key] = value
		}
		rebuiltItems = append(rebuiltItems, item)
		episodeCount += intOr(publishResult["episode_count"], 0, math.MinInt)
		fallbackCount += intOr(plan["fallback_count"], 0, math.MinInt)
	}
	if hasSources {
		for _, source := range sourceScope {
			unfinished = append(unfinished, map[string]any{"source": source, "reason": "not_claimed"})
		}
	}
	if len(rebuiltItems) > 0 {
		s.persist()
	}
	result["processed"] = len(rebuiltItems) + len(failures) + len(unfinished)
	result["rebuilt"] = len(rebuiltItems)
	result["superseded"] = superseded
	result["unfinished"] = len(unfinished)
	result["failed"] = len(failures)
	result["episode_count"] = episodeCount
	result["fallback_count"] = fallbackCount
	result["items"] = rebuiltItems
	result["failures"] = failures
	result["unfinished_items"] = unfinished
	return result, nil
}

func emptyRebuildBatchResult() map[string]any {
	return map[string]any{
		"processed": 0, "rebuilt": 0, "superseded": 0, "failed": 0, "episode_count": 0,
		"fallback_count": 0, "items": []map[string]any{}, "failures": []map[string]any{},
		"unfinished": 0, "unfinished_items": []map[string]any{},
	}
}

// planSourceRebuildWithLeaseHeartbeat 对齐 _maintain_episode_source_lease：规划期间按 lease/3 续租
func (s *IngestService) planSourceRebuildWithLeaseHeartbeat(
	ctx context.Context,
	store *storage.Store,
	source string,
	leaseToken string,
	claimedRevision int,
	generationHash string,
	generation any,
	leaseSeconds float64,
) (map[string]any, error) {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		interval := time.Duration(math.Max(0.1, leaseSeconds/3.0) * float64(time.Second))
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				renewed, err := store.RenewEpisodeSourceRebuildLease(storage.EpisodeLeaseRenewOptions{
					Source: source, LeaseToken: leaseToken, ClaimedRevision: claimedRevision,
					GenerationHash: generationHash, LeaseSeconds: leaseSeconds,
				}, nil)
				if err != nil {
					s.LoggerWarn("Episode 来源租约心跳异常: source=%s, error=%v", source, err)
					return
				}
				if !renewed {
					return
				}
			}
		}
	}()
	plan, err := s.deps.PlanSourceRebuild(ctx, source, generation)
	close(stop)
	<-done
	return plan, err
}

func (s *IngestService) allowMetadataOnly() bool {
	return s.deps.AllowMetadataOnlyWrite != nil && s.deps.AllowMetadataOnlyWrite()
}

func (s *IngestService) relationVectorsEnabled() bool {
	return s.deps.RelationVectorsEnabled != nil && s.deps.RelationVectorsEnabled()
}

func (s *IngestService) paragraphStore() *storage.VectorStore {
	if s.deps.ParagraphStore != nil {
		return s.deps.ParagraphStore()
	}
	return nil
}

func (s *IngestService) enqueueBackfill(token string, errorText string) {
	if s.deps.EnqueueParagraphVectorBackfill != nil {
		s.deps.EnqueueParagraphVectorBackfill(token, errorText)
	}
}

func resolveKnowledgeType(sourceType string) string {
	switch strings.ToLower(strings.TrimSpace(sourceType)) {
	case "person_fact":
		return "factual"
	case "chat_summary":
		return "narrative"
	default:
		return "mixed"
	}
}

func stringTokens(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil
		}
		return []string{trimmed}
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(asString(item)); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	default:
		if trimmed := strings.TrimSpace(asString(value)); trimmed != "" {
			return []string{trimmed}
		}
		return nil
	}
}

func ingestFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func floatPtr(value float64) *float64 {
	return &value
}

func sourcesOrNil(hasSources bool, sources []string) []string {
	if !hasSources {
		return nil
	}
	return sources
}

func containsValue(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func removeValue(values []string, target string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != target {
			out = append(out, value)
		}
	}
	return out
}

func mathMin(a float64, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

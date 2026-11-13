package native

// 人物画像存储与召回适配器
// storage.Store 的结构体/字典返回形状与 utils.PersonProfileStore 不同，转换集中在此

import (
	"context"
	"fmt"

	"maibot-go/internal/A_memorix/core/retrieval"
	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

// personProfileStoreAdapter 把 storage.Store 适配为画像存储接口
type personProfileStoreAdapter struct {
	store *storage.Store
}

var (
	_ utils.PersonProfileStore         = personProfileStoreAdapter{}
	_ utils.PersonProfileFactSupport   = personProfileStoreAdapter{}
	_ utils.PersonProfileFactProjector = personProfileStoreAdapter{}
)

func (a personProfileStoreAdapter) requireStore() (*storage.Store, error) {
	if a.store == nil {
		return nil, fmt.Errorf("元数据存储未初始化")
	}
	return a.store, nil
}

func (a personProfileStoreAdapter) GetPersonIdentity(personID string) (map[string]any, error) {
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	identity, err := store.GetPersonIdentity(personID)
	if err != nil || identity == nil {
		return nil, err
	}
	return identity.ToMap(), nil
}

func (a personProfileStoreAdapter) FindPersonIdentities(keyword string) ([]map[string]any, error) {
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	return store.FindPersonIdentities(keyword, "")
}

func (a personProfileStoreAdapter) GetPersonProfileAliasOverride(personID string) (map[string]any, error) {
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	return store.GetPersonProfileAliasOverride(personID)
}

func (a personProfileStoreAdapter) GetPersonProfileOverride(personID string) (map[string]any, error) {
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	return store.GetPersonProfileOverride(personID)
}

func (a personProfileStoreAdapter) GetParagraphsByEntity(entityName string) ([]map[string]any, error) {
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	return store.GetParagraphsByEntity(entityName)
}

func (a personProfileStoreAdapter) GetParagraphEntities(paragraphHash string) ([]map[string]any, error) {
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	return store.GetParagraphEntities(paragraphHash)
}

func (a personProfileStoreAdapter) GetRelations(subject string, object string, includeInactive bool) ([]map[string]any, error) {
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	return store.GetRelations(subject, "", object, includeInactive)
}

func (a personProfileStoreAdapter) GetParagraphsByRelationHashes(hashes []string) (map[string][]map[string]any, error) {
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	return store.GetParagraphsByRelationHashes(hashes)
}

func (a personProfileStoreAdapter) GetParagraph(hashValue string) (map[string]any, error) {
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	return store.GetParagraph(hashValue)
}

func (a personProfileStoreAdapter) GetParagraphsBySource(source string) ([]map[string]any, error) {
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	return store.GetParagraphsBySource(source)
}

func (a personProfileStoreAdapter) SearchParagraphsByContent(contentQuery string) ([]map[string]any, error) {
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	return store.SearchParagraphsByContent(contentQuery)
}

func (a personProfileStoreAdapter) ListPersonProfileFactClaims(personID string, effectiveAt *float64, limit int) ([]map[string]any, error) {
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	return store.ListPersonProfileFactClaims(personID, effectiveAt, limit)
}

func (a personProfileStoreAdapter) GetParagraphStaleRelationMarksBatch(paragraphHashes []string) (map[string][]map[string]any, error) {
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	return store.GetParagraphStaleRelationMarksBatch(paragraphHashes, nil)
}

func (a personProfileStoreAdapter) GetRelationStatusBatch(hashes []string) (map[string]map[string]any, error) {
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	return store.GetRelationStatusBatch(hashes)
}

func (a personProfileStoreAdapter) GetLatestPersonProfileSnapshot(personID string) (map[string]any, error) {
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	return store.GetLatestPersonProfileSnapshot(personID)
}

func (a personProfileStoreAdapter) UpsertPersonProfileSnapshot(input utils.PersonProfileSnapshotWrite) (map[string]any, error) {
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	return store.UpsertPersonProfileSnapshot(storage.PersonProfileSnapshotInput{
		PersonID:            input.PersonID,
		ProfileText:         input.ProfileText,
		Aliases:             input.Aliases,
		RelationEdges:       input.RelationEdges,
		VectorEvidence:      input.VectorEvidence,
		EvidenceIDs:         input.EvidenceIDs,
		FactClaimIDs:        input.FactClaimIDs,
		EvidenceFingerprint: input.EvidenceFingerprint,
		ExpiresAt:           input.ExpiresAt,
		SourceNote:          input.SourceNote,
	})
}

func (a personProfileStoreAdapter) RefreshPersonProfileSnapshotCache(snapshotID int64, expiresAt *float64, sourceNote string) (map[string]any, error) {
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	return store.RefreshPersonProfileSnapshotCache(snapshotID, expiresAt, sourceNote, nil)
}

// FactRelationHasSupport 委托元数据存储做事实关系支撑校验
func (a personProfileStoreAdapter) FactRelationHasSupport(relationHash string, paragraph map[string]any) (bool, error) {
	store, err := a.requireStore()
	if err != nil {
		return false, err
	}
	return store.FactRelationHasSupport(relationHash, paragraph)
}

// ProjectFactParagraph 委托元数据存储投影事实段落，无有效事实时返回 nil
func (a personProfileStoreAdapter) ProjectFactParagraph(paragraph map[string]any) (map[string]any, error) {
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	return store.ProjectFactParagraph(paragraph)
}

// profileRetrieverAdapter 把双路检索器适配为画像证据召回
type profileRetrieverAdapter struct {
	runtime *Runtime
}

var _ utils.PersonProfileRetriever = profileRetrieverAdapter{}

func (a profileRetrieverAdapter) RetrievePersonProfileEvidence(ctx context.Context, query string, topK int) ([]utils.PersonProfileRetrievedItem, error) {
	if a.runtime == nil || a.runtime.retriever == nil {
		return nil, fmt.Errorf("知识检索器未初始化")
	}
	results, err := a.runtime.retriever.Retrieve(ctx, query, &retrieval.RetrieveOptions{TopK: &topK})
	if err != nil {
		return nil, err
	}
	out := make([]utils.PersonProfileRetrievedItem, 0, len(results))
	for _, item := range results {
		metadata := item.Metadata
		if metadata == nil {
			metadata = map[string]any{}
		}
		out = append(out, utils.PersonProfileRetrievedItem{
			Hash: item.HashValue, ResultType: item.ResultType, Score: item.Score,
			Content: item.Content, Metadata: metadata,
		})
	}
	return out, nil
}

package storage

// 事实段落投影与关系支撑，供检索命中处理复用
// 对齐 Python metadata_fact.project_fact_paragraph / fact_relation_has_support

import "strings"

// ProjectFactParagraph 投影去重管理段落中的有效事实内容
// 返回 nil 表示该段落当前没有有效事实，应被丢弃
func (s *Store) ProjectFactParagraph(paragraph map[string]any) (map[string]any, error) {
	conn, err := s.ResolveConn(nil)
	if err != nil {
		return nil, err
	}
	projected, keep, err := s.episodeProjectFactParagraph(conn, paragraph)
	if err != nil {
		return nil, err
	}
	if !keep {
		return nil, nil
	}
	return projected, nil
}

// FactRelationHasSupport 判断关系是否仍由段落中的有效事实支撑
func (s *Store) FactRelationHasSupport(relationHash string, paragraph map[string]any) (bool, error) {
	projected, err := s.ProjectFactParagraph(paragraph)
	if err != nil {
		return false, err
	}
	if projected == nil {
		return false, nil
	}
	metadata, _ := paragraph["metadata"].(map[string]any)
	if !episodeTruthy(metadata["dedup_managed"]) {
		return true, nil
	}
	relationClaims, _ := metadata["dedup_relation_claims"].(map[string]any)
	active := episodePyStrList(projected["active_fact_claim_ids"])
	activeSet := make(map[string]struct{}, len(active))
	for _, item := range active {
		activeSet[item] = struct{}{}
	}
	for _, item := range episodePyStrList(relationClaims[strings.TrimSpace(relationHash)]) {
		if _, ok := activeSet[item]; ok {
			return true, nil
		}
	}
	return false, nil
}

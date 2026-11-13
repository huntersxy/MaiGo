package native

// 检索命中的事实投影与关系支撑端口，直接复用存储层公开语义

import "errors"

// projectFactParagraph 调用存储层事实段落投影
func (r *Runtime) projectFactParagraph(paragraph map[string]any) (map[string]any, error) {
	if r.metadataStore == nil {
		return nil, errors.New("metadata store unavailable")
	}
	return r.metadataStore.ProjectFactParagraph(paragraph)
}

// factRelationHasSupport 调用存储层关系支撑判定
func (r *Runtime) factRelationHasSupport(relationHash string, paragraph map[string]any) (bool, error) {
	if r.metadataStore == nil {
		return false, errors.New("metadata store unavailable")
	}
	return r.metadataStore.FactRelationHasSupport(relationHash, paragraph)
}

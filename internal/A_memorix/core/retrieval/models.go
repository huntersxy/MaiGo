package retrieval

import "fmt"

// A_memorix 检索公共数据模型，对齐 core/retrieval/dual_path.py 的数据类
// dual_path 的检索器本体在后续文件中实现，复用这里的类型

// RetrievalStrategy 检索策略
type RetrievalStrategy string

const (
	// StrategyParagraphOnly 仅段落检索
	StrategyParagraphOnly RetrievalStrategy = "paragraph_only"
	// StrategyRelationOnly 仅关系检索
	StrategyRelationOnly RetrievalStrategy = "relation_only"
	// StrategyDualPath 双路检索（推荐）
	StrategyDualPath RetrievalStrategy = "dual_path"
)

// RetrievalResult 检索结果，字段与 Python dataclass 一致
type RetrievalResult struct {
	HashValue  string         `json:"hash"`
	Content    string         `json:"content"`
	Score      float64        `json:"score"`
	ResultType string         `json:"type"`
	Source     string         `json:"source"`
	Metadata   map[string]any `json:"metadata"`
}

// ToDict 对齐 RetrievalResult.to_dict；键名与 Python 保持一致
func (r RetrievalResult) ToDict() map[string]any {
	return map[string]any{
		"hash":     r.HashValue,
		"content":  r.Content,
		"score":    r.Score,
		"type":     r.ResultType,
		"source":   r.Source,
		"metadata": r.Metadata,
	}
}

// Clone 返回深拷贝足够的副本：metadata 顶层浅拷贝，嵌套结构按需共享
func (r RetrievalResult) Clone() RetrievalResult {
	cloned := r
	if r.Metadata != nil {
		cloned.Metadata = make(map[string]any, len(r.Metadata))
		for key, value := range r.Metadata {
			cloned.Metadata[key] = value
		}
	}
	return cloned
}

// RetrievalScope 一次检索已解析出的可见资源集合
type RetrievalScope struct {
	Key          string
	ParagraphIDs map[string]struct{}
	RelationIDs  map[string]struct{}
	EntityIDs    map[string]struct{}
	EpisodeIDs   map[string]struct{}
}

// Empty 对应 Python 的 empty 属性
// 注意与 Python 一致：entity_ids 不参与空判定
func (s RetrievalScope) Empty() bool {
	return len(s.ParagraphIDs) == 0 && len(s.RelationIDs) == 0 && len(s.EpisodeIDs) == 0
}

// ParseRetrievalStrategy 把配置字符串解析为策略，非法值报错
func ParseRetrievalStrategy(value string) (RetrievalStrategy, error) {
	switch RetrievalStrategy(value) {
	case StrategyParagraphOnly, StrategyRelationOnly, StrategyDualPath:
		return RetrievalStrategy(value), nil
	}
	return "", fmt.Errorf("不支持的检索策略: %s", value)
}

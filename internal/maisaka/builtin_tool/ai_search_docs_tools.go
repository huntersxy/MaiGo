package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

// SearchOfficialDocsInput 是 search_official_docs 的工具入参
type SearchOfficialDocsInput struct {
	Query string `json:"query" jsonschema:"description=一个或多个简短检索词，用空格分隔"`
	Limit int    `json:"limit,omitempty" jsonschema:"description=返回数量，范围 1 到 8"`
}

// SearchOfficialDocsOutput 是 search_official_docs 的工具结果
type SearchOfficialDocsOutput struct {
	Query     string                    `json:"query"`
	Documents []OfficialDocSearchResult `json:"documents"`
}

// ReadOfficialDocsInput 是 read_official_docs 的工具入参
type ReadOfficialDocsInput struct {
	Paths  []string `json:"paths" jsonschema:"description=search_official_docs 返回的文档路径，最多 4 个"`
	Offset int      `json:"offset,omitempty" jsonschema:"description=开始读取的字符位置，默认 0；可填上次返回的 next_offset 或 snippet_offset"`
}

// ReadOfficialDocsOutput 是 read_official_docs 的工具结果
type ReadOfficialDocsOutput struct {
	Documents []OfficialDocReadResult `json:"documents"`
}

// NewSearchOfficialDocsTool 构造 search_official_docs 工具；store 为 nil 时使用进程级默认仓库
func NewSearchOfficialDocsTool(store *AISearchDocumentStore) (tool.InvokableTool, error) {
	if store == nil {
		store = DefaultAISearchDocumentStore()
	}
	return utils.InferTool(
		"search_official_docs",
		"搜索 docs.mai-mai.org 上的 MaiBot 官方文档，返回文档路径、标题和相关片段。",
		func(ctx context.Context, input *SearchOfficialDocsInput) (*SearchOfficialDocsOutput, error) {
			if input == nil {
				return nil, fmt.Errorf("检索词不能为空")
			}
			query := resolveOfficialDocsToolQuery(input.Query)
			limit := resolveOfficialDocsToolLimit(input.Limit, officialDocsSearchMaxLimit)
			documents, err := store.SearchOfficialDocs(ctx, query, limit)
			if err != nil {
				return nil, fmt.Errorf("暂时无法读取官方文档: %w", err)
			}
			return &SearchOfficialDocsOutput{Query: query, Documents: documents}, nil
		},
	)
}

// NewReadOfficialDocsTool 构造 read_official_docs 工具；store 为 nil 时使用进程级默认仓库
func NewReadOfficialDocsTool(store *AISearchDocumentStore) (tool.InvokableTool, error) {
	if store == nil {
		store = DefaultAISearchDocumentStore()
	}
	return utils.InferTool(
		"read_official_docs",
		"按路径读取 docs.mai-mai.org 官方文档正文。回答文档问题前应先调用此工具。单次返回的正文有长度上限，结果带有 next_offset 时说明文档还有后续内容。",
		func(ctx context.Context, input *ReadOfficialDocsInput) (*ReadOfficialDocsOutput, error) {
			if input == nil {
				return &ReadOfficialDocsOutput{Documents: []OfficialDocReadResult{}}, nil
			}
			documents, err := store.ReadOfficialDocs(ctx, input.Paths, input.Offset)
			if err != nil {
				return nil, fmt.Errorf("暂时无法读取官方文档: %w", err)
			}
			if documents == nil {
				documents = []OfficialDocReadResult{}
			}
			return &ReadOfficialDocsOutput{Documents: documents}, nil
		},
	)
}

func resolveOfficialDocsToolQuery(raw string) string {
	query := strings.TrimSpace(raw)
	runes := []rune(query)
	if len(runes) > aiSearchMaxToolQueryLength {
		query = string(runes[:aiSearchMaxToolQueryLength])
	}
	return query
}

func resolveOfficialDocsToolLimit(raw, maximum int) int {
	limit := raw
	if limit == 0 {
		limit = aiSearchDefaultToolLimit
	}
	if limit < 1 {
		return 1
	}
	if limit > maximum {
		return maximum
	}
	return limit
}

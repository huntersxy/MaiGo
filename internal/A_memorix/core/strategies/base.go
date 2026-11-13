package strategies

// A_memorix 知识切分策略基类，对齐 Python core/strategies/base.py

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"unicode"
	"unicode/utf8"
)

// KnowledgeType 知识类型，对齐 Python KnowledgeType(str, Enum)
type KnowledgeType string

const (
	// KnowledgeTypeNarrative 叙述类知识
	KnowledgeTypeNarrative KnowledgeType = "narrative"
	// KnowledgeTypeFactual 事实类知识
	KnowledgeTypeFactual KnowledgeType = "factual"
	// KnowledgeTypeQuote 语录类知识
	KnowledgeTypeQuote KnowledgeType = "quote"
	// KnowledgeTypeMixed 混合类知识
	KnowledgeTypeMixed KnowledgeType = "mixed"
)

// Value 对齐 Enum.value
func (k KnowledgeType) Value() string {
	return string(k)
}

// String 对齐 Python str(KnowledgeType.XXX) 之外的常规输出
func (k KnowledgeType) String() string {
	return string(k)
}

// SourceInfo 块来源信息，对齐 Python SourceInfo dataclass
type SourceInfo struct {
	File        string `json:"file"`
	OffsetStart int    `json:"offset_start"`
	OffsetEnd   int    `json:"offset_end"`
	Checksum    string `json:"checksum"`
}

// ChunkContext 块上下文，对齐 Python ChunkContext dataclass
type ChunkContext struct {
	ChunkID string         `json:"chunk_id"`
	Index   int            `json:"index"`
	Context map[string]any `json:"context"`
	Text    string         `json:"text"`
}

// ChunkFlags 块标记，对齐 Python ChunkFlags dataclass
// 注意 Go 零值 RequiresLLM=false 与 Python 默认值 True 不同，构造块请用 DefaultChunkFlags
type ChunkFlags struct {
	Verbatim    bool `json:"verbatim"`
	RequiresLLM bool `json:"requires_llm"`
}

// DefaultChunkFlags 返回 Python dataclass 的默认值，requires_llm 默认 true
func DefaultChunkFlags() ChunkFlags {
	return ChunkFlags{RequiresLLM: true}
}

// ProcessedChunk 切分后的知识块，对齐 Python ProcessedChunk dataclass
type ProcessedChunk struct {
	Type   KnowledgeType  `json:"type"`
	Source SourceInfo     `json:"source"`
	Chunk  ChunkContext   `json:"chunk"`
	Data   map[string]any `json:"data"`
	Flags  ChunkFlags     `json:"flags"`
}

// NewProcessedChunk 构造带 Python 默认值的块：data 为空字典、requires_llm 为 true
func NewProcessedChunk(knowledgeType KnowledgeType, source SourceInfo, chunk ChunkContext) ProcessedChunk {
	return ProcessedChunk{
		Type:   knowledgeType,
		Source: source,
		Chunk:  chunk,
		Data:   map[string]any{},
		Flags:  DefaultChunkFlags(),
	}
}

// ToDict 对齐 ProcessedChunk.to_dict，键名与嵌套顺序保持一致
func (c ProcessedChunk) ToDict() map[string]any {
	contextPayload := c.Chunk.Context
	if contextPayload == nil {
		contextPayload = map[string]any{}
	}
	dataPayload := c.Data
	if dataPayload == nil {
		dataPayload = map[string]any{}
	}
	return map[string]any{
		"type": c.Type.Value(),
		"source": map[string]any{
			"file":         c.Source.File,
			"offset_start": c.Source.OffsetStart,
			"offset_end":   c.Source.OffsetEnd,
			"checksum":     c.Source.Checksum,
		},
		"chunk": map[string]any{
			"text":     c.Chunk.Text,
			"chunk_id": c.Chunk.ChunkID,
			"index":    c.Chunk.Index,
			"context":  contextPayload,
		},
		"data": dataPayload,
		"flags": map[string]any{
			"verbatim":     c.Flags.Verbatim,
			"requires_llm": c.Flags.RequiresLLM,
		},
	}
}

// LLMCompletionFunc 对齐 Python llm_func：接收提示词并返回结构化结果
type LLMCompletionFunc func(ctx context.Context, prompt string) (map[string]any, error)

// BaseStrategy 策略接口，对齐 Python BaseStrategy 抽象基类
type BaseStrategy interface {
	// Filename 返回构造时绑定的文件名
	Filename() string
	// Split 按策略将文本切分为块
	Split(text string) []ProcessedChunk
	// Extract 从文本块中抽取结构化信息
	Extract(ctx context.Context, chunk ProcessedChunk, llmFunc LLMCompletionFunc) (ProcessedChunk, error)
	// CalculateChecksum 计算文本 sha256 校验和
	CalculateChecksum(text string) string
	// BuildLanguageGuard 构建统一的输出语言约束
	BuildLanguageGuard(text string) string
}

// baseStrategy 承载 filename 与公共方法，对齐 Python BaseStrategy 的非抽象部分
type baseStrategy struct {
	filename string
}

// Filename 对齐 self.filename
func (b *baseStrategy) Filename() string {
	return b.filename
}

// CalculateChecksum 对齐 hashlib.sha256(text.encode("utf-8")).hexdigest()
func (b *baseStrategy) CalculateChecksum(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// BuildLanguageGuard 对齐 build_language_guard，不区分语言类型，要求保持原文语言
func (b *baseStrategy) BuildLanguageGuard(text string) string {
	_ = text // 预留参数，便于后续按需扩展
	return "Focus on the original source language. Keep extracted events, entities, predicates " +
		"and objects in the same language as the source text, preserve names/terms as-is, " +
		"and do not translate."
}

// strategyStrip 对齐 Python str.strip()，包含 0x1c-0x1f 控制符
func strategyStrip(text string) string {
	return trimPythonSpace(text)
}

// strategyIsSpace 对齐 Python str.isspace()
func strategyIsSpace(r rune) bool {
	if r >= 0x1c && r <= 0x1f {
		return true
	}
	return unicode.IsSpace(r)
}

func trimPythonSpace(text string) string {
	start := 0
	for start < len(text) {
		r, size := utf8.DecodeRuneInString(text[start:])
		if !strategyIsSpace(r) {
			break
		}
		start += size
	}
	end := len(text)
	for end > start {
		r, size := utf8.DecodeLastRuneInString(text[start:end])
		if !strategyIsSpace(r) {
			break
		}
		end -= size
	}
	return text[start:end]
}

// strategyRuneLen 对齐 Python len(str)，按码点计数
func strategyRuneLen(text string) int {
	count := 0
	for range text {
		count++
	}
	return count
}

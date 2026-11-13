package strategies

// A_memorix 语录策略，对齐 Python core/strategies/quote.py

import (
	"context"
	"strconv"
	"strings"
)

// QuoteStrategy 按连续两个换行切分语录段落
type QuoteStrategy struct {
	baseStrategy
}

var _ BaseStrategy = (*QuoteStrategy)(nil)

// NewQuoteStrategy 构造语录策略
func NewQuoteStrategy(filename string) *QuoteStrategy {
	return &QuoteStrategy{baseStrategy: baseStrategy{filename: filename}}
}

// Split 对齐 split，按段落切分并保留原始偏移
func (s *QuoteStrategy) Split(text string) []ProcessedChunk {
	stanzas := strings.Split(text, "\n\n")
	chunks := make([]ProcessedChunk, 0, len(stanzas))
	offset := 0

	for idx, stanza := range stanzas {
		if strategyStrip(stanza) == "" {
			offset += strategyRuneLen(stanza) + 2
			continue
		}

		chunk := NewProcessedChunk(
			KnowledgeTypeQuote,
			SourceInfo{
				File:        s.Filename(),
				OffsetStart: offset,
				OffsetEnd:   offset + strategyRuneLen(stanza),
				Checksum:    s.CalculateChecksum(stanza),
			},
			ChunkContext{
				ChunkID: s.Filename() + "_" + strconv.Itoa(idx),
				Index:   idx,
				Text:    stanza,
				Context: map[string]any{},
			},
		)
		// 语录文本本身即逐字实体，默认不调用 LLM
		chunk.Flags = ChunkFlags{Verbatim: true, RequiresLLM: false}
		chunks = append(chunks, chunk)

		offset += strategyRuneLen(stanza) + 2
	}

	return chunks
}

// Extract 对齐 extract，把整段文本作为逐字实体，可选 LLM 分支保持透传
func (s *QuoteStrategy) Extract(ctx context.Context, chunk ProcessedChunk, llmFunc LLMCompletionFunc) (ProcessedChunk, error) {
	_ = ctx
	chunk.Data = map[string]any{"verbatim_entities": []any{chunk.Chunk.Text}}
	return chunk, nil
}

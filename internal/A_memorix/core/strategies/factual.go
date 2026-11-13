package strategies

// A_memorix 事实策略，对齐 Python core/strategies/factual.py

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

const (
	// DefaultFactualTargetSize 事实块默认目标长度，对齐 DEFAULT_TARGET_SIZE
	DefaultFactualTargetSize = 1200
	// FactualMinTargetSize 事实块目标长度下限，对齐 max(200, ...)
	FactualMinTargetSize = 200
)

var (
	// factualListPattern 对齐 ^[\-\*]\s+
	factualListPattern = regexp.MustCompile("^[\\-\\*]\\s+")
	// factualOrderedPattern 对齐 ^\d+\.\s+
	factualOrderedPattern = regexp.MustCompile("^\\d+\\.\\s+")
	// factualDefinitionPattern 对齐 ^[^：:]+[：:].+
	factualDefinitionPattern = regexp.MustCompile("^[^：:]+[：:].+")
)

// FactualStrategy 结构感知地切分事实文本，避免在列表与定义中间断开
type FactualStrategy struct {
	baseStrategy
	// TargetSize 单块目标长度，按码点计
	TargetSize int
}

var _ BaseStrategy = (*FactualStrategy)(nil)

// NewFactualStrategy 使用默认目标长度构造事实策略
func NewFactualStrategy(filename string) *FactualStrategy {
	return NewFactualStrategyWithTarget(filename, DefaultFactualTargetSize)
}

// NewFactualStrategyWithTarget 对齐 __init__ 的参数校验
// targetSize 为 0 时取默认值，小于下限时按下限处理
func NewFactualStrategyWithTarget(filename string, targetSize int) *FactualStrategy {
	if targetSize == 0 {
		targetSize = DefaultFactualTargetSize
	}
	if targetSize < FactualMinTargetSize {
		targetSize = FactualMinTargetSize
	}
	return &FactualStrategy{
		baseStrategy: baseStrategy{filename: filename},
		TargetSize:   targetSize,
	}
}

// Split 对齐 split，按行累计长度并在非结构行处切分
func (s *FactualStrategy) Split(text string) []ProcessedChunk {
	lines := strings.Split(text, "\n")
	chunks := make([]ProcessedChunk, 0, len(lines)/4+1)
	currentLines := make([]string, 0, 8)
	currentLen := 0

	for _, line := range lines {
		// 当前行为列表项、定义项或表格行时尽量不切分
		isStructure := isFactualStructuralLine(line)

		currentLen += strategyRuneLen(line) + 1
		currentLines = append(currentLines, line)

		if currentLen >= s.TargetSize && !isStructure {
			chunks = append(chunks, s.createChunk(currentLines, len(chunks)))
			currentLines = nil
			currentLen = 0
		} else if currentLen >= s.TargetSize*2 {
			// 超长时强制切分
			chunks = append(chunks, s.createChunk(currentLines, len(chunks)))
			currentLines = nil
			currentLen = 0
		}
	}

	if len(currentLines) > 0 {
		chunks = append(chunks, s.createChunk(currentLines, len(chunks)))
	}
	return chunks
}

// isFactualStructuralLine 对齐 _is_structural_line
func isFactualStructuralLine(line string) bool {
	line = strategyStrip(line)
	if line == "" {
		return false
	}
	if matchAtStart(factualListPattern, line) || matchAtStart(factualOrderedPattern, line) {
		return true
	}
	if matchAtStart(factualDefinitionPattern, line) {
		return true
	}
	return strings.HasPrefix(line, "|") && strings.HasSuffix(line, "|")
}

// createChunk 对齐 _create_chunk，偏移固定为 0
func (s *FactualStrategy) createChunk(lines []string, index int) ProcessedChunk {
	text := strings.Join(lines, "\n")
	return NewProcessedChunk(
		KnowledgeTypeFactual,
		SourceInfo{
			File:        s.Filename(),
			OffsetStart: 0,
			OffsetEnd:   0,
			Checksum:    s.CalculateChecksum(text),
		},
		ChunkContext{
			ChunkID: fmt.Sprintf("%s_%d", s.Filename(), index),
			Index:   index,
			Text:    text,
			Context: map[string]any{},
		},
	)
}

// Extract 对齐 extract，提取事实三元组与实体
func (s *FactualStrategy) Extract(ctx context.Context, chunk ProcessedChunk, llmFunc LLMCompletionFunc) (ProcessedChunk, error) {
	if llmFunc == nil {
		return chunk, fmt.Errorf("LLM function required for Factual extraction")
	}

	languageGuard := s.BuildLanguageGuard(chunk.Chunk.Text)

	var builder strings.Builder
	builder.WriteString("You are a factual knowledge extraction engine.\n")
	builder.WriteString("Extract factual triples and entities from the text.\n")
	builder.WriteString("Preserve lists and definitions accurately.\n\n")
	builder.WriteString("Language constraints:\n- ")
	builder.WriteString(languageGuard)
	builder.WriteString("\n- Preserve original names and domain terms exactly when possible.")
	builder.WriteString("\n- JSON keys must stay exactly as: triples, entities, subject, predicate, object.")
	builder.WriteString("\n\nText:\n")
	builder.WriteString(chunk.Chunk.Text)
	builder.WriteString("\n\nReturn ONLY valid JSON:\n")
	builder.WriteString("{\n")
	builder.WriteString("  \"triples\": [\n")
	builder.WriteString("    {\"subject\": \"Entity\", \"predicate\": \"Relationship\", \"object\": \"Entity\"}\n")
	builder.WriteString("  ],\n")
	builder.WriteString("  \"entities\": [\"Entity1\", \"Entity2\"]\n")
	builder.WriteString("}\n")

	result, err := llmFunc(ctx, builder.String())
	if err != nil {
		return chunk, err
	}
	chunk.Data = result
	return chunk, nil
}

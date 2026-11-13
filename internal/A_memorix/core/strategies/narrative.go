package strategies

// A_memorix 叙述策略，对齐 Python core/strategies/narrative.py

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

const (
	// DefaultWindowSize 叙述块默认窗口大小，对齐 DEFAULT_WINDOW_SIZE
	DefaultWindowSize = 1600
	// DefaultOverlap 叙述块默认重叠长度，对齐 DEFAULT_OVERLAP
	DefaultOverlap = 400
)

// narrativeScenePattern 对齐 _split_into_scenes 的 scene_pattern_str
const narrativeScenePattern = "(?m)^(?:#{1,6}\\s+.*|Chapter\\s+\\d+|^\\*{3,}$|^={3,}$)"

var narrativeSceneRegexp = regexp.MustCompile(narrativeScenePattern)

// NarrativeStrategy 按场景与滑动窗口切分叙述文本
type NarrativeStrategy struct {
	baseStrategy
	WindowSize int
	Overlap    int
}

var _ BaseStrategy = (*NarrativeStrategy)(nil)

// NewNarrativeStrategy 使用默认窗口与重叠构造叙述策略
func NewNarrativeStrategy(filename string) *NarrativeStrategy {
	return NewNarrativeStrategyWithWindow(filename, DefaultWindowSize, DefaultOverlap)
}

// NewNarrativeStrategyWithWindow 对齐 __init__ 的参数校验
// windowSize 为 0 时取默认值，overlap 为 0 表示不重叠，负值按 0 处理
func NewNarrativeStrategyWithWindow(filename string, windowSize int, overlap int) *NarrativeStrategy {
	if windowSize == 0 {
		windowSize = DefaultWindowSize
	}
	if windowSize < 200 {
		windowSize = 200
	}
	if overlap < 0 {
		overlap = 0
	}
	maxOverlap := windowSize - 1
	if maxOverlap < 0 {
		maxOverlap = 0
	}
	if overlap > maxOverlap {
		overlap = maxOverlap
	}
	return &NarrativeStrategy{
		baseStrategy: baseStrategy{filename: filename},
		WindowSize:   windowSize,
		Overlap:      overlap,
	}
}

// Split 按场景切分后对每个场景做滑动窗口切块
func (s *NarrativeStrategy) Split(text string) []ProcessedChunk {
	scenes := s.splitIntoScenes(text)
	chunks := make([]ProcessedChunk, 0, len(scenes))
	for sceneIdx, scene := range scenes {
		chunks = append(chunks, s.slidingWindow(scene.text, scene.title, sceneIdx)...)
	}
	return chunks
}

// narrativeScene 一个场景的正文与标题
type narrativeScene struct {
	text  string
	title string
}

// splitIntoScenes 对齐 _split_into_scenes，按 markdown 标题或分隔符切分场景
func (s *NarrativeStrategy) splitIntoScenes(text string) []narrativeScene {
	parts := splitKeepDelimiters(narrativeSceneRegexp, text)
	scenes := make([]narrativeScene, 0, len(parts))
	currentTitle := "Start"
	currentContent := make([]string, 0, 1)

	if len(parts) > 0 && strategyStrip(parts[0]) == "" {
		parts = parts[1:]
	}

	for _, part := range parts {
		if matchAtStart(narrativeSceneRegexp, part) {
			// 先保存上一段场景
			if len(currentContent) > 0 {
				scenes = append(scenes, narrativeScene{text: strings.Join(currentContent, ""), title: currentTitle})
				currentContent = currentContent[:0]
			}
			currentTitle = strategyStrip(part)
			continue
		}
		currentContent = append(currentContent, part)
	}

	if len(currentContent) > 0 {
		scenes = append(scenes, narrativeScene{text: strings.Join(currentContent, ""), title: currentTitle})
	}

	// 若未识别到场景，则把全文视作单一场景
	if len(scenes) == 0 {
		scenes = []narrativeScene{{text: text, title: "Whole Text"}}
	}
	return scenes
}

// slidingWindow 对齐 _sliding_window，按码点窗口滑动并尽量对齐换行
func (s *NarrativeStrategy) slidingWindow(text string, sceneID string, sceneIdx int) []ProcessedChunk {
	runes := []rune(text)
	if len(runes) <= s.WindowSize {
		return []ProcessedChunk{s.createChunk(text, sceneID, sceneIdx, 0, 0)}
	}

	chunks := make([]ProcessedChunk, 0, len(runes)/s.WindowSize+1)
	start := 0
	localIdx := 0
	for start < len(runes) {
		end := start + s.WindowSize
		if end > len(runes) {
			end = len(runes)
		}

		// 尽量对齐到最近换行，避免生硬截断句子
		// 仅在未到文本尾部且回退距离可接受时启用
		if end < len(runes) {
			lastNewline := -1
			for idx := end - 1; idx >= start; idx-- {
				if runes[idx] == '\n' {
					lastNewline = idx - start
					break
				}
			}
			if lastNewline > s.WindowSize/2 {
				end = start + lastNewline + 1
			}
		}

		chunkText := string(runes[start:end])
		chunkSize := end - start
		chunks = append(chunks, s.createChunk(chunkText, sceneID, sceneIdx, localIdx, start))

		if end < len(runes) {
			step := chunkSize - s.Overlap
			if step < 1 {
				step = 1
			}
			start += step
		} else {
			start += chunkSize
		}
		localIdx++
	}
	return chunks
}

// createChunk 对齐 _create_chunk，偏移以场景内码点计
func (s *NarrativeStrategy) createChunk(text string, sceneID string, sceneIdx int, localIdx int, offset int) ProcessedChunk {
	return NewProcessedChunk(
		KnowledgeTypeNarrative,
		SourceInfo{
			File:        s.Filename(),
			OffsetStart: offset,
			OffsetEnd:   offset + strategyRuneLen(text),
			Checksum:    s.CalculateChecksum(text),
		},
		ChunkContext{
			ChunkID: fmt.Sprintf("%s_%d_%d", s.Filename(), sceneIdx, localIdx),
			Index:   localIdx,
			Text:    text,
			Context: map[string]any{"scene_id": sceneID},
		},
	)
}

// Extract 对齐 extract，提取事件与人物关系
func (s *NarrativeStrategy) Extract(ctx context.Context, chunk ProcessedChunk, llmFunc LLMCompletionFunc) (ProcessedChunk, error) {
	if llmFunc == nil {
		return chunk, fmt.Errorf("LLM function required for Narrative extraction")
	}

	languageGuard := s.BuildLanguageGuard(chunk.Chunk.Text)
	sceneID := "None"
	if value, ok := chunk.Chunk.Context["scene_id"]; ok {
		if value == nil {
			sceneID = "None"
		} else {
			sceneID = fmt.Sprint(value)
		}
	}

	var builder strings.Builder
	builder.WriteString("You are a narrative knowledge extraction engine.\n")
	builder.WriteString("Extract key events and character relations from the scene text.\n\n")
	builder.WriteString("Language constraints:\n- ")
	builder.WriteString(languageGuard)
	builder.WriteString("\n- Preserve original names and terms exactly when possible.")
	builder.WriteString("\n- JSON keys must stay exactly as: events, relations, subject, predicate, object.")
	builder.WriteString("\n\nScene:\n")
	builder.WriteString(sceneID)
	builder.WriteString("\n\nText:\n")
	builder.WriteString(chunk.Chunk.Text)
	builder.WriteString("\n\nReturn ONLY valid JSON:\n")
	builder.WriteString("{\n")
	builder.WriteString("  \"events\": [\"event description 1\", \"event description 2\"],\n")
	builder.WriteString("  \"relations\": [\n")
	builder.WriteString("    {\"subject\": \"CharacterA\", \"predicate\": \"relation\", \"object\": \"CharacterB\"}\n")
	builder.WriteString("  ]\n")
	builder.WriteString("}\n")

	result, err := llmFunc(ctx, builder.String())
	if err != nil {
		return chunk, err
	}
	chunk.Data = result
	return chunk, nil
}

// splitKeepDelimiters 对齐 re.split 携带捕获组的行为，保留分隔符本身
func splitKeepDelimiters(re *regexp.Regexp, text string) []string {
	matches := re.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return []string{text}
	}
	parts := make([]string, 0, len(matches)*2+1)
	last := 0
	for _, match := range matches {
		parts = append(parts, text[last:match[0]], text[match[0]:match[1]])
		last = match[1]
	}
	parts = append(parts, text[last:])
	return parts
}

// matchAtStart 对齐 re.match：只在字符串起点尝试匹配
func matchAtStart(re *regexp.Regexp, text string) bool {
	location := re.FindStringIndex(text)
	return location != nil && location[0] == 0
}

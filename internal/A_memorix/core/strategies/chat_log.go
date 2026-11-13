package strategies

// A_memorix 聊天记录策略，对齐 Python core/strategies/chat_log.py

import (
	"regexp"
	"unicode/utf8"
)

// chatTimestampPattern 对齐 _TIMESTAMP
const chatTimestampPattern = "(?:\\d{4}[-/.]\\d{1,2}[-/.]\\d{1,2}[ T])?\\d{1,2}:\\d{2}(?::\\d{2})?"

// chatMessageHeaderPattern 对齐 _MESSAGE_HEADER，使用 (?m) 复刻 re.MULTILINE
const chatMessageHeaderPattern = "(?m)^(?:\\[" + chatTimestampPattern + "\\]|" + chatTimestampPattern + ")\\s*" +
	"(?:<[^>\\r\\n]{1,80}>|\\[[^\\]\\r\\n]{1,80}\\]|[^\\r\\n:：]{1,80}[：:])\\s*"

var chatMessageHeaderRegexp = regexp.MustCompile(chatMessageHeaderPattern)

// chatMessageRange 一条消息在整段文本中的码点区间
type chatMessageRange struct {
	start int
	end   int
}

// ChatLogStrategy 按带时间和发送者的消息边界切分纯文本聊天记录
type ChatLogStrategy struct {
	*NarrativeStrategy
	// SplitWarning 对齐 split_warning，未识别到消息头时的提示
	SplitWarning string
	// OversizedMessageCount 对齐 oversized_message_count，超长单条消息计数
	OversizedMessageCount int
}

var _ BaseStrategy = (*ChatLogStrategy)(nil)

// NewChatLogStrategy 对齐 ChatLogStrategy(filename, window_size, overlap)
func NewChatLogStrategy(filename string, windowSize int, overlap int) *ChatLogStrategy {
	return &ChatLogStrategy{
		NarrativeStrategy: NewNarrativeStrategyWithWindow(filename, windowSize, overlap),
	}
}

// Split 对齐 split：识别消息头后按窗口切块，否则回退到叙述切分
func (s *ChatLogStrategy) Split(text string) []ProcessedChunk {
	s.SplitWarning = ""
	s.OversizedMessageCount = 0
	matches := chatMessageHeaderRegexp.FindAllStringIndex(text, -1)
	if len(matches) == 0 || strategyStrip(text[:matches[0][0]]) != "" {
		s.SplitWarning = "未识别到完整的时间与发送者消息头，已按普通叙事文本切块"
		return s.NarrativeStrategy.Split(text)
	}

	messages := s.messageRanges(text, matches)
	return s.windowMessages(text, messages)
}

// messageRanges 对齐 _message_ranges，把匹配位置转换为消息区间
func (s *ChatLogStrategy) messageRanges(text string, matches [][]int) []chatMessageRange {
	offsets := byteOffsetToRuneOffsets(text, matches)
	total := strategyRuneLen(text)
	ranges := make([]chatMessageRange, 0, len(offsets))
	for index, start := range offsets {
		end := total
		if index+1 < len(offsets) {
			end = offsets[index+1]
		}
		ranges = append(ranges, chatMessageRange{start: start, end: end})
	}
	return ranges
}

// windowMessages 对齐 _window_messages，按消息边界做窗口与重叠
func (s *ChatLogStrategy) windowMessages(text string, messages []chatMessageRange) []ProcessedChunk {
	runes := []rune(text)
	chunks := make([]ProcessedChunk, 0, len(messages))
	cursor := 0
	localIdx := 0

	for cursor < len(messages) {
		endIndex := cursor
		chunkStart := messages[cursor].start
		for endIndex < len(messages) {
			candidateEnd := messages[endIndex].end
			candidateSize := candidateEnd - chunkStart
			if endIndex > cursor && candidateSize > s.WindowSize {
				break
			}
			endIndex++
			if candidateSize >= s.WindowSize {
				break
			}
		}

		chunkEnd := messages[endIndex-1].end
		chunkText := string(runes[chunkStart:chunkEnd])
		if strategyRuneLen(chunkText) > s.WindowSize && endIndex == cursor+1 {
			s.OversizedMessageCount++
		}
		chunks = append(chunks, s.createChunk(chunkText, "Chat Log", 0, localIdx, chunkStart))
		localIdx++

		if endIndex >= len(messages) {
			break
		}

		nextCursor := endIndex
		overlapSize := 0
		overlapIndex := endIndex - 1
		for overlapIndex > cursor && overlapSize < s.Overlap {
			messageStart := messages[overlapIndex].start
			messageEnd := messages[overlapIndex].end
			overlapSize += messageEnd - messageStart
			nextCursor = overlapIndex
			overlapIndex--
		}
		cursor = nextCursor
	}

	return chunks
}

// byteOffsetToRuneOffsets 把正序的字节偏移转换为码点偏移
func byteOffsetToRuneOffsets(text string, matches [][]int) []int {
	offsets := make([]int, len(matches))
	runeIndex := 0
	byteIndex := 0
	for index, match := range matches {
		target := match[0]
		for byteIndex < target {
			_, size := utf8.DecodeRuneInString(text[byteIndex:])
			byteIndex += size
			runeIndex++
		}
		offsets[index] = runeIndex
	}
	return offsets
}

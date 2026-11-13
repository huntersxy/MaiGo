package replyer

import "strings"

// CalculateTypingTime 移植 utils.calculate_typing_time（秒）
func CalculateTypingTime(input string, cfg TimingConfig, typingSpeed float64) float64 {
	if typingSpeed <= 0 {
		return 0
	}

	chineseTime := cfg.ChineseTimeSec
	englishTime := cfg.EnglishTimeSec
	if chineseTime == 0 {
		chineseTime = 0.3
	}
	if englishTime == 0 {
		englishTime = 0.15
	}

	trimmed := strings.TrimSpace(input)
	chineseCount := 0
	for _, r := range input {
		if r >= 0x4E00 && r <= 0x9FFF {
			chineseCount++
		}
	}

	if !cfg.IsEmoji && !cfg.IsImage && chineseCount == 1 && runeLen(trimmed) == 1 {
		return chineseTime*3 + 0.3
	}

	var total float64
	for _, r := range input {
		if r >= 0x4E00 && r <= 0x9FFF {
			total += chineseTime
		} else {
			total += englishTime
		}
	}
	if cfg.IsEmoji || cfg.IsImage {
		total = 1
	}
	return total * typingSpeed
}

func attachTypingDelays(segments []Segment, timing TimingConfig, typingSpeed float64) []Segment {
	out := make([]Segment, len(segments))
	for i, seg := range segments {
		seg.TypingDelaySec = CalculateTypingTime(seg.Text, timing, typingSpeed)
		out[i] = seg
	}
	return out
}

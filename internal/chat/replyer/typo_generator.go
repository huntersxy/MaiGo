package replyer

import (
	"math"
	"math/rand/v2"
	"sort"
	"strings"
)

// maxFreqDiff 对齐 ChineseTypoGenerator 构造默认参数 max_freq_diff=200
const maxFreqDiff = 200.0

// typoGenerator 移植 src/chat/utils/typo_generator.py ChineseTypoGenerator
type typoGenerator struct {
	data *typoData
	rng  *rand.Rand
	cfg  TypoConfig
}

func newTypoGenerator(cfg TypoConfig, rng *rand.Rand) (*typoGenerator, error) {
	data, err := loadTypoData()
	if err != nil {
		return nil, err
	}
	if rng == nil {
		rng = rand.New(rand.NewPCG(1, 2))
	}
	return &typoGenerator{data: data, rng: rng, cfg: cfg}, nil
}

// isTypoChineseChar 对齐 typo_generator._is_chinese_char（U+4E00–U+9FFF）
func isTypoChineseChar(r rune) bool {
	return r >= 0x4E00 && r <= 0x9FFF
}

func containsTypoChinese(word string) bool {
	for _, r := range word {
		if isTypoChineseChar(r) {
			return true
		}
	}
	return false
}

// charFrequency 对齐 self.char_frequency.get(char, 0)
func (g *typoGenerator) charFrequency(r rune) float64 {
	return g.data.charFrequency[r]
}

// wordPinyin 对齐 pypinyin.pinyin(word, style=TONE3) 的逐字首读音
func (g *typoGenerator) wordPinyin(word string) []string {
	return g.data.wordPinyin(word)
}

// similarTonePinyin 对齐 _get_similar_tone_pinyin
func (g *typoGenerator) similarTonePinyin(py string) string {
	if py == "" {
		return py
	}
	last := py[len(py)-1]
	if last < '0' || last > '9' {
		return py + "1"
	}
	base := py[:len(py)-1]
	tone := int(last - '0')
	if tone < 1 || tone > 4 {
		return base + string(rune('0'+1+g.rng.IntN(4)))
	}
	possible := make([]int, 0, 3)
	for candidate := 1; candidate <= 4; candidate++ {
		if candidate != tone {
			possible = append(possible, candidate)
		}
	}
	return base + string(rune('0'+possible[g.rng.IntN(len(possible))]))
}

// replacementProbability 对齐 _calculate_replacement_probability
func (g *typoGenerator) replacementProbability(origFreq, targetFreq float64) float64 {
	if targetFreq > origFreq {
		return 1.0
	}
	diff := origFreq - targetFreq
	if diff > maxFreqDiff {
		return 0.0
	}
	return math.Exp(-3 * diff / maxFreqDiff)
}

type typoCandidate struct {
	char rune
	prob float64
}

// similarFrequencyChars 对齐 _get_similar_frequency_chars（默认 num_candidates=5）
func (g *typoGenerator) similarFrequencyChars(char rune, py string, numCandidates int) []rune {
	var homophones []rune
	if g.rng.Float64() < g.cfg.ToneErrorRate {
		homophones = append(homophones, g.data.pinyinDict[g.similarTonePinyin(py)]...)
	}
	homophones = append(homophones, g.data.pinyinDict[py]...)
	if len(homophones) == 0 {
		return nil
	}
	origFreq := g.charFrequency(char)
	minFreq := float64(g.cfg.MinFreq)
	candidates := make([]typoCandidate, 0, len(homophones))
	for _, h := range homophones {
		if h == char {
			continue
		}
		freq := g.charFrequency(h)
		if freq < minFreq {
			continue
		}
		prob := g.replacementProbability(origFreq, freq)
		if prob <= 0 {
			continue
		}
		candidates = append(candidates, typoCandidate{char: h, prob: prob})
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].prob > candidates[j].prob })
	if numCandidates > 0 && len(candidates) > numCandidates {
		candidates = candidates[:numCandidates]
	}
	out := make([]rune, len(candidates))
	for i, candidate := range candidates {
		out[i] = candidate.char
	}
	return out
}

type typoWordCandidate struct {
	word  string
	score float64
}

// wordHomophones 对齐 _get_word_homophones：按整词同音组合查 jieba 词典并按综合分排序
func (g *typoGenerator) wordHomophones(word string) []string {
	runes := []rune(word)
	if len(runes) == 1 {
		return nil
	}
	wordPinyin := g.wordPinyin(word)
	candidates := make([][]rune, 0, len(wordPinyin))
	for _, py := range wordPinyin {
		chars := g.data.pinyinDict[py]
		if len(chars) == 0 {
			return nil
		}
		candidates = append(candidates, chars)
	}
	originalFreq, _ := g.data.dict.Frequency(word)
	minWordFreq := originalFreq * 0.1
	minFreq := float64(g.cfg.MinFreq)

	var homophones []typoWordCandidate
	indexes := make([]int, len(candidates))
	for {
		var b strings.Builder
		for i, chars := range candidates {
			b.WriteRune(chars[indexes[i]])
		}
		newWord := b.String()
		if newWord != word {
			if newFreq, ok := g.data.dict.Frequency(newWord); ok && newFreq > 0 && newFreq >= minWordFreq {
				charSum := 0.0
				for _, r := range newWord {
					charSum += g.charFrequency(r)
				}
				charAvg := charSum / float64(len([]rune(newWord)))
				score := newFreq*0.7 + charAvg*0.3
				if score >= minFreq {
					homophones = append(homophones, typoWordCandidate{word: newWord, score: score})
				}
			}
		}
		pos := len(indexes) - 1
		for pos >= 0 {
			indexes[pos]++
			if indexes[pos] < len(candidates[pos]) {
				break
			}
			indexes[pos] = 0
			pos--
		}
		if pos < 0 {
			break
		}
	}
	if len(homophones) == 0 {
		return nil
	}
	sort.SliceStable(homophones, func(i, j int) bool { return homophones[i].score > homophones[j].score })
	if len(homophones) > 5 {
		homophones = homophones[:5]
	}
	out := make([]string, len(homophones))
	for i, candidate := range homophones {
		out[i] = candidate.word
	}
	return out
}

type typoWordPair struct {
	wrong   string
	correct string
}

type typoCharPair struct {
	wrong   rune
	correct rune
}

// createTypoSentence 移植 create_typo_sentence，返回错字句与随机纠正建议
func (g *typoGenerator) createTypoSentence(sentence string) (string, string) {
	var result strings.Builder
	var wordTypos []typoWordPair
	var charTypos []typoCharPair

	words := g.data.dict.Cut(sentence)
	for _, word := range words {
		if !containsTypoChinese(word) {
			result.WriteString(word)
			continue
		}
		wordPinyin := g.wordPinyin(word)
		wordRunes := []rune(word)

		if len(wordRunes) > 1 && g.rng.Float64() < g.cfg.WordReplaceRate {
			if homophones := g.wordHomophones(word); len(homophones) > 0 {
				typoWord := homophones[g.rng.IntN(len(homophones))]
				result.WriteString(typoWord)
				wordTypos = append(wordTypos, typoWordPair{wrong: typoWord, correct: word})
				continue
			}
		}

		if len(wordRunes) == 1 {
			char := wordRunes[0]
			py := ""
			if len(wordPinyin) > 0 {
				py = wordPinyin[0]
			}
			if g.rng.Float64() < g.cfg.ErrorRate {
				if similar := g.similarFrequencyChars(char, py, 5); len(similar) > 0 {
					typoChar := similar[g.rng.IntN(len(similar))]
					if g.rng.Float64() < g.replacementProbability(g.charFrequency(char), g.charFrequency(typoChar)) {
						result.WriteRune(typoChar)
						charTypos = append(charTypos, typoCharPair{wrong: typoChar, correct: char})
						continue
					}
				}
			}
			result.WriteRune(char)
			continue
		}

		wordErrorRate := g.cfg.ErrorRate * math.Pow(0.7, float64(len(wordRunes)-1))
		for idx, char := range wordRunes {
			py := ""
			if idx < len(wordPinyin) {
				py = wordPinyin[idx]
			}
			if g.rng.Float64() < wordErrorRate {
				if similar := g.similarFrequencyChars(char, py, 5); len(similar) > 0 {
					typoChar := similar[g.rng.IntN(len(similar))]
					if g.rng.Float64() < g.replacementProbability(g.charFrequency(char), g.charFrequency(typoChar)) {
						result.WriteRune(typoChar)
						charTypos = append(charTypos, typoCharPair{wrong: typoChar, correct: char})
						continue
					}
				}
			}
			result.WriteRune(char)
		}
	}

	correction := ""
	if g.rng.Float64() < 0.5 {
		if len(wordTypos) > 0 {
			correction = wordTypos[g.rng.IntN(len(wordTypos))].correct
		} else if len(charTypos) > 0 {
			correction = string(charTypos[g.rng.IntN(len(charTypos))].correct)
		}
	}
	return result.String(), correction
}

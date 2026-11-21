package chinesetypo

import (
	"bufio"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	_ "embed"
	"fmt"
	"math"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"MaiGo/internal/chat/replyer/jiebacut"
)

//go:embed pinyin_tone3.tsv.gz
var pinyinTone3 []byte

type ruleData struct {
	characters map[rune]string
	phrases    map[string]string
	maxPhrase  int
	homophones map[string][]rune
	frequency  map[rune]float64
}

var loadRuleData = sync.OnceValues(func() (*ruleData, error) {
	data := &ruleData{characters: make(map[rune]string), phrases: make(map[string]string),
		homophones: make(map[string][]rune), frequency: make(map[rune]float64)}
	reader, err := gzip.NewReader(bytes.NewReader(pinyinTone3))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		key, pronunciation, ok := strings.Cut(scanner.Text(), "\t")
		if !ok || key == "" || pronunciation == "" {
			return nil, fmt.Errorf("无效的拼音词条")
		}
		char, width := utf8.DecodeRuneInString(key)
		if width == len(key) {
			data.characters[char] = pronunciation
		} else {
			data.phrases[key] = pronunciation
			data.maxPhrase = max(data.maxPhrase, utf8.RuneCountInString(key))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	for char := rune(0x4e00); char < 0x9fff; char++ {
		pinyin := data.characterPinyin(char)
		data.homophones[pinyin] = append(data.homophones[pinyin], char)
	}
	var maximum float64
	if err := jiebacut.VisitDefaultWords(func(word string, frequency float64) {
		for _, char := range word {
			if isChinese(char) {
				data.frequency[char] += frequency
				maximum = max(maximum, data.frequency[char])
			}
		}
	}); err != nil {
		return nil, err
	}
	for char, frequency := range data.frequency {
		data.frequency[char] = frequency / maximum * 1000
	}
	return data, nil
})

func isChinese(char rune) bool { return char >= 0x4e00 && char <= 0x9fff }

// pypinyin 0.55.0 使用的汉字范围，含扩展区和兼容字。
var pinyinHan = regexp.MustCompile(`^[\x{3007}\x{e815}-\x{e864}\x{fa18}\x{3400}-\x{4dbf}\x{4e00}-\x{9fff}\x{f900}-\x{faff}\x{20000}-\x{2a6df}\x{2a703}-\x{2b73f}\x{2b740}-\x{2b81d}\x{2b825}-\x{2bf6e}\x{2c029}-\x{2ce93}\x{2d016}\x{2d11b}-\x{2ebd9}\x{2f80a}-\x{2fa1f}\x{30000}-\x{3134a}\x{31350}-\x{32389}]+`)

func (d *ruleData) characterPinyin(char rune) string {
	if pronunciation := d.characters[char]; pronunciation != "" {
		return pronunciation
	}
	return string(char)
}

func (d *ruleData) wordPinyin(word string) []string {
	var result []string
	for word != "" {
		han := pinyinHan.FindString(word)
		if han == "" {
			end := len(word)
			for index, char := range word {
				if pinyinHan.MatchString(string(char)) {
					end = index
					break
				}
			}
			result = append(result, word[:end])
			word = word[end:]
			continue
		}
		for remaining := []rune(han); len(remaining) != 0; {
			matched := false
			for length := min(len(remaining), d.maxPhrase); length >= 2; length-- {
				if pronunciation := d.phrases[string(remaining[:length])]; pronunciation != "" {
					result = append(result, strings.Split(pronunciation, " ")...)
					remaining = remaining[length:]
					matched = true
					break
				}
			}
			if !matched {
				result = append(result, d.characterPinyin(remaining[0]))
				remaining = remaining[1:]
			}
		}
		word = word[len(han):]
	}
	return result
}

func similarTone(pinyin string, random *rand.Rand) string {
	if pinyin == "" {
		return ""
	}
	last := pinyin[len(pinyin)-1]
	if last < '0' || last > '9' {
		return pinyin + "1"
	}
	tones := "1234"
	if last >= '1' && last <= '4' {
		position := int(last - '1')
		tones = tones[:position] + tones[position+1:]
	}
	tone := random.IntN(len(tones))
	return pinyin[:len(pinyin)-1] + tones[tone:tone+1]
}

func replacementProbability(original, replacement float64) float64 {
	difference := original - replacement
	if difference < 0 {
		return 1
	}
	if difference > 200 {
		return 0
	}
	return math.Exp(-3 * difference / 200)
}

type scoredCandidate[T any] struct {
	value T
	score float64
}

func addCandidate[T any](top []scoredCandidate[T], value T, score float64) []scoredCandidate[T] {
	if len(top) == 5 && score <= top[4].score {
		return top
	}
	top = append(top, scoredCandidate[T]{value, score})
	slices.SortStableFunc(top, func(a, b scoredCandidate[T]) int { return cmp.Compare(b.score, a.score) })
	return top[:min(len(top), 5)]
}

func (d *ruleData) replaceChar(char rune, pinyin string, settings typoSettings, random *rand.Rand) rune {
	var candidates []scoredCandidate[rune]
	add := func(pronunciation string) {
		for _, other := range d.homophones[pronunciation] {
			if other == char || d.frequency[other] < float64(settings.minFrequency) {
				continue
			}
			score := replacementProbability(d.frequency[char], d.frequency[other])
			if score > 0 {
				candidates = addCandidate(candidates, other, score)
			}
		}
	}
	if random.Float64() < settings.toneChance {
		add(similarTone(pinyin, random))
	}
	add(pinyin)
	if len(candidates) > 0 {
		candidate := candidates[random.IntN(len(candidates))]
		if random.Float64() < candidate.score {
			return candidate.value
		}
	}
	return char
}

func (d *ruleData) replaceWord(ctx context.Context, tokenizer *jiebacut.Dictionary, word string, pronunciation []string, settings typoSettings, random *rand.Rand) (string, error) {
	positions := make([][]rune, 0, len(pronunciation))
	for _, pinyin := range pronunciation {
		chars := d.homophones[pinyin]
		if len(chars) == 0 {
			return word, nil
		}
		positions = append(positions, chars)
	}
	minimum := tokenizer.BaseFrequency(word) * 0.1
	var candidates []scoredCandidate[string]
	err := tokenizer.MatchBaseWords(ctx, positions, func(candidate string, wordFrequency float64) {
		if candidate == word || wordFrequency < minimum {
			return
		}
		var total float64
		for _, char := range candidate {
			total += d.frequency[char]
		}
		score := wordFrequency*0.7 + total/float64(len(positions))*0.3
		if score >= float64(settings.minFrequency) {
			candidates = addCandidate(candidates, candidate, score)
		}
	})
	if err != nil || len(candidates) == 0 {
		return word, err
	}
	return candidates[random.IntN(len(candidates))].value, nil
}

func (d *ruleData) ruleTypo(ctx context.Context, tokenizer *jiebacut.Dictionary, text string, settings typoSettings, random *rand.Rand) (string, string, error) {
	var output strings.Builder
	var words, characters []string
	for _, word := range tokenizer.Cut(text) {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		chars := []rune(word)
		if !slices.ContainsFunc(chars, isChinese) {
			output.WriteString(word)
			continue
		}
		pronunciation := d.wordPinyin(word)
		if len(chars) > 1 && random.Float64() < settings.wordChance {
			replacement, err := d.replaceWord(ctx, tokenizer, word, pronunciation, settings, random)
			if err != nil {
				return "", "", err
			}
			if replacement != word {
				output.WriteString(replacement)
				words = append(words, word)
				continue
			}
		}
		chance := settings.charChance * math.Pow(0.7, float64(len(chars)-1))
		for i, char := range chars[:min(len(chars), len(pronunciation))] {
			if random.Float64() < chance {
				if replacement := d.replaceChar(char, pronunciation[i], settings, random); replacement != char {
					output.WriteRune(replacement)
					characters = append(characters, string(char))
					continue
				}
			}
			output.WriteRune(char)
		}
	}
	correction := ""
	if random.Float64() < 0.5 {
		if len(words) > 0 {
			correction = words[random.IntN(len(words))]
		} else if len(characters) > 0 {
			correction = characters[random.IntN(len(characters))]
		}
	}
	return output.String(), correction, nil
}

package jiebacut

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"strconv"
	"strings"
)

// VisitDefaultWords 遍历内置 jieba 词表的原始词频，包括用户词典之外的单字。
func VisitDefaultWords(visit func(string, float64)) error {
	reader, err := gzip.NewReader(bytes.NewReader(defaultDictionaryGz))
	if err != nil {
		return err
	}
	defer reader.Close()
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		word, rest, ok := strings.Cut(scanner.Text(), " ")
		if !ok {
			continue
		}
		frequency, _, _ := strings.Cut(rest, " ")
		n, err := strconv.ParseFloat(frequency, 64)
		if err != nil {
			return err
		}
		visit(word, n)
	}
	return scanner.Err()
}

// BaseFrequency 返回加载用户词典之前的词频。
func (d *Dictionary) BaseFrequency(word string) float64 {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.baseFrequencyLocked(word)
}

func (d *Dictionary) baseFrequencyLocked(word string) float64 {
	if n, changed := d.baseOverrides[word]; changed {
		return n
	}
	return d.freq[word]
}

// MatchBaseWords 以每个位置的候选顺序遍历词典，提前淘汰不在词典中的前缀。
func (d *Dictionary) MatchBaseWords(ctx context.Context, positions [][]rune, visit func(string, float64)) error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var walk func(int, []byte) error
	walk = func(i int, prefix []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if i == len(positions) {
			if word := string(prefix); d.baseFrequencyLocked(word) > 0 {
				visit(word, d.baseFrequencyLocked(word))
			}
			return nil
		}
		for _, char := range positions[i] {
			candidate := append(prefix, string(char)...)
			if _, exists := d.freq[string(candidate)]; exists {
				if err := walk(i+1, candidate); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(0, make([]byte, 0, len(positions)*3))
}

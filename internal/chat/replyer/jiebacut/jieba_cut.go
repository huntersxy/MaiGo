// Package jiebacut 是 MaiBot 依赖的 jieba 精确模式分词在 Go 侧的移植
//
// Python 对照：jieba/__init__.py 的 cut(cut_all=False, HMM=True) 路径
//   - re_han_default  : ([\u4E00-\u9FD5a-zA-Z0-9+#&._%-]+)
//   - re_skip_default : (\r\n|\s)
//   - get_DAG / calc / __cut_DAG 逐行对应
//
// 词典数据与 CharFrequency 由 replyer/data 以 go:embed 提供；HMM 未登录词切分复用
// github.com/wangbin/jiebago/finalseg 的 __cut（与 jieba finalseg 同源），
// 但**不复用**它的分块正则：jiebago 的 reSkip 是 (\d+\.\d+|[a-zA-Z0-9]+)，
// Python 是 ([a-zA-Z0-9]+(?:\.\d+)?%?)，且 Python 对非汉字段原样输出不再逐字拆。
// 直接用 jiebago 的 Cut 会把 "50%" 拆成 "50"/"%"、把 "%-." 拆成 "%"/"-"/"."
// 与 Python 不一致，因此见 finalsegCut。
package jiebacut

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/wangbin/jiebago/finalseg"
)

const pyWhitespace = `\x{0020}\t\n\v\f\r\x{001C}-\x{001F}\x{0085}\x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}`

var (
	reHanDefault  = regexp.MustCompile(`([\x{4E00}-\x{9FD5}a-zA-Z0-9+#&._%-]+)`)
	reSkipDefault = regexp.MustCompile(`(\r\n|[` + pyWhitespace + `])`)
)

// Dictionary 对应 jieba.Tokenizer 的 FREQ 与 total
//
// mu 保护 freq/total/logTotal：词典是进程级共享对象，用户词典加载会写入，
// 而分词在多个请求 goroutine 上并发读，Python 靠 GIL 保证一致，Go 必须显式加锁。
type Dictionary struct {
	mu       sync.RWMutex
	freq     map[string]float64
	total    float64
	logTotal float64
}

// NewDictionary 从 jieba dict.txt 结构（每行 词语 频次 [词性]）加载词典
func NewDictionary(r io.Reader) (*Dictionary, error) {
	d := &Dictionary{freq: make(map[string]float64, 400000)}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		freq, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return nil, err
		}
		word := fields[0]
		if word == "" || freq <= 0 {
			continue
		}
		d.freq[word] += freq
		d.total += freq
		// jieba gen_pfdict：把词的全部前缀补成 0 频次，供 get_DAG 向前探词
		runes := []rune(word)
		for i := 1; i < len(runes); i++ {
			prefix := string(runes[:i])
			if _, ok := d.freq[prefix]; !ok {
				d.freq[prefix] = 0
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if d.total <= 0 {
		return nil, io.ErrUnexpectedEOF
	}
	d.logTotal = math.Log(d.total)
	return d, nil
}

// Frequency 返回词频与是否存在（前缀词频为 0）
func (d *Dictionary) Frequency(word string) (float64, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	freq, ok := d.freq[word]
	return freq, ok
}

// Total 返回词频总和
func (d *Dictionary) Total() float64 {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.total
}

// AddWord 向词典加入一个词；对应 jieba.Tokenizer.add_word
//
// freq 为 nil 时按 suggest_freq 推算，保证该词能被切出。加入词后补齐所有前缀
// 的 0 频次，供 get_DAG 向前探词——这与 gen_pfdict 的处理一致，缺了会导致
// 新词的前缀断链，DAG 找不到该词。
func (d *Dictionary) AddWord(word string, freq *float64) error {
	word = strings.TrimSpace(word)
	if word == "" {
		return fmt.Errorf("用户词典词条为空")
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	value := 0.0
	if freq != nil {
		value = *freq
	} else {
		value = d.suggestFreqLocked(word)
	}
	d.freq[word] = value
	d.total += value
	for i := 1; i < len([]rune(word)); i++ {
		prefix := string([]rune(word)[:i])
		if _, ok := d.freq[prefix]; !ok {
			d.freq[prefix] = 0
		}
	}
	if d.total > 0 {
		d.logTotal = math.Log(d.total)
	}
	return nil
}

// suggestFreqLocked 推算让该词整体切出的最小频次；对应 jieba.suggest_freq(word, False)
//
// 调用方必须已持有写锁。
func (d *Dictionary) suggestFreqLocked(word string) float64 {
	ftotal := d.total
	freq := 1.0
	for _, seg := range d.cutLocked(word) {
		segFreq, ok := d.freq[seg]
		if !ok || segFreq == 0 {
			segFreq = 1
		}
		freq *= segFreq / ftotal
	}
	computed := float64(int(freq*d.total) + 1)
	if existing, ok := d.freq[word]; ok && existing > computed {
		return existing
	}
	return computed
}

// Cut 精确模式分词；对应 jieba.cut(cut_all=False, HMM=True)
func (d *Dictionary) Cut(sentence string) []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.cutLocked(sentence)
}

// cutLocked 分词实现体，调用方负责持锁
func (d *Dictionary) cutLocked(sentence string) []string {
	if sentence == "" {
		return nil
	}
	var out []string
	for _, block := range splitKeepingSeparators(reHanDefault, sentence) {
		if reHanDefault.MatchString(block) {
			out = append(out, d.cutDAG(block)...)
			continue
		}
		for _, sub := range splitKeepingSeparators(reSkipDefault, block) {
			if reSkipDefault.MatchString(sub) {
				out = append(out, sub)
				continue
			}
			for _, r := range sub {
				out = append(out, string(r))
			}
		}
	}
	return out
}

// CutForSearch 搜索模式分词；对应 jieba.cut_for_search(HMM=True)
// 先按精确模式切分，再对长词补充存在的二字词与三字词，最后输出原词
func (d *Dictionary) CutForSearch(sentence string) []string {
	return d.cutForSearch(sentence, true)
}

// cutForSearch 内部实现；tokenize 表示是否对结果做小写与去空白处理
func (d *Dictionary) cutForSearch(sentence string, tokenize bool) []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	words := d.cutLocked(sentence)
	out := make([]string, 0, len(words)*2)
	for _, word := range words {
		runes := []rune(word)
		if len(runes) > 2 {
			for i := 0; i < len(runes)-1; i++ {
				gram := string(runes[i : i+2])
				if freq, ok := d.freq[gram]; ok && freq > 0 {
					out = append(out, gram)
				}
			}
		}
		if len(runes) > 3 {
			for i := 0; i < len(runes)-2; i++ {
				gram := string(runes[i : i+3])
				if freq, ok := d.freq[gram]; ok && freq > 0 {
					out = append(out, gram)
				}
			}
		}
		out = append(out, word)
	}
	return out
}

// splitKeepingSeparators 等价 Python re.split(pattern_with_group, s)
func splitKeepingSeparators(re *regexp.Regexp, s string) []string {
	matches := re.FindAllStringIndex(s, -1)
	if len(matches) == 0 {
		return []string{s}
	}
	out := make([]string, 0, len(matches)*2+1)
	last := 0
	for _, m := range matches {
		out = append(out, s[last:m[0]])
		out = append(out, s[m[0]:m[1]])
		last = m[1]
	}
	out = append(out, s[last:])
	return out
}

type dagRoute struct {
	frequency float64
	index     int
}

// cutDAG 对应 jieba __cut_DAG（含 HMM 未登录词处理）
func (d *Dictionary) cutDAG(sentence string) []string {
	runes := []rune(sentence)
	n := len(runes)
	routes := make([]dagRoute, n+1)
	for idx := n - 1; idx >= 0; idx-- {
		best := dagRoute{frequency: math.Inf(-1), index: idx}
		for x := idx; x < n; x++ {
			frag := string(runes[idx : x+1])
			freq, ok := d.freq[frag]
			if !ok {
				break
			}
			if freq <= 0 {
				continue
			}
			score := math.Log(freq) - d.logTotal + routes[x+1].frequency
			if score > best.frequency || (score == best.frequency && x > best.index) {
				best = dagRoute{frequency: score, index: x}
			}
		}
		if math.IsInf(best.frequency, -1) {
			best = dagRoute{frequency: 0, index: idx}
		}
		routes[idx] = best
	}

	var out []string
	var buf []rune
	for x := 0; x < n; {
		y := routes[x].index + 1
		frag := runes[x:y]
		if y-x == 1 {
			buf = append(buf, frag...)
		} else {
			if len(buf) > 0 {
				out = append(out, d.flushBuffer(buf)...)
				buf = nil
			}
			out = append(out, string(frag))
		}
		x = y
	}
	if len(buf) > 0 {
		out = append(out, d.flushBuffer(buf)...)
	}
	return out
}

// flushBuffer 对应 jieba __cut_DAG 中对单字缓冲区的 HMM 处理
func (d *Dictionary) flushBuffer(buf []rune) []string {
	if len(buf) == 1 {
		return []string{string(buf)}
	}
	word := string(buf)
	if freq, ok := d.freq[word]; !ok || freq == 0 {
		return finalsegCut(word)
	}
	out := make([]string, len(buf))
	for i, r := range buf {
		out[i] = string(r)
	}
	return out
}

// Python jieba finalseg 的分块正则，与 jiebago/finalseg 的实现不同：
//
//	Python : re_han = ([一-鿕]+)   re_skip = ([a-zA-Z0-9]+(?:\.\d+)?%?)
//	jiebago: reHan  = \p{Han}+             reSkip  = (\d+\.\d+|[a-zA-Z0-9]+)
//
// 直接用 jiebago 的 Cut 会把 "50%" 拆成 "50" + "%"、"v1.2" 拆成 "v1" + "." + "2"，
// 与 Python 原版不一致。这里按 Python 的正则自行分块，只把纯汉字段交给
// jiebago 的 HMM（其 __cut 与 jieba finalseg 同源），非汉字段自己处理。
var (
	finalsegHan  = regexp.MustCompile(`([\x{4E00}-\x{9FD5}]+)`)
	finalsegSkip = regexp.MustCompile(`([a-zA-Z0-9]+(?:\.[0-9]+)?%?)`)
)

// finalsegCut 等价 Python jieba.finalseg.cut(sentence)
//
// Python 对非汉字段只做 re_skip 切分、原样输出每一段（不再逐字拆开），
// 所以 "%-." 这种片段会整体保留。
func finalsegCut(sentence string) []string {
	var out []string
	for _, block := range splitKeepingSeparators(finalsegHan, sentence) {
		if finalsegHan.MatchString(block) {
			for word := range finalseg.Cut(block) {
				out = append(out, word)
			}
			continue
		}
		for _, piece := range splitKeepingSeparators(finalsegSkip, block) {
			if piece != "" {
				out = append(out, piece)
			}
		}
	}
	return out
}

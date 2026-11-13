package replyer

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"fmt"
	"io"
	"sync"

	"maibot-go/internal/chat/replyer/jiebacut"

	"github.com/bytedance/sonic"
)

// 数据来源（均随仓库内嵌，保持二进制自包含）：
//   - depends-data/char_frequency.json：MaiBot 的 ChineseTypoGenerator 字频缓存
//   - jieba 0.42.1 dict.txt：jieba 词典（349046 词条）；与 MaiBot 字频缓存逐字校验一致
//   - pypinyin 0.55.0 pinyin_dict.json / phrases_dict.json：单字与词语拼音，已离线转成 TONE3
//
//go:embed data/char_frequency.json.gz
var typoCharFrequencyGz []byte

//go:embed data/jieba_dict.txt.gz
var typoJiebaDictGz []byte

//go:embed data/pinyin_tone3.json.gz
var typoPinyinGz []byte

//go:embed data/phrases_tone3.json.gz
var typoPhrasesGz []byte

// typoData 是 ChineseTypoGenerator 的进程级共享数据，对齐 Python @cache 静态方法
type typoData struct {
	charFrequency map[rune]float64
	pinyin        map[rune]string
	phrases       map[string][]string
	pinyinDict    map[string][]rune
	dict          *jiebacut.Dictionary
}

var (
	typoDataOnce sync.Once
	typoDataVal  *typoData
	typoDataErr  error
)

// loadTypoData 首次使用时加载并缓存；对齐 Python 首次构造生成器的懒加载语义
func loadTypoData() (*typoData, error) {
	typoDataOnce.Do(func() {
		typoDataVal, typoDataErr = newTypoData()
	})
	return typoDataVal, typoDataErr
}

func newTypoData() (*typoData, error) {
	charFreq, err := decodeCharFrequency(typoCharFrequencyGz)
	if err != nil {
		return nil, fmt.Errorf("加载汉字字频: %w", err)
	}
	pinyinTable, err := decodePinyinTable(typoPinyinGz)
	if err != nil {
		return nil, fmt.Errorf("加载拼音表: %w", err)
	}
	phrases, err := decodePhrases(typoPhrasesGz)
	if err != nil {
		return nil, fmt.Errorf("加载词语拼音表: %w", err)
	}
	dict, err := decodeJiebaDict(typoJiebaDictGz)
	if err != nil {
		return nil, fmt.Errorf("加载 jieba 词典: %w", err)
	}
	return &typoData{
		charFrequency: charFreq,
		pinyin:        pinyinTable,
		phrases:       phrases,
		pinyinDict:    buildPinyinDict(pinyinTable),
		dict:          dict,
	}, nil
}

func decodeCharFrequency(compressed []byte) (map[rune]float64, error) {
	raw, err := gunzip(compressed)
	if err != nil {
		return nil, err
	}
	var jsonMap map[string]float64
	if err := sonic.Unmarshal(raw, &jsonMap); err != nil {
		return nil, err
	}
	out := make(map[rune]float64, len(jsonMap))
	for key, freq := range jsonMap {
		runes := []rune(key)
		if len(runes) != 1 {
			continue
		}
		out[runes[0]] = freq
	}
	return out, nil
}

func decodePinyinTable(compressed []byte) (map[rune]string, error) {
	raw, err := gunzip(compressed)
	if err != nil {
		return nil, err
	}
	var jsonMap map[string]string
	if err := sonic.Unmarshal(raw, &jsonMap); err != nil {
		return nil, err
	}
	out := make(map[rune]string, len(jsonMap))
	for key, py := range jsonMap {
		runes := []rune(key)
		if len(runes) != 1 || py == "" {
			continue
		}
		out[runes[0]] = py
	}
	return out, nil
}

func decodePhrases(compressed []byte) (map[string][]string, error) {
	raw, err := gunzip(compressed)
	if err != nil {
		return nil, err
	}
	var jsonMap map[string][]string
	if err := sonic.Unmarshal(raw, &jsonMap); err != nil {
		return nil, err
	}
	return jsonMap, nil
}

func decodeJiebaDict(compressed []byte) (*jiebacut.Dictionary, error) {
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	return jiebacut.NewDictionary(gz)
}

func gunzip(compressed []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	return io.ReadAll(gz)
}

// buildPinyinDict 对齐 ChineseTypoGenerator._create_pinyin_dict：
// 遍历 U+4E00–U+9FFF，按单个汉字 TONE3 拼音聚合；无拼音字按 pypinyin errors=default 保留原字
func buildPinyinDict(pinyinTable map[rune]string) map[string][]rune {
	out := make(map[string][]rune, 4096)
	for r := rune(0x4E00); r <= 0x9FFF; r++ {
		py := pinyinTable[r]
		if py == "" {
			py = string(r)
		}
		out[py] = append(out[py], r)
	}
	return out
}

// pinyinOf 返回单字 TONE3 拼音；无拼音时与 pypinyin errors=default 一致回退为原字
func (d *typoData) pinyinOf(r rune) string {
	if py := d.pinyin[r]; py != "" {
		return py
	}
	return string(r)
}

// wordPinyin 对齐 pypinyin.pinyin(word, style=TONE3) 的逐字首读音：
// 先按汉字/非汉字切块，汉字块用词语库做正向最大匹配，命中词语取词语读音，否则逐字取音
func (d *typoData) wordPinyin(word string) []string {
	out := make([]string, 0, len([]rune(word)))
	for _, block := range splitHanBlocks(word) {
		if !isHanString(block) {
			for _, r := range block {
				out = append(out, string(r))
			}
			continue
		}
		for _, phrase := range d.cutPhrases(block) {
			if readings, ok := d.phrases[phrase]; ok && len(readings) == len([]rune(phrase)) {
				out = append(out, readings...)
				continue
			}
			for _, r := range phrase {
				out = append(out, d.pinyinOf(r))
			}
		}
	}
	return out
}

// splitHanBlocks 对齐 pypinyin.seg.simpleseg：按连续汉字/非汉字切块
func splitHanBlocks(text string) []string {
	if text == "" {
		return nil
	}
	var out []string
	var b []rune
	lastHan := isHanRune(firstRune(text))
	for _, r := range text {
		han := isHanRune(r)
		if len(b) > 0 && han != lastHan {
			out = append(out, string(b))
			b = b[:0]
		}
		b = append(b, r)
		lastHan = han
	}
	if len(b) > 0 {
		out = append(out, string(b))
	}
	return out
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

func isHanString(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !isHanRune(r) {
			return false
		}
	}
	return true
}

// isHanRune 对齐 pypinyin RE_HANS（UCS4 分支）覆盖的汉字范围
func isHanRune(r rune) bool {
	switch {
	case r == 0x3007, r >= 0xE815 && r <= 0xE864, r == 0xFA18,
		r >= 0x3400 && r <= 0x4DBF,
		r >= 0x4E00 && r <= 0x9FFF,
		r >= 0xF900 && r <= 0xFAFF,
		r >= 0x20000 && r <= 0x2A6DF,
		r >= 0x2A703 && r <= 0x2B73F,
		r >= 0x2B740 && r <= 0x2B81D,
		r >= 0x2B825 && r <= 0x2BF6E,
		r >= 0x2C029 && r <= 0x2CE93,
		r == 0x2D016,
		r >= 0x2D11B && r <= 0x2EBD9,
		r >= 0x2F80A && r <= 0x2FA1F,
		r >= 0x30000 && r <= 0x3134A,
		r >= 0x300F7 && r <= 0x31288,
		r == 0x30EDD, r == 0x30EDE,
		r >= 0x31350 && r <= 0x32389:
		return true
	}
	return false
}

// cutPhrases 对齐 pypinyin.contrib.mmseg（no_non_phrases=True）：
// 贪心最长词语匹配，未命中则单字前进
func (d *typoData) cutPhrases(text string) []string {
	runes := []rune(text)
	out := make([]string, 0, len(runes))
	for i := 0; i < len(runes); {
		matched := 0
		for j := len(runes); j > i+1; j-- {
			candidate := string(runes[i:j])
			if _, ok := d.phrases[candidate]; ok {
				matched = j - i
				break
			}
		}
		if matched > 0 {
			out = append(out, string(runes[i:i+matched]))
			i += matched
			continue
		}
		out = append(out, string(runes[i]))
		i++
	}
	return out
}

// WarmTypoData 预加载错别字数据；供启动阶段异步预热，避免首条回复承担词典加载耗时
func WarmTypoData() error {
	_, err := loadTypoData()
	return err
}

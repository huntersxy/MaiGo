package retrieval

// 中文稀疏索引分词器，对齐 MaiBot A_memorix 的 jieba cut_for_search 策略
// 词典随包内嵌，避免依赖宿主环境是否安装 jieba；加载失败时退化为字符 n-gram，
// 并让索引与查询共用同一分词器，避免“索引单字、查询 bigram”的零命中

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/chat/replyer/jiebacut"
)

//go:embed data/jieba_dict.txt.gz
var jiebaDictionaryGz []byte

// JiebaSegmenter 基于 replyer/jiebacut 的 jieba 搜索模式分词器
type JiebaSegmenter struct {
	dict *jiebacut.Dictionary
}

// CutForSearch 返回 jieba 搜索模式切词结果
func (s *JiebaSegmenter) CutForSearch(sentence string) []string {
	if s == nil || s.dict == nil {
		return nil
	}
	return s.dict.CutForSearch(sentence)
}

// LoadUserDict 加载 jieba 用户词典；对应 jieba.load_userdict
//
// 文件格式与 Python 一致：每行「词语 [频次] [词性]」，以空白分隔，频次可省略。
// 频次省略时按 suggest_freq 推算，保证该词能被整体切出。Python 侧加载失败只
// 记 warning 不中断（sparse_bm25.py:366-372），这里的错误由调用方同样处理。
func (s *JiebaSegmenter) LoadUserDict(path string) error {
	if s == nil || s.dict == nil {
		return fmt.Errorf("jieba 分词器未就绪: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开用户词典失败: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// 对齐 Python re_userdict = ^(.+?)( [0-9]+)?( [a-z]+)?$
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		word := fields[0]
		var freq *float64
		if len(fields) > 1 {
			if value, err := strconv.ParseFloat(fields[1], 64); err == nil {
				freq = &value
			}
		}
		if err := s.dict.AddWord(word, freq); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取用户词典失败: %w", err)
	}
	return nil
}

// CharNgramSegmenter 字符 n-gram 分词器，作为 jieba 不可用时的确定性退化
type CharNgramSegmenter struct {
	N int
}

// NewCharNgramSegmenter 构造字符 n-gram 分词器
func NewCharNgramSegmenter(n int) *CharNgramSegmenter {
	if n < 1 {
		n = 1
	}
	return &CharNgramSegmenter{N: n}
}

// CutForSearch 按去除空白后的字符窗口切分
func (s *CharNgramSegmenter) CutForSearch(sentence string) []string {
	if s == nil {
		return nil
	}
	compact := strings.ToLower(strings.Join(strings.Fields(sentence), ""))
	if compact == "" {
		return nil
	}
	runes := []rune(compact)
	if len(runes) < s.N {
		return []string{compact}
	}
	out := make([]string, 0, len(runes)-s.N+1)
	for start := 0; start+s.N <= len(runes); start++ {
		out = append(out, string(runes[start:start+s.N]))
	}
	return out
}

var (
	defaultJiebaSegmenterOnce sync.Once
	defaultJiebaSegmenter     *JiebaSegmenter
	defaultJiebaSegmenterErr  error
)

// DefaultJiebaSegmenter 返回进程级共享的 jieba 分词器，首次调用时加载内嵌词典
func DefaultJiebaSegmenter() (*JiebaSegmenter, error) {
	defaultJiebaSegmenterOnce.Do(func() {
		reader, err := gzip.NewReader(bytes.NewReader(jiebaDictionaryGz))
		if err != nil {
			defaultJiebaSegmenterErr = fmt.Errorf("解压 jieba 词典失败: %w", err)
			return
		}
		defer reader.Close()
		dict, err := jiebacut.NewDictionary(reader)
		if err != nil {
			defaultJiebaSegmenterErr = fmt.Errorf("加载 jieba 词典失败: %w", err)
			return
		}
		defaultJiebaSegmenter = &JiebaSegmenter{dict: dict}
	})
	return defaultJiebaSegmenter, defaultJiebaSegmenterErr
}

// SegmenterForSparseConfig 按稀疏配置选择索引与查询共用的分词器
// jieba/mixed 优先真实 jieba；词典不可用或 char_2gram 时退化为字符 n-gram
func SegmenterForSparseConfig(config SparseBM25Config) storage.Segmenter {
	config.Normalize()
	if config.TokenizerMode == "char_2gram" {
		return NewCharNgramSegmenter(config.CharNgramN)
	}
	segmenter, err := DefaultJiebaSegmenter()
	if err == nil && segmenter != nil {
		return segmenter
	}
	return NewCharNgramSegmenter(config.CharNgramN)
}

var _ storage.Segmenter = (*JiebaSegmenter)(nil)
var _ storage.Segmenter = (*CharNgramSegmenter)(nil)

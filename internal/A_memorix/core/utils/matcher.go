package utils

// A_memorix 多模式匹配工具，对齐 Python core/utils/matcher.py
// Go 侧没有 ahocorasick-rs 原生实现，保留原生阈值语义作为构造参数
// 实际匹配始终使用纯 Go 的 Aho-Corasick 自动机

import (
	"os"
	"sort"
	"strconv"
	"strings"
)

// DefaultNativeMatcherMinPatterns 原生匹配器默认最少模式数，对齐常量 3000
const DefaultNativeMatcherMinPatterns = 3000

// nativeMatcherMinEnv 原生匹配阈值环境变量名
const nativeMatcherMinEnv = "A_MEMORIX_NATIVE_MATCHER_MIN_PATTERNS"

// AhoCorasickMatch 一次模式匹配结果
// End 为匹配结束位置，按码点计，与 Python 的 enumerate 索引一致
type AhoCorasickMatch struct {
	End     int
	Pattern string
}

// AhoCorasick Aho-Corasick 自动机，对齐 Python AhoCorasick
type AhoCorasick struct {
	nextStates []map[rune]int
	fail       []int
	output     []map[string]struct{}
	patterns   map[string]struct{}
	// NativeMinPatterns 原生实现阈值，对齐 native_min_patterns
	NativeMinPatterns int
	pythonBuilt       bool
}

// NewAhoCorasick 构造自动机，nativeMinPatterns 为 nil 时读取环境变量
func NewAhoCorasick(nativeMinPatterns *int) *AhoCorasick {
	return &AhoCorasick{
		nextStates:        []map[rune]int{{}},
		fail:              []int{0},
		output:            []map[string]struct{}{{}},
		patterns:          map[string]struct{}{},
		NativeMinPatterns: ResolveNativeMinPatterns(nativeMinPatterns),
	}
}

// ResolveNativeMinPatterns 对齐 _resolve_native_min_patterns
// 显式传值取 max(1, 值)；否则读取环境变量，空值或非法值回退默认 3000
func ResolveNativeMinPatterns(nativeMinPatterns *int) int {
	if nativeMinPatterns != nil {
		if *nativeMinPatterns < 1 {
			return 1
		}
		return *nativeMinPatterns
	}
	rawValue := strings.TrimSpace(os.Getenv(nativeMatcherMinEnv))
	if rawValue != "" {
		if parsed, err := strconv.Atoi(rawValue); err == nil {
			if parsed < 1 {
				return 1
			}
			return parsed
		}
	}
	return DefaultNativeMatcherMinPatterns
}

// AddPattern 对齐 add_pattern，空模式直接忽略
func (a *AhoCorasick) AddPattern(pattern string) {
	if pattern == "" {
		return
	}
	a.pythonBuilt = false
	if a.patterns == nil {
		a.patterns = map[string]struct{}{}
	}
	a.patterns[pattern] = struct{}{}

	state := 0
	for _, char := range pattern {
		nextState, ok := a.nextStates[state][char]
		if !ok {
			nextState = len(a.nextStates)
			a.nextStates[state][char] = nextState
			a.nextStates = append(a.nextStates, map[rune]int{})
			a.fail = append(a.fail, 0)
			a.output = append(a.output, map[string]struct{}{})
		}
		state = nextState
	}
	a.output[state][pattern] = struct{}{}
}

// Build 对齐 build，构建失败指针与输出合并
func (a *AhoCorasick) Build() {
	a.buildPythonMatcher()
}

// buildPythonMatcher 对齐 _build_python_matcher
func (a *AhoCorasick) buildPythonMatcher() {
	if a.pythonBuilt {
		return
	}

	queue := make([]int, 0, len(a.nextStates))
	for _, state := range a.nextStates[0] {
		queue = append(queue, state)
		a.fail[state] = 0
	}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for char, next := range a.nextStates[current] {
			queue = append(queue, next)
			state := a.fail[current]
			for {
				if _, ok := a.nextStates[state][char]; ok {
					break
				}
				if state == 0 {
					break
				}
				state = a.fail[state]
			}
			failState, ok := a.nextStates[state][char]
			if !ok {
				failState = 0
			}
			a.fail[next] = failState
			for pattern := range a.output[failState] {
				a.output[next][pattern] = struct{}{}
			}
		}
	}
	a.pythonBuilt = true
}

// Search 对齐 search，返回 (匹配结束码点索引, 模式)
// 同一结束位置内按模式字典序返回，避免 Go map 迭代顺序导致的随机结果
func (a *AhoCorasick) Search(text string) []AhoCorasickMatch {
	a.buildPythonMatcher()

	results := make([]AhoCorasickMatch, 0)
	state := 0
	for index, char := range []rune(text) {
		for {
			if _, ok := a.nextStates[state][char]; ok {
				break
			}
			if state == 0 {
				break
			}
			state = a.fail[state]
		}
		nextState, ok := a.nextStates[state][char]
		if !ok {
			nextState = 0
		}
		state = nextState

		patterns := make([]string, 0, len(a.output[state]))
		for pattern := range a.output[state] {
			patterns = append(patterns, pattern)
		}
		sort.Strings(patterns)
		for _, pattern := range patterns {
			results = append(results, AhoCorasickMatch{End: index, Pattern: pattern})
		}
	}
	return results
}

// FindAll 对齐 find_all，统计每个模式的出现次数
func (a *AhoCorasick) FindAll(text string) map[string]int {
	stats := make(map[string]int, len(a.patterns))
	for _, match := range a.Search(text) {
		stats[match.Pattern]++
	}
	return stats
}

// Contains 判断文本中是否出现任意模式
func (a *AhoCorasick) Contains(text string) bool {
	if len(a.patterns) == 0 {
		return false
	}
	return len(a.Search(text)) > 0
}

// Patterns 返回全部模式，按字典序排序
func (a *AhoCorasick) Patterns() []string {
	patterns := make([]string, 0, len(a.patterns))
	for pattern := range a.patterns {
		patterns = append(patterns, pattern)
	}
	sort.Strings(patterns)
	return patterns
}

// PythonBuilt 返回纯 Go 自动机是否已构建
func (a *AhoCorasick) PythonBuilt() bool {
	return a.pythonBuilt
}

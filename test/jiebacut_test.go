package test

import (
	"bytes"
	"compress/gzip"
	"os"
	"strings"
	"testing"

	"maibot-go/internal/chat/replyer/jiebacut"
)

// loadTestJiebaDictionary 读取 replyer 内嵌的 jieba 词典
func loadTestJiebaDictionary(t *testing.T) *jiebacut.Dictionary {
	t.Helper()
	raw, err := os.ReadFile("../internal/chat/replyer/data/jieba_dict.txt.gz")
	if err != nil {
		t.Skipf("缺少 jieba 词典，跳过: %v", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("解压词典失败: %v", err)
	}
	defer func() { _ = reader.Close() }()
	dict, err := jiebacut.NewDictionary(reader)
	if err != nil {
		t.Fatalf("加载词典失败: %v", err)
	}
	return dict
}

// TestJiebaCutMatchesPythonOnNumbersAndPercents 固定 HMM 分块口径。
//
// Python jieba finalseg 的非汉字段只按 ([a-zA-Z0-9]+(?:\.\d+)?%?) 切分，
// 且不匹配的片段整体保留；jiebago 的 finalseg 用的是
// (\d+\.\d+|[a-zA-Z0-9]+)，会把 "50%" 拆成 "50" + "%"、"v1.2" 拆成
// "v1" + "." + "2"。这里的期望值取自 Python jieba 0.42.1 的实际输出。
func TestJiebaCutMatchesPythonOnNumbersAndPercents(t *testing.T) {
	dict := loadTestJiebaDictionary(t)
	cases := []struct {
		sentence string
		want     []string
	}{
		{
			"这次活动增长50%以上，效果很好",
			[]string{"这次", "活动", "增长", "50%", "以上", "，", "效果", "很", "好"},
		},
		{
			"版本是v1.2-beta，记得更新",
			[]string{"版本", "是", "v1.2", "-", "beta", "，", "记得", "更新"},
		},
		{
			"股价涨了3.5%，大家都很开心",
			[]string{"股价", "涨", "了", "3.5%", "，", "大家", "都", "很", "开心"},
		},
		{
			"微信支付扣了0.5%手续费",
			[]string{"微信", "支付", "扣", "了", "0.5%", "手续费"},
		},
	}
	for _, tc := range cases {
		got := dict.Cut(tc.sentence)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Fatalf("分词结果与 Python 不一致\n句子: %s\n实际: %v\n期望: %v", tc.sentence, got, tc.want)
		}
	}
}

// TestJiebaCutForSearchKeepsShortWords 搜索模式在精确模式基础上补二字/三字词
func TestJiebaCutForSearchKeepsShortWords(t *testing.T) {
	dict := loadTestJiebaDictionary(t)
	got := dict.CutForSearch("今天天气真不错")
	if len(got) == 0 {
		t.Fatal("搜索模式应返回非空结果")
	}
	joined := strings.Join(got, "|")
	if !strings.Contains(joined, "今天") {
		t.Fatalf("搜索模式应包含二字词，实际: %v", got)
	}
}

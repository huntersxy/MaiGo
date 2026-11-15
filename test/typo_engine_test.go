package test

import (
	"context"
	"errors"
	"testing"

	"MaiGo/internal/chat/replyer"
	"MaiGo/internal/services/pythoncompat"
)

// stubTypoEngine 记录入参并返回固定结果，用于验证 applyTypoPostProcess 的接线
type stubTypoEngine struct {
	gotSentences []string
	results      []replyer.TypoResult
	err          error
	calls        int
}

func (e *stubTypoEngine) CreateTypoSentences(_ context.Context, sentences []string, _ replyer.TypoConfig) ([]replyer.TypoResult, error) {
	e.calls++
	e.gotSentences = append([]string(nil), sentences...)
	return e.results, e.err
}

// TestTypoEngineReceivesAllSentences 确认整批句子一次交给注入后端，
// 而不是逐句往返（侧车模式下这是每回复只发一次 HTTP 的前提）。
func TestTypoEngineReceivesAllSentences(t *testing.T) {
	engine := &stubTypoEngine{results: []replyer.TypoResult{
		{Text: "错字一"},
		{Text: "错字二", Correction: "更正"},
	}}
	engine.results[0].Text = "错字一"

	result := replyer.ApplyPipelineForTest(context.Background(), "第一句。第二句。", replyer.Options{
		Config:     replyer.DefaultConfig(),
		TypoEngine: engine,
	})
	if engine.calls != 1 {
		t.Fatalf("expected a single batched call, got %d", engine.calls)
	}
	if len(engine.gotSentences) == 0 {
		t.Fatal("engine should receive the split sentences")
	}
	if result == nil {
		t.Fatal("pipeline should return segments")
	}
}

// TestTypoEngineErrorSkipsTypo 注入失败时应保持原文本，不伪造错字
func TestTypoEngineErrorSkipsTypo(t *testing.T) {
	engine := &stubTypoEngine{err: errors.New("boom")}
	result := replyer.ApplyPipelineForTest(context.Background(), "这句话不该被改动。", replyer.Options{
		Config:     replyer.DefaultConfig(),
		TypoEngine: engine,
	})
	if len(result) == 0 {
		t.Fatal("expected at least one segment")
	}
	for _, segment := range result {
		if segment.IsCorrection {
			t.Fatalf("失败时不应生成纠正段: %+v", segment)
		}
	}
}

// TestSidecarTypoEngineUnconfiguredFallsBack 未配置侧车时本地实现兜底，
// 不返回错误（保证错别字功能在无侧车环境仍可用）。
func TestSidecarTypoEngineUnconfiguredFallsBack(t *testing.T) {
	pythoncompat.SetDefaultClient(pythoncompat.NewClient(pythoncompat.Config{}))
	t.Cleanup(func() { pythoncompat.SetDefaultClient(nil) })

	engine := &stubTypoEngine{results: []replyer.TypoResult{{Text: "回落到本地"}}}
	result := replyer.ApplyPipelineForTest(context.Background(), "测试句子。", replyer.Options{
		Config:     replyer.DefaultConfig(),
		TypoEngine: engine,
	})
	if len(result) == 0 || result[0].Text != "回落到本地" {
		t.Fatalf("unexpected segments: %+v", result)
	}
}

package utils

// 本轮 A_Memorix 内核修复的局部回归测试

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestApplySafeContentDedupKeepsContainedLongerText(t *testing.T) {
	results := []RetrievalResult{
		{HashValue: "h1", Content: "小明喜欢喝咖啡"},
		{HashValue: "h2", Content: "小明喜欢喝咖啡，也喜欢喝茶"},
	}
	deduped, removed := ApplySafeContentDedup(results)
	if len(deduped) != 2 || removed != 0 {
		t.Fatalf("包含关系不应判重: kept=%d removed=%d", len(deduped), removed)
	}

	duplicates := []RetrievalResult{
		{HashValue: "h1", Content: "同一段正文"},
		{HashValue: "h2", Content: "同一段正文"},
	}
	deduped, removed = ApplySafeContentDedup(duplicates)
	if len(deduped) != 1 || removed != 1 {
		t.Fatalf("全文相等应判重: kept=%d removed=%d", len(deduped), removed)
	}
}

func TestNormalizeSummaryFactsRejectsInvalidFacts(t *testing.T) {
	cases := []struct {
		name string
		raw  any
	}{
		{"非数组", map[string]any{}},
		{"非对象项", []any{"fact"}},
		{"fact_id 非字符串", []any{map[string]any{"fact_id": 1, "text": "内容"}}},
		{"空 fact_id", []any{map[string]any{"fact_id": " ", "text": "内容"}}},
		{"空 text", []any{map[string]any{"fact_id": "f1", "text": ""}}},
		{"重复 fact_id", []any{
			map[string]any{"fact_id": "f1", "text": "内容一"},
			map[string]any{"fact_id": "f1", "text": "内容二"},
		}},
	}
	for _, testCase := range cases {
		if _, err := normalizeSummaryFacts(testCase.raw); err == nil {
			t.Fatalf("%s: 期望 rejected", testCase.name)
		}
	}

	facts, err := normalizeSummaryFacts([]any{map[string]any{
		"fact_id": " f1 ",
		"text":    " 有效事实 ",
		"image_evidence": []any{
			map[string]any{"message_id": " m1 ", "component_path": " p1 "},
			"invalid",
		},
	}})
	if err != nil {
		t.Fatalf("合法事实解析失败: %v", err)
	}
	if len(facts) != 1 || facts[0].FactID != "f1" || facts[0].Text != "有效事实" {
		t.Fatalf("事实字段未按预期归一: %+v", facts)
	}
	if len(facts[0].ImageEvidence) != 1 || facts[0].ImageEvidence[0].MessageID != "m1" {
		t.Fatalf("图片证据未按预期归一: %+v", facts[0].ImageEvidence)
	}
}

func TestFactDedupInterleaveChannelsPrioritizesExact(t *testing.T) {
	exact := factDedupCandidate{ID: "exact"}
	channelA := []factDedupCandidate{{ID: "a1"}, {ID: "a2"}}
	channelB := []factDedupCandidate{{ID: "b1"}}
	channelC := []factDedupCandidate{{ID: "c1"}, {ID: "c2"}}

	merged := factDedupInterleaveChannels(&exact, channelA, channelB, channelC)
	want := []string{"exact", "a1", "b1", "c1", "a2", "c2"}
	if len(merged) != len(want) {
		t.Fatalf("交错长度不符: got=%d want=%d", len(merged), len(want))
	}
	for index, id := range want {
		if merged[index].ID != id {
			t.Fatalf("第 %d 项应为 %s，实际 %s", index, id, merged[index].ID)
		}
	}
}

func TestResolveFastTextGenerationModel(t *testing.T) {
	tasks := map[string]TextGenerationTaskConfig{
		"memory":     {ModelList: []string{"m"}},
		"utils":      {ModelList: []string{"u"}},
		"fast_model": {ModelList: []string{"f"}},
	}
	model, err := ResolveFastTextGenerationModel(tasks)
	if err != nil || model.TaskName != "fast_model" {
		t.Fatalf("应优先 fast_model: model=%+v err=%v", model, err)
	}

	delete(tasks, "fast_model")
	model, err = ResolveFastTextGenerationModel(tasks)
	if err != nil || model.TaskName != "utils" {
		t.Fatalf("缺少 fast_model 时应回退 utils: model=%+v err=%v", model, err)
	}

	if _, err := ResolveFastTextGenerationModel(map[string]TextGenerationTaskConfig{
		"memory": {ModelList: []string{"m"}},
	}); err == nil {
		t.Fatal("没有 fast_model/utils 时应报错")
	}
}

func TestLLMRetryConfigCountsFirstAttempt(t *testing.T) {
	manager := &WebImportManager{deps: WebImportDeps{
		Config: func(key string, fallback any) any {
			if key == "web.import.llm_retry.max_attempts" {
				return 4
			}
			return fallback
		},
	}}
	if retries := int(manager.llmRetryConfig()["retries"]); retries != 3 {
		t.Fatalf("max_attempts=4 应产生 3 次额外重试，实际 %d", retries)
	}

	managerZero := &WebImportManager{deps: WebImportDeps{
		Config: func(key string, fallback any) any {
			if key == "web.import.llm_retry.max_attempts" {
				return 0
			}
			return fallback
		},
	}}
	if retries := int(managerZero.llmRetryConfig()["retries"]); retries != 0 {
		t.Fatalf("max_attempts 至少包含首次调用，实际重试 %d", retries)
	}
}

func TestCancelAndWaitForTasksHonorsContext(t *testing.T) {
	cancelled := 0
	first := make(chan struct{})
	second := make(chan struct{})
	close(first)
	close(second)
	tasks := []StoppableTask{
		{Name: "first", Cancel: func() { cancelled++ }, Done: first},
		{Name: "second", Cancel: func() { cancelled++ }, Done: second},
	}
	if err := CancelAndWaitForTasks(context.Background(), "关闭任务", tasks, nil); err != nil {
		t.Fatalf("任务已退出时不应报错: %v", err)
	}
	if cancelled != 2 {
		t.Fatalf("未取消全部任务: %d", cancelled)
	}

	open := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := CancelAndWaitForTasks(ctx, "关闭任务", []StoppableTask{{Name: "open", Done: open}}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回上下文取消: %v", err)
	}

	var logged string
	err = cancelAndWaitForTasksWithTimeout(context.Background(), "关闭任务",
		[]StoppableTask{{Name: "stuck", Done: open}}, 10*time.Millisecond,
		func(message string) { logged = message })
	if err == nil {
		t.Fatal("任务未退出时应超时")
	}
	if !strings.Contains(err.Error(), "stuck") || !strings.Contains(logged, "stuck") {
		t.Fatalf("超时报告应包含未退出任务: err=%v log=%s", err, logged)
	}
}

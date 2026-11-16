package replyer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"MaiGo/internal/llm_models"
	"MaiGo/internal/maisaka/display"
)

func TestBuildReplyMonitorDetailIncludesPromptHTMLURI(t *testing.T) {
	result := ReplyGenerationResult{
		Completion: LLMCompletionResult{
			RequestPrompt: "prompt", ResponseText: "reply", ReasoningText: "reason",
			ModelName: "model", PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15,
		},
		Metrics:               GenerationMetrics{PromptMS: 1, LLMMS: 2, OverallMS: 3, StageLogs: []string{"stage"}},
		RequestMessageCount:   1,
		RequestMessages:       []llm.ContextItem{llm.NewUserMessageItem(llm.ContextContentPart{Type: llm.ContextPartText, Text: "hi"})},
		OutputItems:           []llm.ContextItem{llm.NewAssistantMessageItem("turn", llm.ContextContentPart{Type: llm.ContextPartText, Text: "reply"})},
		GenerationAttempts:    []GenerationAttempt{{WorkflowPurpose: "replyer", WorkflowAttempt: 1}},
		SelectedExpressionIDs: []int{1},
		SelectedExpressionDetails: []map[string]any{
			{"id": 1, "situation": "日常", "style": "简短"},
		},
		PromptHTMLURI: "/api/webui/config/maisaka-prompt-preview?path=x",
	}
	detail := BuildReplyMonitorDetail(result)
	if detail["prompt_html_uri"] != result.PromptHTMLURI {
		t.Fatalf("缺少 prompt_html_uri: %#v", detail)
	}
	if detail["output_text"] != "reply" || detail["reasoning_text"] != "reason" {
		t.Fatalf("正文或推理缺失: %#v", detail)
	}
	metrics, _ := detail["metrics"].(map[string]any)
	if metrics["model_name"] != "model" || metrics["total_tokens"] != 15 {
		t.Fatalf("指标不符: %#v", metrics)
	}
	sections, _ := detail["extra_sections"].([]map[string]any)
	if len(sections) != 2 {
		t.Fatalf("附加段落不符: %#v", sections)
	}
}

func TestPersistStagePreviewWritesPromptHTMLURI(t *testing.T) {
	root := t.TempDir()
	generator := &ReplyGenerator{
		Builder:       &PromptBuilder{Config: GeneratorConfig{SaveReasoning: true}},
		PreviewLogger: display.NewPromptPreviewLogger(root),
	}
	result := &ReplyGenerationResult{}
	generator.persistStagePreview(context.Background(), result, "replyer_creative", "session-1", "reason",
		[]llm.ContextItem{llm.NewUserMessageItem(llm.ContextContentPart{Type: llm.ContextPartText, Text: "hi"})},
		[]llm.ContextItem{llm.NewAssistantMessageItem("turn", llm.ContextContentPart{Type: llm.ContextPartText, Text: "reply"})},
		[]GenerationAttempt{{WorkflowPurpose: "replyer.ja-JP", WorkflowAttempt: 1}},
		12.5, "model", 10, 5, 15)
	if result.PromptHTMLURI == "" {
		t.Fatal("阶段预览未写入 prompt_html_uri")
	}
	matches, err := filepath.Glob(filepath.Join(root, "*", "*", "*.json"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("阶段预览文件未生成: %v", err)
	}
	if _, err := os.Stat(matches[0]); err != nil {
		t.Fatalf("阶段预览文件不可读: %v", err)
	}
}

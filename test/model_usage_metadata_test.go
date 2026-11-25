package test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"MaiGo/internal/llm_models"
	"MaiGo/internal/llm_models/contract"

	"github.com/cloudwego/eino/schema"
)

func TestChatUsageKeepsProviderReportedCacheMiss(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   "test-model",
			"choices": []any{map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": "ok",
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens":            100,
				"completion_tokens":        10,
				"total_tokens":             110,
				"prompt_cache_hit_tokens":  20,
				"prompt_cache_miss_tokens": 77,
			},
		})
	}))
	defer server.Close()

	client, err := llm.NewClient(llm.ModelConfig{
		ClientType: "openai",
		BaseURL:    server.URL,
		APIKey:     "test-key",
		Model:      "test-model",
		Timeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := client.Generate(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("ping")})
	if err != nil {
		t.Fatal(err)
	}
	if message == nil || message.ResponseMeta == nil {
		t.Fatal("response metadata is missing")
	}
	details, ok := message.ResponseMeta.Extension.(*contract.TokenUsageDetails)
	if !ok || details == nil {
		t.Fatalf("usage details = %#v, want contract.TokenUsageDetails", message.ResponseMeta.Extension)
	}
	if details.PromptCacheHitTokens != 20 || details.PromptCacheMissTokens != 77 || !details.PromptCacheReported {
		t.Fatalf("usage details = %+v", *details)
	}
}

package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"MaiGo/internal/A_memorix/core/utils"
	"MaiGo/internal/A_memorix/hostadapter"
	"MaiGo/internal/config/maibot"
	"MaiGo/internal/config/model_profiles"
	"MaiGo/internal/llm_models"
	"MaiGo/internal/maisaka"
	maisakaruntime "MaiGo/internal/maisaka/runtime"

	"github.com/cloudwego/eino/schema"
)

func TestPlannerDoesNotFallBackToReplyer(t *testing.T) {
	profiles, err := modelprofiles.NewStaticStore("replyer-only", "openai", "http://127.0.0.1:1/v1", "replyer-only", "fake-key")
	if err != nil {
		t.Fatal(err)
	}
	for _, candidates := range [][]string{nil, {"missing"}} {
		config := &maibot.ModelConfigFile{ModelTaskConfig: maibot.ModelTaskConfig{
			Replyer: maibot.TaskConfig{ModelList: []string{"replyer-only"}},
			Planner: maibot.TaskConfig{ModelList: candidates},
		}}
		router := llm.NewRouter(profiles, config)
		if _, err := router.Resolve(t.Context(), llm.TaskPlanner); err == nil {
			t.Fatalf("planner %v resolved outside its task", candidates)
		}
		if _, client, err := router.NewClientForTaskWithFailover(t.Context(), llm.TaskPlanner); client != nil || err == nil {
			t.Fatalf("planner %v returned a client: %v", candidates, err)
		}
		if _, client, err := router.NewClientForTaskWithFailover(t.Context(), llm.TaskReplyer); client == nil || err != nil {
			t.Fatalf("replyer task lost its client: %v", err)
		}
	}
}

func TestPlannerCompactionKeepsSuccessfulSnapshotAndRetriesInvalidJSON(t *testing.T) {
	var attempts []maisaka.ChatRequest
	compactionClient := &maisakaruntime.AgentChatClient{ChatFunc: func(_ context.Context, request maisaka.ChatRequest) (maisaka.ChatResponse, error) {
		attempts = append(attempts, request)
		if len(attempts) == 1 {
			return maisaka.ChatResponse{Content: `{"topics":["one"ç]}`}, nil
		}
		return maisaka.ChatResponse{Content: `{"topics":[" one ","one"],"unused":"ignored"}`}, nil
	}}
	planner := &maisakaruntime.AgentChatClient{ChatFunc: func(_ context.Context, _ maisaka.ChatRequest) (maisaka.ChatResponse, error) {
		return maisaka.ChatResponse{Content: "ok", RequestClient: compactionClient, Usage: maisaka.ChatUsage{PromptTokens: 37}}, nil
	}}
	firstLoop := maisaka.NewChatLoopService("chat", true, nil, planner)
	firstLoop.MaxImageSide = 640
	runtime := maisaka.NewRuntime(maisaka.RuntimeOptions{SessionID: "chat", ChatLoop: firstLoop})
	history := []maisaka.ContextMessage{{Role: maisaka.RoleUser, Source: maisaka.SourceUser, Content: "chat message", CountInContext: true}}
	if _, err := firstLoop.ChatLoopStep(t.Context(), history, "planner", 10, nil, "system", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	secondLoop := maisaka.NewChatLoopService("chat", true, nil, nil)
	runtime.SetChatLoop(secondLoop)
	snapshot := runtime.PlannerRequestSnapshot()
	if snapshot == nil || snapshot.Client != compactionClient || secondLoop.LastPlannerPromptTokens() != 37 {
		t.Fatalf("prior request was lost on model refresh: %+v", snapshot)
	}
	compactor := &maisakaruntime.AgentHistoryCompactor{Agent: &maisakaruntime.Agent{}}
	summary, err := compactor.Summarize(t.Context(), maisaka.HistoryCompactionInput{Removed: history, Snapshot: snapshot})
	if err != nil || summary == nil || summary.Content != "当前话题:\n- one" {
		t.Fatalf("compaction result: %+v, %v", summary, err)
	}
	if len(attempts) != 2 || attempts[1].RequestKind != "maisaka.context_compaction" || attempts[1].MaxImageSide != 640 ||
		!reflect.DeepEqual(attempts[0].Items[:len(snapshot.Items)], snapshot.Items) ||
		!reflect.DeepEqual(attempts[1].Items[:len(attempts[0].Items)], attempts[0].Items) ||
		!strings.Contains(attempts[1].Items[len(attempts[1].Items)-1].Text, "禁止调用任何工具") {
		t.Fatalf("unexpected compaction attempts: %+v", attempts)
	}
	runtime.ClearPlannerRequestSnapshot()
	if runtime.PlannerRequestSnapshot() != nil || secondLoop.LastPlannerPromptTokens() != 0 {
		t.Fatal("compaction did not clear request state")
	}
	if _, err := maisaka.ParseConversationState(`{"topics":[null]}`); err == nil {
		t.Fatal("malformed summary accepted")
	}
}

func TestNativeMemorixUsesReloadedTasksAndModels(t *testing.T) {
	t.Setenv("MAIBOT_INSTANCE_DIR", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(writer, `{"choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, body.Model)
	}))
	defer server.Close()
	profiles, err := modelprofiles.NewStaticStore("unused", "openai", server.URL, "unused", "fake-key")
	if err != nil {
		t.Fatal(err)
	}
	var current atomic.Pointer[maibot.ModelConfigFile]
	configFor := func(modelName string, taskConfig maibot.ModelTaskConfig) *maibot.ModelConfigFile {
		return &maibot.ModelConfigFile{
			ApiProviders:    []maibot.APIProvider{{Name: "mock", BaseUrl: server.URL + "/v1", ClientType: "openai", ApiKey: "fake-key"}},
			Models:          []maibot.ModelInfo{{Name: modelName, ModelIdentifier: modelName, ApiProvider: "mock"}},
			ModelTaskConfig: taskConfig,
		}
	}
	current.Store(configFor("before", maibot.ModelTaskConfig{Utils: maibot.TaskConfig{ModelList: []string{"before"}}}))
	gateway := hostadapter.NewNativeLLMGateway(profiles, current.Load)
	classifier := utils.NewPersonProfileClassificationLLM(gateway, nil)
	assertClassification := func(wantTask, wantModel string) {
		t.Helper()
		resolved, err := classifier.ResolveModel()
		if err != nil || resolved.TaskName != wantTask || !slices.Equal(resolved.TaskConfig.ModelList, []string{wantModel}) {
			t.Fatalf("task resolution: %+v, %v", resolved, err)
		}
		if _, exists := gateway.ModelsDict()[wantModel]; !exists {
			t.Fatalf("model %q absent from current dictionary", wantModel)
		}
		got, err := classifier.GeneratePersonProfileClassification(t.Context(), "classify", 0, 32)
		if err != nil || got != wantModel {
			t.Fatalf("generation used %q, want %q: %v", got, wantModel, err)
		}
	}
	assertClassification("utils", "before")
	current.Store(configFor("after", maibot.ModelTaskConfig{FastModel: maibot.TaskConfig{ModelList: []string{"after"}}}))
	assertClassification("fast_model", "after")
}

func TestPlannerSnapshotPinsSuccessfulFailoverClient(t *testing.T) {
	t.Setenv("MAIBOT_INSTANCE_DIR", t.TempDir())
	var primaryCalls, fallbackCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		if body.Model == "primary" {
			primaryCalls.Add(1)
			writer.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(writer, `{"error":{"message":"unavailable"}}`)
			return
		}
		fallbackCalls.Add(1)
		fmt.Fprint(writer, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	profiles, err := modelprofiles.NewStaticStore("unused", "openai", server.URL, "unused", "fake-key")
	if err != nil {
		t.Fatal(err)
	}
	config := &maibot.ModelConfigFile{
		ApiProviders: []maibot.APIProvider{{Name: "mock", BaseUrl: server.URL + "/v1", ClientType: "openai", ApiKey: "fake-key"}},
		Models: []maibot.ModelInfo{
			{Name: "primary", ModelIdentifier: "primary", ApiProvider: "mock"},
			{Name: "fallback", ModelIdentifier: "fallback", ApiProvider: "mock"},
		},
		ModelTaskConfig: maibot.ModelTaskConfig{Planner: maibot.TaskConfig{ModelList: []string{"primary", "fallback"}, SelectionStrategy: "sequential"}},
	}
	router := llm.NewRouter(profiles, config)
	_, client, err := router.NewClientForTaskWithFailover(t.Context(), llm.TaskPlanner)
	if err != nil {
		t.Fatal(err)
	}
	ctx, successful := llm.WithSuccessfulModel(t.Context())
	input := []*schema.AgenticMessage{schema.UserAgenticMessage("plan")}
	if _, err := client.Generate(ctx, input); err != nil || successful.Name != "fallback" || successful.Client == nil {
		t.Fatalf("successful model not captured: %+v, %v", successful, err)
	}
	if _, err := successful.Client.Generate(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if primaryCalls.Load() != 1 || fallbackCalls.Load() != 2 {
		t.Fatalf("requests primary=%d fallback=%d", primaryCalls.Load(), fallbackCalls.Load())
	}
}

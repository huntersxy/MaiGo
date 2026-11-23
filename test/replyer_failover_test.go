package test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"MaiGo/internal/chat/replyer"
	"MaiGo/internal/config/maibot"
	"MaiGo/internal/config/model_profiles"
	"MaiGo/internal/llm_models"
)

type replyerRoutingFixture struct{ router *llm.Router }

func (f replyerRoutingFixture) ResolveModel(ctx context.Context, name string) (replyer.ResolvedModel, error) {
	_, client, err := f.router.NewClientForTaskWithFailoverAndModel(ctx, llm.TaskReplyer, name)
	return replyer.ResolvedModel{Model: client, Name: name, Info: f.ModelInfo(name)}, err
}

func (f replyerRoutingFixture) ClientForTask(context.Context, string) (replyer.LLMClient, error) {
	return nil, fmt.Errorf("unexpected task override")
}

func (f replyerRoutingFixture) ModelInfo(name string) replyer.ModelInfo {
	return replyer.ModelInfo{Name: name, ModelIdentifier: name, APIProvider: name, ClientType: "openai", Visual: name == "primary"}
}

func TestReplyerFailoverBuildsContextForTheSelectedModel(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadRequest} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Setenv("MAIBOT_INSTANCE_DIR", t.TempDir())
			var primaryAttempts, backupAttempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model    string `json:"model"`
					Messages []struct {
						Content json.RawMessage `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) != 1 {
					t.Errorf("invalid model request: %+v, %v", body, err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if body.Model == "primary" {
					primaryAttempts.Add(1)
					w.Header().Set("X-Request-Id", "upstream-primary")
					w.WriteHeader(status)
					fmt.Fprint(w, `{"error":{"message":"upstream failure"}}`)
					return
				}
				backupAttempts.Add(1)
				if body.Model != "backup" || string(body.Messages[0].Content) != `"backup context"` {
					t.Errorf("backup received unexpected input: %+v", body)
				}
				fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ready"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
			}))
			defer server.Close()
			profiles, err := modelprofiles.NewStaticStore("unused", "openai", server.URL, "unused", "fake-key")
			if err != nil {
				t.Fatal(err)
			}
			config := &maibot.ModelConfigFile{
				ApiProviders: []maibot.APIProvider{{Name: "mock", BaseUrl: server.URL + "/v1", ClientType: "openai", ApiKey: "fake-key", MaxRetry: 2, RetryInterval: 1}},
				Models: []maibot.ModelInfo{
					{Name: "primary", ModelIdentifier: "primary", ApiProvider: "mock"},
					{Name: "backup", ModelIdentifier: "backup", ApiProvider: "mock"},
				},
				ModelTaskConfig: maibot.ModelTaskConfig{Replyer: maibot.TaskConfig{
					ModelList: []string{"primary", "backup"}, SelectionStrategy: "sequential",
				}},
			}
			router := llm.NewRouter(profiles, config)
			ctx := context.Background()
			route, model, err := router.NewClientForTaskWithFailover(ctx, llm.TaskReplyer)
			if err != nil {
				t.Fatal(err)
			}
			client := replyer.NewAgenticLLMClient(replyerRoutingFixture{router}, "replyer", model, route.MaiBotModel)
			var built []string
			build := func(_ context.Context, info *replyer.ModelInfo) ([]llm.ContextItem, error) {
				built = append(built, info.Name)
				return []llm.ContextItem{llm.NewUserMessageItem(llm.ContextContentPart{Type: llm.ContextPartText, Text: info.Name + " context"})}, nil
			}
			result, err := client.GenerateResponseWithContext(ctx, build, replyer.GenerationOptions{})
			if err != nil || result.ModelName != "backup" || result.Response != "ready" {
				t.Fatalf("reply: %+v, %v", result, err)
			}
			if !reflect.DeepEqual(built, []string{"primary", "backup"}) {
				t.Fatalf("context factory calls: %v", built)
			}
			wantPrimaryAttempts := int32(1)
			if status == http.StatusInternalServerError {
				wantPrimaryAttempts = 2
			}
			if primaryAttempts.Load() != wantPrimaryAttempts || backupAttempts.Load() != 1 {
				t.Fatalf("request counts primary=%d backup=%d", primaryAttempts.Load(), backupAttempts.Load())
			}
			_, err = client.GenerateResponseWithContext(ctx, build, replyer.GenerationOptions{ModelName: "primary"})
			var upstream *llm.HTTPStatusError
			if !errors.As(err, &upstream) || upstream.StatusCode != status || upstream.RequestID != "upstream-primary" {
				t.Fatalf("explicit model error: %v; provider details: %+v", err, upstream)
			}
			if backupAttempts.Load() != 1 || primaryAttempts.Load() != 2*wantPrimaryAttempts {
				t.Fatalf("explicit model request counts primary=%d backup=%d", primaryAttempts.Load(), backupAttempts.Load())
			}
		})
	}
}

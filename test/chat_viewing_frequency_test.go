package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	messagereceive "MaiGo/internal/chat/message_receive"
	chatwiring "MaiGo/internal/chat/wiring"
	memory "MaiGo/internal/common/database"
	migration "MaiGo/internal/common/database/migrations"
	maibotcfg "MaiGo/internal/config/maibot"
	modelprofiles "MaiGo/internal/config/model_profiles"
	"MaiGo/internal/maisaka"
	maisaruntime "MaiGo/internal/maisaka/runtime"
	mcp "MaiGo/internal/mcp_module"
	"MaiGo/internal/platform_io/drivers/onebot"
	"MaiGo/internal/runtime/execution/policy"
	webuichat "MaiGo/internal/webui/routers/chat"

	"github.com/go-chi/chi/v5"
)

const viewingFrequencyFixture = `[inner]
version = "8.16.25"

[chat.reply_timing]
talk_value = 0.0
private_reply_paused = true
private_reply_pause_rules = [
    { platform = "qq", item_id = "quiet-user" },
    { platform = "qq", item_id = "active-user", paused = false },
]

[chat.reply_timing.talk_rule_groups]
active_group = "default"

[[chat.reply_timing.talk_rule_groups.groups]]
name = "default"
rules = [
    { platform = "qq", item_id = "994571792", rule_type = "group", time = "00:00-23:59", value = 0.0 },
    { platform = "qq", item_id = "open-group", time = "*" },
]
`

func writeViewingFrequencyFixture(t *testing.T, root, contents string) {
	t.Helper()
	t.Setenv(maibotcfg.SharedModelsEnv, "")
	dir := filepath.Join(root, "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bot_config.toml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestChatViewingFrequencySurvivesWriteback(t *testing.T) {
	root := t.TempDir()
	writeViewingFrequencyFixture(t, root, viewingFrequencyFixture)
	opts := maibotcfg.LoadOptions{DisableLegacyMigration: true, ForceWriteback: true}
	if _, err := maibotcfg.LoadFromInstanceDirWithOptions(root, opts); err != nil {
		t.Fatal(err)
	}
	reloaded, err := maibotcfg.LoadFromInstanceDirWithOptions(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	timing := reloaded.Bot.Chat.ReplyTiming
	for target, want := range map[string]float64{"994571792": 0, "open-group": 0.5, "other-group": 0} {
		if got := maibotcfg.GroupTalkFrequency(timing, "qq", target); got != want {
			t.Errorf("group %s frequency = %v, want %v", target, got, want)
		}
	}
	for target, want := range map[string]float64{"quiet-user": 0, "active-user": 1, "unknown-user": 0} {
		if got := maibotcfg.PrivateTalkFrequency(timing, "qq", target); got != want {
			t.Errorf("private %s frequency = %v, want %v", target, got, want)
		}
	}
}

func TestChatViewingFrequencyUsesSessionIdentity(t *testing.T) {
	root := t.TempDir()
	contents := strings.Replace(viewingFrequencyFixture, "talk_value = 0.0", "talk_value = 0.112", 1)
	writeViewingFrequencyFixture(t, root, contents)
	holder, err := maibotcfg.NewInstanceHolder(root)
	if err != nil {
		t.Fatal(err)
	}
	database, err := memory.OpenDB(filepath.Join(root, "MaiBot.db"))
	if err != nil {
		t.Fatal(err)
	}
	connection, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if err := migration.RunMigrations(database); err != nil {
		t.Fatal(err)
	}
	manager, err := memory.NewManager(database, root)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := maisaruntime.New(manager, modelprofiles.NewStore(database), mcp.NewStore(database), policy.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agent.Stop)
	agent.SetInstanceHolder(holder)
	router := chi.NewRouter()
	webuichat.Mount(router, webuichat.Deps{Memory: manager, InstanceDir: root})
	for _, testCase := range []struct {
		kind, externalTarget, externalUser string
		targetID                           int64
		want                               float64
	}{
		{kind: memory.ConversationKindGroup, targetID: 994571792, want: 0},
		{kind: memory.ConversationKindGroup, targetID: 173, externalTarget: "open-group", want: 0.5},
		{kind: memory.ConversationKindPrivate, targetID: 174, externalTarget: "transport-user", externalUser: "active-user", want: 1},
	} {
		t.Run(testCase.kind+"/"+testCase.externalTarget, func(t *testing.T) {
			if err := manager.MarkConversationActive(t.Context(), testCase.kind, testCase.targetID, true); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			agent.HandleMessage(t.Context(), &onebot.ConversationMessage{
				ConversationKind: testCase.kind, TargetID: testCase.targetID, MessageID: testCase.targetID,
				UserID: 12, Nickname: "tester", Content: "hello", Time: now, ReceivedAt: now,
				Platform: "qq", ExternalTargetID: testCase.externalTarget, ExternalUserID: testCase.externalUser,
			})
			log, err := manager.WithContext(t.Context()).GetMessageLogByScope(testCase.kind, testCase.targetID, testCase.targetID)
			if err != nil || log == nil || log.ReplyFrequency == nil || *log.ReplyFrequency != testCase.want {
				t.Fatalf("message reply frequency mismatch: %+v, err=%v, want %v", log, err, testCase.want)
			}
			path := "/sessions/" + log.SessionID
			assertViewingFrequencyResponse(t, router, http.MethodGet, path, "", testCase.want)
			if testCase.externalTarget == "open-group" {
				assertViewingFrequencyResponse(t, router, http.MethodPut, path+"/talk-frequency", `{"value":0}`, 0)
				if err := holder.ReloadFromDisk([]maibotcfg.ReloadScope{maibotcfg.ReloadScopeBot}); err != nil {
					t.Fatal(err)
				}
				message := &messagereceive.SessionMessage{Platform: "qq", MessageInfo: messagereceive.MessageInfo{
					GroupInfo: &messagereceive.GroupInfo{GroupID: testCase.externalTarget},
				}}
				frequency, ok := chatwiring.ConfigFrequencyFallback(&holder.Config().Bot.Chat.ReplyTiming)(message)
				if !ok || frequency != 0 {
					t.Fatalf("message frequency = %v, ok=%v", frequency, ok)
				}
				result := maisaka.EvaluateMessageTurn(0, frequency, true, func() maisaka.TurnGateResult {
					t.Fatal("zero frequency reached the probability gate")
					return maisaka.TurnGateResult{}
				})
				if !result.ShouldTrigger() || !strings.Contains(result.Detail, "静默消费") {
					t.Fatalf("expected silent consumption, got %+v", result)
				}
			}
		})
	}
}

func assertViewingFrequencyResponse(t *testing.T, router http.Handler, method, path, body string, want float64) {
	t.Helper()
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(body)))
	var payload struct {
		Success bool `json:"success"`
		Detail  struct {
			Frequency struct {
				Effective    *float64 `json:"effective_value"`
				MatchedRules []struct {
					Effective bool    `json:"is_effective"`
					Value     float64 `json:"value"`
				} `json:"matched_rules"`
			} `json:"talk_frequency"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || response.Code != http.StatusOK || !payload.Success {
		t.Fatalf("%s %s failed: status=%d body=%s err=%v", method, path, response.Code, response.Body, err)
	}
	frequency := payload.Detail.Frequency
	if frequency.Effective == nil || *frequency.Effective != want {
		t.Fatalf("effective frequency should be %v: %s", want, response.Body)
	}
	if rules := frequency.MatchedRules; len(rules) > 0 && (!rules[0].Effective || rules[0].Value != want) {
		t.Fatalf("matched rule frequency should be %v: %+v", want, rules)
	}
}

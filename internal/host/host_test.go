package host

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenWorkspaceFreshCreatesFirstBot(t *testing.T) {
	root := t.TempDir()
	workspace, err := OpenWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	bots, err := workspace.Registry.ListBots()
	if err != nil {
		t.Fatal(err)
	}
	if len(bots) != 1 {
		t.Fatalf("新建工作区应只有一个 Bot，实际 %d", len(bots))
	}
	if bots[0].Initialization != "initialized" {
		t.Fatalf("Bot 初始化状态 = %s", bots[0].Initialization)
	}
	if err := ValidateWorkspaceBot(bots[0].Directory); err != nil {
		t.Fatalf("生成的配置不完整: %v", err)
	}
}

func TestOpenWorkspaceAdoptsExistingInstance(t *testing.T) {
	root := t.TempDir()
	if err := writeDefaultInstanceConfigs(filepath.Join(root, "config")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "config", "bot_config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "config", "bot_config.toml"),
		append([]byte("[bot]\nnickname = \"旧实例\"\n"), raw...), 0o666)
	workspace, err := OpenWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	bots, err := workspace.Registry.ListBots()
	if err != nil {
		t.Fatal(err)
	}
	if len(bots) != 1 || bots[0].Owned || bots[0].Directory != root {
		t.Fatalf("旧实例应原地登记: %#v", bots)
	}
	if bots[0].Name != "旧实例" {
		t.Fatalf("应沿用 Bot 昵称，实际 %s", bots[0].Name)
	}
}

func TestMovedWorkspaceIsRejected(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, MovedMarker), []byte("/new/location"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenWorkspace(root); err == nil {
		t.Fatal("已移动目录应拒绝启动")
	}
}

func TestCredentialsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "webui.json")
	credentials, err := NewCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.TokenSource() != "temporary" || !credentials.Verify(credentials.Token()) {
		t.Fatalf("临时口令状态不符: %s", credentials.TokenSource())
	}
	if err := credentials.Update("short"); err == nil {
		t.Fatal("短口令应被拒绝")
	}
	if err := credentials.Update("Abcdefgh1!"); err != nil {
		t.Fatal(err)
	}
	if credentials.TokenSource() != "configured" {
		t.Fatal("设置后应变为固定口令")
	}
	if err := credentials.CompleteSetup(); err != nil {
		t.Fatal(err)
	}
	if status := credentials.SetupStatus(); status["is_first_setup"] != false {
		t.Fatalf("首次设置状态 = %#v", status)
	}
}

func TestSessionsTicketAndRevoke(t *testing.T) {
	root := t.TempDir()
	credentials, err := NewCredentials(filepath.Join(root, "host", "webui.json"))
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := NewSessions(root, credentials, false)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := sessions.IssueTicket()
	if err != nil {
		t.Fatal(err)
	}
	session, ok := sessions.Exchange(ticket, "http://localhost")
	if !ok || !sessions.Valid(session) {
		t.Fatal("票据应换出有效会话")
	}
	if _, again := sessions.Exchange(ticket, ""); again {
		t.Fatal("票据不能重复使用")
	}
	sessions.Revoke(session)
	if sessions.Valid(session) {
		t.Fatal("注销后会话应失效")
	}
}

func TestRuntimeContextRoundTrip(t *testing.T) {
	contract := RuntimeContext{
		BotID: "0123456789abcdef0123456789abcdef", HTTPEndpoint: "/tmp/x.sock",
		PublicBaseURL: "http://127.0.0.1:8001/", StatusPath: "/tmp/status.json",
	}
	parsed, err := ParseRuntimeContext(contract.ToEnv())
	if err != nil {
		t.Fatal(err)
	}
	if parsed.PublicBaseURL != "http://127.0.0.1:8001" {
		t.Fatalf("public_base_url = %s", parsed.PublicBaseURL)
	}
	if _, err := ParseRuntimeContext(`{"bot_id":"bad"}`); err == nil {
		t.Fatal("无效契约应拒绝")
	}
}

func TestSupervisorCrashLoopStopsRestart(t *testing.T) {
	root := t.TempDir()
	workspace, err := OpenWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	bots, err := workspace.Registry.ListBots()
	if err != nil || len(bots) != 1 {
		t.Fatalf("工作区 Bot = %#v, err = %v", bots, err)
	}
	supervisor := NewSupervisor(workspace)
	instance := supervisor.runtime(bots[0])
	now := time.Now()
	instance.crashes = []time.Time{now, now, now, now, now}
	instance.mu.Lock()
	instance.bot.DesiredState = "running"
	instance.bot.RestartPolicy = "on-failure"
	instance.status = "starting"
	instance.mu.Unlock()
	if got := len(instance.crashes); got != crashLimit {
		t.Fatalf("崩溃计数 = %d", got)
	}
}

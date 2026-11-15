package test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"MaiGo/internal/runtime/execution"
)

func writeRuntimeToml(t *testing.T, instanceDir, content string) {
	t.Helper()
	path := execution.RuntimeTomlPath(instanceDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultProfileAndFeatures(t *testing.T) {
	profile := execution.DefaultProfile()
	if profile.Mode != execution.ModeCompatibility {
		t.Fatalf("expected default mode %s, got %s", execution.ModeCompatibility, profile.Mode)
	}
	if profile.WorkerPoolSize != 0 {
		t.Fatalf("expected default worker pool size 0, got %d", profile.WorkerPoolSize)
	}
	if !execution.NativeModeImplemented {
		t.Fatal("native mode should report its scheduler implementation as available")
	}
	if execution.NativeModeActive(profile, execution.DefaultFeatureToggles()) {
		t.Fatal("native mode must stay inactive by default")
	}
	nativeProfile := execution.Profile{Mode: execution.ModeNative}
	if execution.NativeModeActive(nativeProfile, execution.DefaultFeatureToggles()) {
		t.Fatal("native mode without an implemented engine must stay inactive")
	}
	schedulerToggles := execution.DefaultFeatureToggles()
	schedulerToggles.NativeSessionScheduler = true
	if !execution.NativeModeActive(nativeProfile, schedulerToggles) {
		t.Fatal("native mode with the session scheduler enabled must be active")
	}
	if execution.NativeModeActive(profile, schedulerToggles) {
		t.Fatal("compatibility mode must ignore native feature toggles")
	}

	toggles := execution.DefaultFeatureToggles()
	if toggles.NativeSessionScheduler ||
		toggles.NativeEventDispatcher ||
		toggles.NativeWriteCoordinator ||
		toggles.AsyncMemoryWriteback ||
		toggles.ParallelLLMCalls ||
		toggles.BatchDBWrites ||
		toggles.SQLiteReadPool {
		t.Fatalf("expected all default feature toggles to be false, got %+v", toggles)
	}

	infoList := toggles.ToInfoList()
	if len(infoList) == 0 {
		t.Fatal("expected non-empty feature info list")
	}
	for _, item := range infoList {
		if item.Enabled {
			t.Fatalf("expected feature %s to be disabled by default", item.Key)
		}
		if item.PythonState == "" || item.GoState == "" {
			t.Fatalf("expected feature %s to have state descriptions", item.Key)
		}
		if item.Phase <= 0 {
			t.Fatalf("expected feature %s to declare a roadmap phase", item.Key)
		}
		if item.Key == "native_session_scheduler" {
			if !item.Implemented {
				t.Fatalf("feature %s should be implemented after phase 2 wiring", item.Key)
			}
			continue
		}
		if item.Implemented {
			t.Fatalf("feature %s is marked implemented without runtime wiring", item.Key)
		}
	}
}

func TestLoadProfileDefaultWhenMissing(t *testing.T) {
	t.Setenv(execution.EnvExecutionMode, "")
	profile, err := execution.LoadProfile(t.TempDir())
	if err != nil {
		t.Fatalf("LoadProfile returned error: %v", err)
	}
	if profile.Mode != execution.ModeCompatibility {
		t.Fatalf("expected compatibility mode, got %s", profile.Mode)
	}
}

func TestLoadProfileEnvOverride(t *testing.T) {
	t.Setenv(execution.EnvExecutionMode, "native")
	profile, err := execution.LoadProfile(t.TempDir())
	if err != nil {
		t.Fatalf("LoadProfile returned error: %v", err)
	}
	if profile.Mode != execution.ModeNative {
		t.Fatalf("expected native mode from env override, got %s", profile.Mode)
	}
}

func TestLoadProfileRejectsInvalidMode(t *testing.T) {
	t.Setenv(execution.EnvExecutionMode, "")
	dir := t.TempDir()
	writeRuntimeToml(t, dir, "[execution]\nmode = \"turbo\"\n")

	_, err := execution.LoadProfile(dir)
	if !errors.Is(err, execution.ErrInvalidMode) {
		t.Fatalf("expected ErrInvalidMode, got %v", err)
	}
}

func TestLoadProfileRejectsMalformedConfig(t *testing.T) {
	t.Setenv(execution.EnvExecutionMode, "")
	dir := t.TempDir()
	writeRuntimeToml(t, dir, "[execution\nmode = \"native\"\n")

	_, err := execution.LoadProfile(dir)
	if !errors.Is(err, execution.ErrConfigParse) {
		t.Fatalf("expected ErrConfigParse, got %v", err)
	}
}

func TestLoadFeatureTogglesMalformedConfigReturnsError(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeToml(t, dir, "[features\nnative_session_scheduler = true\n")

	_, err := execution.LoadFeatureToggles(dir)
	if !errors.Is(err, execution.ErrConfigParse) {
		t.Fatalf("expected ErrConfigParse, got %v", err)
	}
}

func TestSaveAndLoadRoundtrip(t *testing.T) {
	t.Setenv(execution.EnvExecutionMode, "")
	dir := t.TempDir()

	profile := execution.Profile{Mode: execution.ModeNative, WorkerPoolSize: 8}
	if err := execution.SaveProfile(dir, profile); err != nil {
		t.Fatalf("SaveProfile failed: %v", err)
	}

	loadedProfile, err := execution.LoadProfile(dir)
	if err != nil {
		t.Fatalf("LoadProfile failed: %v", err)
	}
	if loadedProfile.Mode != execution.ModeNative || loadedProfile.WorkerPoolSize != 8 {
		t.Fatalf("loaded profile mismatch: %+v", loadedProfile)
	}

	toggles := execution.DefaultFeatureToggles()
	toggles.NativeSessionScheduler = true
	toggles.ParallelLLMCalls = true
	if err := execution.SaveFeatureToggles(dir, toggles); err != nil {
		t.Fatalf("SaveFeatureToggles failed: %v", err)
	}

	loadedToggles, err := execution.LoadFeatureToggles(dir)
	if err != nil {
		t.Fatalf("LoadFeatureToggles failed: %v", err)
	}
	if !loadedToggles.NativeSessionScheduler || !loadedToggles.ParallelLLMCalls {
		t.Fatalf("feature toggles mismatch: %+v", loadedToggles)
	}
	if loadedToggles.NativeEventDispatcher {
		t.Fatal("expected NativeEventDispatcher to remain false")
	}

	// 保存 features 不能覆盖 execution 节。
	reloadedProfile, err := execution.LoadProfile(dir)
	if err != nil {
		t.Fatalf("LoadProfile after feature save failed: %v", err)
	}
	if reloadedProfile.Mode != execution.ModeNative || reloadedProfile.WorkerPoolSize != 8 {
		t.Fatalf("profile was overwritten or lost: %+v", reloadedProfile)
	}
}

func TestSaveAllPreservesUnknownSectionsAndComments(t *testing.T) {
	t.Setenv(execution.EnvExecutionMode, "")
	dir := t.TempDir()
	writeRuntimeToml(t, dir, `# 顶部注释
[execution]
mode = "native"
worker_pool_size = 2

[features]
native_session_scheduler = false

[custom]
keep = "me"
`)

	profile := execution.Profile{Mode: execution.ModeCompatibility, WorkerPoolSize: 4}
	toggles := execution.DefaultFeatureToggles()
	toggles.SQLiteReadPool = true
	if err := execution.SaveAll(dir, profile, toggles); err != nil {
		t.Fatalf("SaveAll failed: %v", err)
	}

	raw, err := os.ReadFile(execution.RuntimeTomlPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "[custom]") || !strings.Contains(text, `keep = "me"`) {
		t.Fatalf("unknown section was lost:\n%s", text)
	}
	if !strings.Contains(text, "# 顶部注释") {
		t.Fatalf("top-level comment was lost:\n%s", text)
	}

	loadedProfile, err := execution.LoadProfile(dir)
	if err != nil {
		t.Fatalf("LoadProfile failed: %v", err)
	}
	if loadedProfile.Mode != execution.ModeCompatibility || loadedProfile.WorkerPoolSize != 4 {
		t.Fatalf("saved profile mismatch: %+v", loadedProfile)
	}

	loadedToggles, err := execution.LoadFeatureToggles(dir)
	if err != nil {
		t.Fatalf("LoadFeatureToggles failed: %v", err)
	}
	if !loadedToggles.SQLiteReadPool {
		t.Fatal("expected sqlite_read_pool to be saved")
	}
}

func TestSaveAllRejectsInvalidProfile(t *testing.T) {
	toggles := execution.DefaultFeatureToggles()
	cases := []execution.Profile{
		{Mode: execution.Mode("turbo")},
		{Mode: execution.ModeCompatibility, WorkerPoolSize: -1},
		{Mode: execution.ModeCompatibility, WorkerPoolSize: execution.MaxWorkerPoolSize + 1},
	}
	for _, profile := range cases {
		if err := execution.SaveAll(t.TempDir(), profile, toggles); err == nil {
			t.Fatalf("expected profile to be rejected: %+v", profile)
		}
	}
}

func TestResetAllRecoversFromMalformedConfig(t *testing.T) {
	t.Setenv(execution.EnvExecutionMode, "")
	dir := t.TempDir()
	writeRuntimeToml(t, dir, "[execution\nmode = \"native\"\n")

	if err := execution.ResetAll(dir); err != nil {
		t.Fatalf("ResetAll failed: %v", err)
	}

	profile, err := execution.LoadProfile(dir)
	if err != nil {
		t.Fatalf("LoadProfile after reset failed: %v", err)
	}
	if profile.Mode != execution.ModeCompatibility || profile.WorkerPoolSize != 0 {
		t.Fatalf("reset profile mismatch: %+v", profile)
	}

	toggles, err := execution.LoadFeatureToggles(dir)
	if err != nil {
		t.Fatalf("LoadFeatureToggles after reset failed: %v", err)
	}
	if toggles.NativeSessionScheduler || toggles.SQLiteReadPool {
		t.Fatalf("reset toggles mismatch: %+v", toggles)
	}
}

func TestConcurrentSavesKeepBothSections(t *testing.T) {
	t.Setenv(execution.EnvExecutionMode, "")
	dir := t.TempDir()

	var wg sync.WaitGroup
	for index := 0; index < 20; index++ {
		index := index
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = execution.SaveProfile(dir, execution.Profile{
				Mode:           execution.ModeNative,
				WorkerPoolSize: index,
			})
		}()
		go func() {
			defer wg.Done()
			toggles := execution.DefaultFeatureToggles()
			toggles.NativeSessionScheduler = index%2 == 0
			_ = execution.SaveFeatureToggles(dir, toggles)
		}()
	}
	wg.Wait()

	profile, err := execution.LoadProfile(dir)
	if err != nil {
		t.Fatalf("LoadProfile after concurrent saves failed: %v", err)
	}
	if profile.Mode != execution.ModeNative {
		t.Fatalf("execution section was lost: %+v", profile)
	}
	if _, err := execution.LoadFeatureToggles(dir); err != nil {
		t.Fatalf("features section was lost: %v", err)
	}
}

func TestFeatureKeyValidation(t *testing.T) {
	if !execution.IsKnownFeatureKey("native_session_scheduler") {
		t.Fatal("expected native_session_scheduler to be known")
	}
	if execution.IsKnownFeatureKey("does_not_exist") {
		t.Fatal("expected unknown feature key to be rejected")
	}
	if !execution.IsFeatureImplemented("native_session_scheduler") {
		t.Fatal("expected native_session_scheduler to be implemented")
	}
	if execution.IsFeatureImplemented("native_event_dispatcher") {
		t.Fatal("expected native_event_dispatcher to remain unimplemented")
	}

	keys := execution.UnimplementedEnabledKeys(map[string]bool{
		"native_session_scheduler": true,
		"native_event_dispatcher":  true,
		"batch_db_writes":          false,
	})
	if len(keys) != 1 || keys[0] != "native_event_dispatcher" {
		t.Fatalf("unexpected unimplemented keys: %v", keys)
	}
}

func TestApplyEnvMode(t *testing.T) {
	t.Setenv(execution.EnvExecutionMode, "NATIVE")
	profile, err := execution.ApplyEnvMode(execution.DefaultProfile())
	if err != nil {
		t.Fatalf("ApplyEnvMode failed: %v", err)
	}
	if profile.Mode != execution.ModeNative {
		t.Fatalf("expected native mode, got %s", profile.Mode)
	}

	t.Setenv(execution.EnvExecutionMode, "turbo")
	if _, err := execution.ApplyEnvMode(execution.DefaultProfile()); !errors.Is(err, execution.ErrInvalidMode) {
		t.Fatalf("expected ErrInvalidMode, got %v", err)
	}
}

func TestLoadRuntimeFileIgnoresEnvOverride(t *testing.T) {
	t.Setenv(execution.EnvExecutionMode, "native")
	dir := t.TempDir()
	writeRuntimeToml(t, dir, "[execution]\nmode = \"compatibility\"\n")

	fileProfile, _, err := execution.LoadRuntimeFile(dir)
	if err != nil {
		t.Fatalf("LoadRuntimeFile failed: %v", err)
	}
	if fileProfile.Mode != execution.ModeCompatibility {
		t.Fatalf("expected file mode compatibility, got %s", fileProfile.Mode)
	}

	effective, _, err := execution.LoadRuntime(dir)
	if err != nil {
		t.Fatalf("LoadRuntime failed: %v", err)
	}
	if effective.Mode != execution.ModeNative {
		t.Fatalf("expected env mode native, got %s", effective.Mode)
	}
}

func TestSaveAllPreservesUnknownFeatureKeys(t *testing.T) {
	t.Setenv(execution.EnvExecutionMode, "")
	dir := t.TempDir()
	writeRuntimeToml(t, dir, `[features]
native_session_scheduler = false
future_flag = true
`)

	toggles := execution.DefaultFeatureToggles()
	toggles.SQLiteReadPool = true
	if err := execution.SaveAll(dir, execution.DefaultProfile(), toggles); err != nil {
		t.Fatalf("SaveAll failed: %v", err)
	}

	raw, err := os.ReadFile(execution.RuntimeTomlPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "future_flag = true") {
		t.Fatalf("unknown feature key was lost:\n%s", string(raw))
	}

	loaded, err := execution.LoadFeatureToggles(dir)
	if err != nil {
		t.Fatalf("LoadFeatureToggles failed: %v", err)
	}
	if !loaded.SQLiteReadPool {
		t.Fatal("expected sqlite_read_pool to be saved")
	}
}

func TestUpdateRuntimeSerializesConcurrentChanges(t *testing.T) {
	t.Setenv(execution.EnvExecutionMode, "")
	dir := t.TempDir()

	var wg sync.WaitGroup
	for index := 0; index < 20; index++ {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := execution.UpdateRuntime(dir, func(_ *execution.Profile, toggles *execution.FeatureToggles) error {
				if index%2 == 0 {
					toggles.NativeSessionScheduler = true
				} else {
					toggles.SQLiteReadPool = true
				}
				return nil
			})
			if err != nil {
				t.Errorf("UpdateRuntime failed: %v", err)
			}
		}()
	}
	wg.Wait()

	toggles, err := execution.LoadFeatureToggles(dir)
	if err != nil {
		t.Fatalf("LoadFeatureToggles failed: %v", err)
	}
	if !toggles.NativeSessionScheduler || !toggles.SQLiteReadPool {
		t.Fatalf("concurrent updates were lost: %+v", toggles)
	}
}

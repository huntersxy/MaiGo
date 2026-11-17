package host

import (
	"archive/zip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testBot(t *testing.T) (Workspace, BotRecord) {
	t.Helper()
	workspace, err := OpenWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bots, err := workspace.Registry.ListBots()
	if err != nil || len(bots) != 1 {
		t.Fatalf("bots = %#v, err = %v", bots, err)
	}
	return workspace, bots[0]
}

func TestAdoptRegisterAndUndo(t *testing.T) {
	workspace, _ := testBot(t)
	source := t.TempDir()
	if err := writeDefaultInstanceConfigs(filepath.Join(source, "config")); err != nil {
		t.Fatal(err)
	}
	record, err := Adopt(workspace, source, "register", "旧实例")
	if err != nil {
		t.Fatal(err)
	}
	if record.Owned || record.Directory != source {
		t.Fatalf("登记结果 = %#v", record)
	}
	if err := UndoAdopt(workspace, record.BotID); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Registry.GetBot(record.BotID); err == nil {
		t.Fatal("撤销后不应保留登记")
	}
}

func TestArchiveAndCloneBot(t *testing.T) {
	workspace, bot := testBot(t)
	if err := os.WriteFile(filepath.Join(bot.Directory, "data", "keep.txt"), []byte("payload"), 0o666); err != nil {
		t.Fatal(err)
	}
	cloned, err := CloneBot(workspace, bot, "副本")
	if err != nil {
		t.Fatal(err)
	}
	if cloned.Name != "副本" || cloned.Initialization != "initialized" {
		t.Fatalf("克隆结果 = %#v", cloned)
	}
	if _, err := os.Stat(filepath.Join(cloned.Directory, "data", "keep.txt")); !os.IsNotExist(err) {
		t.Fatal("克隆不应复制 data")
	}
	if _, err := ArchiveBot(workspace, cloned); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace.HostDirectory(), "archives", cloned.BotID, "instance")); err != nil {
		t.Fatalf("归档目录缺失: %v", err)
	}
	if _, err := workspace.Registry.GetBot(cloned.BotID); err == nil {
		t.Fatal("归档后应删除登记")
	}
}

func TestHostAuthVerifyAndSetup(t *testing.T) {
	webui, _, _ := newTestWebUI(t)
	handler := webui.Handler()
	token := webui.credentials.Token()
	verify := httptest.NewRequest(http.MethodPost, "/api/webui/auth/verify", strings.NewReader(`{"token":"`+token+`"}`))
	verify.Header.Set("X-MaiBot-Host", "1")
	verifyRecorder := httptest.NewRecorder()
	handler.ServeHTTP(verifyRecorder, verify)
	if verifyRecorder.Code != http.StatusOK {
		t.Fatalf("验证口令失败: %d %s", verifyRecorder.Code, verifyRecorder.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(verifyRecorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["valid"] != true || payload["is_first_setup"] != true {
		t.Fatalf("验证响应 = %#v", payload)
	}
	cookies := verifyRecorder.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("验证成功应写入会话")
	}
	check := httptest.NewRequest(http.MethodGet, "/api/webui/auth/check", nil)
	for _, cookie := range cookies {
		check.AddCookie(cookie)
	}
	checkRecorder := httptest.NewRecorder()
	handler.ServeHTTP(checkRecorder, check)
	if !strings.Contains(checkRecorder.Body.String(), `"authenticated":true`) {
		t.Fatalf("认证状态 = %s", checkRecorder.Body.String())
	}
}

func TestHostSettingsRoundTrip(t *testing.T) {
	webui, _, _ := newTestWebUI(t)
	handler := webui.Handler()
	login := httptest.NewRequest(http.MethodPost, "/api/host/session", strings.NewReader(`{"ticket":"`+mustTicket(t, webui)+`"}`))
	login.Header.Set("X-MaiBot-Host", "1")
	loginRecorder := httptest.NewRecorder()
	handler.ServeHTTP(loginRecorder, login)
	cookies := loginRecorder.Result().Cookies()
	snapshotRequest := httptest.NewRequest(http.MethodGet, "/api/host/settings", nil)
	for _, cookie := range cookies {
		snapshotRequest.AddCookie(cookie)
	}
	snapshotRecorder := httptest.NewRecorder()
	handler.ServeHTTP(snapshotRecorder, snapshotRequest)
	var snapshot map[string]any
	if err := json.Unmarshal(snapshotRecorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"revision": snapshot["revision"], "webui": snapshot["webui"], "retention": snapshot["retention"],
	}
	encoded, _ := json.Marshal(body)
	update := httptest.NewRequest(http.MethodPut, "/api/host/settings", strings.NewReader(string(encoded)))
	update.Header.Set("X-MaiBot-Host", "1")
	for _, cookie := range cookies {
		update.AddCookie(cookie)
	}
	updateRecorder := httptest.NewRecorder()
	handler.ServeHTTP(updateRecorder, update)
	if updateRecorder.Code != http.StatusOK {
		t.Fatalf("保存设置失败: %d %s", updateRecorder.Code, updateRecorder.Body.String())
	}
}

func mustTicket(t *testing.T, webui *WebUI) string {
	t.Helper()
	ticket, err := webui.sessions.IssueTicket()
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func TestTransferExportImportRoundTrip(t *testing.T) {
	workspace, bot := testBot(t)
	if err := os.WriteFile(filepath.Join(bot.Directory, "data", "state.txt"), []byte("old"), 0o666); err != nil {
		t.Fatal(err)
	}
	service := NewTransferService(bot, workspace.HostDirectory(), filepath.Join(workspace.HostDirectory(), "transfers", bot.BotID), func(operation func() error) error {
		return operation()
	})
	job := service.export(map[string]any{})
	archivePath := waitTransferJob(t, service, job.JobID)
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	reader.Close()
	if err := os.WriteFile(filepath.Join(bot.Directory, "data", "state.txt"), []byte("new"), 0o666); err != nil {
		t.Fatal(err)
	}
	importJob := service.importUploaded(archivePath, map[string]struct{}{"config": {}, "data": {}})
	waitTransferStatus(t, service, importJob.JobID, "completed")
	raw, err := os.ReadFile(filepath.Join(bot.Directory, "data", "state.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "old" {
		t.Fatalf("导入后内容 = %q", string(raw))
	}
}

func waitTransferJob(t *testing.T, service *TransferService, jobID string) string {
	t.Helper()
	waitTransferStatus(t, service, jobID, "completed")
	job, err := service.getJob(jobID, "export")
	if err != nil {
		t.Fatal(err)
	}
	job.mu.Lock()
	path := job.FilePath
	job.mu.Unlock()
	if path == "" {
		t.Fatal("导出文件路径为空")
	}
	return path
}

func waitTransferStatus(t *testing.T, service *TransferService, jobID, status string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		job, err := service.getJob(jobID, "")
		if err != nil {
			t.Fatal(err)
		}
		job.mu.Lock()
		current := job.Status
		job.mu.Unlock()
		if current == status {
			return
		}
		if current == "failed" {
			job.mu.Lock()
			message := job.Error
			job.mu.Unlock()
			t.Fatalf("任务失败: %s", message)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("任务未在期限内完成: %s", jobID)
}

func TestMaintenanceDependencyInstallDoesNotDeadlock(t *testing.T) {
	t.Setenv("MAIBOT_PLUGIN_PYTHON", "missing-python")
	workspace, _ := testBot(t)
	maintenance := NewHostMaintenance(NewSupervisor(workspace))
	done := make(chan error, 1)
	go func() {
		_, err := maintenance.InstallPluginDependencies(context.Background())
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("插件依赖检查持有维护锁时死锁")
	}
}

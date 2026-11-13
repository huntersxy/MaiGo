package htmlrender

// 托管浏览器状态记录，对齐 Python src/services/html_render_service.py
// 的 ManagedBrowserRecord / _update_managed_browser_record：记录 Playwright 托管
// Chromium 的来源、目录与最近校验时间，写入 data/plugin_runtime/html_render_browser_state.json。

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"
)

const (
	managedBrowserStateName = "html_render_browser_state.json"
	managedBrowserAuto      = "auto_download"
	managedBrowserCache     = "existing_cache"
	managedBrowserName      = "chromium"
)

// managedBrowserRecord 托管浏览器使用记录，字段与 Python ManagedBrowserRecord 一致
type managedBrowserRecord struct {
	BrowserName       string `json:"browser_name"`
	BrowsersPath      string `json:"browsers_path"`
	InstallSource     string `json:"install_source"`
	PlaywrightVersion string `json:"playwright_version"`
	RecordedAt        string `json:"recorded_at"`
	LastVerifiedAt    string `json:"last_verified_at"`
}

// valid 判断记录是否完整且来源合法，对齐 ManagedBrowserRecord.from_dict
func (r managedBrowserRecord) valid() bool {
	if strings.TrimSpace(r.BrowserName) == "" || strings.TrimSpace(r.BrowsersPath) == "" {
		return false
	}
	if strings.TrimSpace(r.PlaywrightVersion) == "" || strings.TrimSpace(r.RecordedAt) == "" {
		return false
	}
	if strings.TrimSpace(r.LastVerifiedAt) == "" {
		return false
	}
	return r.InstallSource == managedBrowserAuto || r.InstallSource == managedBrowserCache
}

// managedBrowserStatePath 对齐 _get_browser_state_path
func managedBrowserStatePath(instanceDir string) string {
	base := strings.TrimSpace(instanceDir)
	if base == "" {
		base, _ = os.Getwd()
	}
	return filepath.Join(base, "data", "plugin_runtime", managedBrowserStateName)
}

// hasManagedBrowserArtifact 判断目录内是否存在 Playwright 托管浏览器
func hasManagedBrowserArtifact(browsersPath string) bool {
	entries, err := os.ReadDir(browsersPath)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "chromium-") || strings.HasPrefix(name, "chrome-") || strings.HasPrefix(name, "chrome-headless-shell-") {
			return true
		}
	}
	return false
}

// loadManagedBrowserRecord 读取上次成功使用的记录，非法内容忽略
func loadManagedBrowserRecord(statePath string) *managedBrowserRecord {
	raw, err := os.ReadFile(statePath)
	if err != nil {
		return nil
	}
	var record managedBrowserRecord
	if err := sonic.Unmarshal(raw, &record); err != nil {
		zap.L().Warn("HTML 渲染浏览器状态文件读取失败，将忽略并继续", zap.String("path", statePath), zap.Error(err))
		return nil
	}
	if !record.valid() {
		zap.L().Warn("HTML 渲染浏览器状态文件格式无效，将忽略并继续", zap.String("path", statePath))
		return nil
	}
	return &record
}

// saveManagedBrowserRecord 写入托管浏览器记录
func saveManagedBrowserRecord(statePath string, record managedBrowserRecord) error {
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return err
	}
	raw, err := sonic.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(statePath, raw, 0o644)
}

// updateManagedBrowserRecord 更新托管浏览器记录，对齐 _update_managed_browser_record
//
// 只在该目录确有托管浏览器时记录；同目录沿用既有 recorded_at，只刷新 last_verified_at。
func updateManagedBrowserRecord(instanceDir, browsersPath, installSource, playwrightVersion string) {
	browsersPath = strings.TrimSpace(browsersPath)
	if !hasManagedBrowserArtifact(browsersPath) {
		return
	}
	statePath := managedBrowserStatePath(instanceDir)
	now := time.Now().UTC().Format(time.RFC3339)
	recordedAt := now
	if existing := loadManagedBrowserRecord(statePath); existing != nil && existing.BrowsersPath == browsersPath {
		recordedAt = existing.RecordedAt
	}
	record := managedBrowserRecord{
		BrowserName:       managedBrowserName,
		BrowsersPath:      browsersPath,
		InstallSource:     installSource,
		PlaywrightVersion: strings.TrimSpace(playwrightVersion),
		RecordedAt:        recordedAt,
		LastVerifiedAt:    now,
	}
	if record.PlaywrightVersion == "" {
		record.PlaywrightVersion = "unknown"
	}
	if err := saveManagedBrowserRecord(statePath, record); err != nil {
		zap.L().Warn("HTML 渲染服务写入浏览器状态记录失败", zap.String("path", statePath), zap.Error(err))
		return
	}
	zap.L().Info("HTML 渲染服务已更新托管浏览器记录",
		zap.String("source", installSource),
		zap.String("browsers_path", browsersPath),
		zap.String("last_verified_at", now))
}

package chat

import (
	"encoding/json"
	imagesystem "maibot-go/internal/chat/image_system"
	"net/http"
	"path/filepath"
	"strings"

	"maibot-go/internal/webui/httputil"
	webuiutils "maibot-go/internal/webui/utils"
)

const (
	botConfigFileName   = "bot_config.toml"
	modelConfigFileName = "model_config.toml"
)

// botConfigPath 返回 bot_config.toml 的绝对路径
//
// 与父包 routers 的同名方法语义一致：实例目录未配置时返回 ok=false，
// 由调用方写出统一的 503 提示。
func (d Deps) botConfigPath() (string, bool) {
	dir := strings.TrimSpace(d.InstanceDir)
	if dir == "" {
		return "", false
	}
	return filepath.Join(filepath.Clean(dir), "config", botConfigFileName), true
}

// readTOMLDocument 读取 TOML 文档
func (d Deps) readTOMLDocument(path string) (map[string]any, error) {
	return webuiutils.ReadTOMLDocument(path)
}

// writeConfigReadError 配置读取失败时的统一响应
func (d Deps) writeConfigReadError(w http.ResponseWriter, err error) {
	httputil.WriteJSON(w, http.StatusInternalServerError, map[string]any{
		"detail":      "读取配置文件失败：" + err.Error(),
		"implemented": true,
		"success":     false,
	})
}

// writeInstanceDirMissing 实例目录未配置时的统一响应
func (d Deps) writeInstanceDirMissing(w http.ResponseWriter, path string) {
	httputil.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{
		"detail":      "需要设置 MAIBOT_INSTANCE_DIR 指向 MaiBot 实例根目录",
		"maibot_path": path,
		"implemented": true,
		"success":     false,
	})
}

// stringValue 把任意值转为字符串，供配置渲染使用
func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return ""
	}
}

// handleChatNotFound 404 响应，对齐 FastAPI 默认 detail="Not Found"
func (d Deps) handleChatNotFound(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusNotFound, map[string]any{"detail": "Not Found"})
}

// intValue 把任意数值转为 int，无法识别时返回 fallback
func intValue(value any, fallback int) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
		return fallback
	default:
		return fallback
	}
}

// resolveMonitorMediaStore 返回监控媒体只读存储
//
// 聊天历史里的图片元数据来自 images 表；未接 Memory 时返回 nil，
// 调用方按「无媒体」处理而不是报错。
func (d Deps) resolveMonitorMediaStore() *imagesystem.GORMStore {
	if d.Memory == nil || d.Memory.GetDB() == nil {
		return nil
	}
	return imagesystem.NewGORMStore(d.Memory.GetDB())
}

package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SessionCWD 对齐 acp.session_cwd：远端路径不经本机 resolve
func SessionCWD(cfg Config, localCWD string) (string, error) {
	remote := strings.TrimSpace(cfg.ACPRemoteCwd)
	if remote == "" {
		return localCWD, nil
	}
	if cfg.Connection == ConnectionDSH {
		return "", fmt.Errorf("ACP 远程会话工作目录仅适用于自定义 ACP 或 Desktop 隧道连接")
	}
	if !isAbsolutePath(remote) {
		return "", fmt.Errorf("ACP 远程会话工作目录必须是远端已存在的绝对路径，不能使用 ~ 或相对路径")
	}
	return remote, nil
}

func isAbsolutePath(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	if len(p) >= 2 && p[1] == ':' {
		return true
	}
	return filepath.IsAbs(p)
}

// DesktopManifestPath 对齐 acp.desktop_manifest（含 ~ 展开）
func DesktopManifestPath(cfg Config) string {
	if p := strings.TrimSpace(cfg.DesktopManifest); p != "" {
		if expanded, err := expandUserPath(p); err == nil && expanded != "" {
			return expanded
		}
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".dsh", "maibot-desktop.json")
}

// DesktopBridgeInfo dsh Desktop 桥接 manifest v1
type DesktopBridgeInfo struct {
	Version int    `json:"version"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Token   string `json:"token"`
}

// LoadDesktopBridgeInfo 读取 Desktop 桥接文件
func LoadDesktopBridgeInfo(manifestPath string) (DesktopBridgeInfo, error) {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return DesktopBridgeInfo{}, fmt.Errorf("未找到 dsh Desktop 桥接信息；请安装 dsh-maibot-bridge 插件并启动 Desktop")
	}
	var info DesktopBridgeInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return DesktopBridgeInfo{}, fmt.Errorf("无效的 Desktop 桥接文件: %w", err)
	}
	if info.Version != 1 || info.Host != "127.0.0.1" {
		return DesktopBridgeInfo{}, fmt.Errorf("Desktop 桥接只接受本机的 v1 连接信息")
	}
	if info.Port < 1 || info.Port > 65535 || strings.TrimSpace(info.Token) == "" {
		return DesktopBridgeInfo{}, fmt.Errorf("无效的 Desktop 桥接端口或认证信息")
	}
	return info, nil
}

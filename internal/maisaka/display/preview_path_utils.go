package display

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var safeNamePattern = regexp.MustCompile("[^A-Za-z0-9._-]+")

// PreviewSessionInfo 预览目录所需的会话信息
type PreviewSessionInfo struct {
	Platform string
	IsGroup  bool
	GroupID  string
	UserID   string
}

// PreviewSessionLookup 按会话编号查询预览会话信息
type PreviewSessionLookup func(sessionID string) (PreviewSessionInfo, bool)

// RepoRoot 提供实例根目录，对齐 Python get_instance_root：优先 MAIBOT_INSTANCE_DIR，
// 未设置时回退进程工作目录
var RepoRoot = func() string {
	if value := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR")); value != "" {
		if abs, err := filepath.Abs(value); err == nil {
			return abs
		}
		return value
	}
	root, err := filepath.Abs(".")
	if err != nil {
		return "."
	}
	return root
}

// NormalizePreviewName 归一化预览名称
func NormalizePreviewName(value string) string {
	normalized := strings.Trim(safeNamePattern.ReplaceAllString(strings.TrimSpace(value), "_"), "._")
	if normalized != "" {
		return normalized
	}
	return "unknown"
}

// NormalizePlatformName 归一化平台名称
func NormalizePlatformName(platform string) string {
	normalized := strings.ToLower(strings.TrimSpace(platform))
	if normalized == "telegram" {
		normalized = "tg"
	}
	return NormalizePreviewName(normalized)
}

// BuildPreviewChatDirName 构造预览聊天目录名
func BuildPreviewChatDirName(sessionID string, lookup PreviewSessionLookup) string {
	if lookup != nil {
		if session, ok := lookup(sessionID); ok {
			platform := NormalizePlatformName(session.Platform)
			if session.IsGroup && session.GroupID != "" {
				return platform + "_group_" + NormalizePreviewName(session.GroupID)
			}
			if session.UserID != "" {
				return platform + "_private_" + NormalizePreviewName(session.UserID)
			}
		}
	}
	normalized := NormalizePreviewName(sessionID)
	if normalized != "unknown" {
		return normalized
	}
	return "unknown_chat"
}

// BuildDisplayPath 构造用于展示的路径，项目内文件优先显示相对路径
func BuildDisplayPath(filePath string) string {
	absolute, err := filepath.Abs(filePath)
	if err != nil {
		absolute = filePath
	}
	root, err := filepath.Abs(RepoRoot())
	if err == nil {
		if relative, relErr := filepath.Rel(root, absolute); relErr == nil && !strings.HasPrefix(relative, "..") {
			return filepath.ToSlash(relative)
		}
	}
	return filepath.ToSlash(absolute)
}

// BuildFileURI 构造 file:// 地址
func BuildFileURI(filePath string) string {
	absolute, err := filepath.Abs(filePath)
	if err != nil {
		absolute = filePath
	}
	normalized := filepath.ToSlash(absolute)
	return "file:///" + strings.ReplaceAll(url.PathEscape(normalized), "%2F", "/")
}

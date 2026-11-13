package memorix

// A_memorix 路径常量与解析，对齐 Python src/A_memorix/paths.py
// 差异说明：Python 的 package_root 取 paths.py 所在插件目录；Go 版插件资源不再以源码目录分发，
// 默认取可执行文件所在目录，可用 A_MEMORIX_PACKAGE_ROOT 显式覆盖（发行包放置 config_schema.json 等资源）

import (
	"os"
	"path/filepath"
	"strings"
)

// SystemID 对齐 A_MEMORIX_SYSTEM_ID
const SystemID = "a_memorix"

// InstanceDirEnv 对齐 MaiBot src/common/paths.py INSTANCE_DIRECTORY_ENV
const InstanceDirEnv = "MAIBOT_INSTANCE_DIR"

// PackageRootEnv 覆盖 Go 版插件资源根目录
const PackageRootEnv = "A_MEMORIX_PACKAGE_ROOT"

// PackageRoot 返回插件资源根目录，对齐 package_root()
func PackageRoot() string {
	if raw := strings.TrimSpace(os.Getenv(PackageRootEnv)); raw != "" {
		abs, err := filepath.Abs(expandUser(raw))
		if err == nil {
			return abs
		}
		return filepath.Clean(expandUser(raw))
	}
	if executable, err := os.Executable(); err == nil {
		return filepath.Dir(executable)
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

// SrcRoot 返回插件目录的上一级，对齐 src_root()
func SrcRoot() string {
	return filepath.Dir(PackageRoot())
}

// RepoRoot 返回实例根目录，对齐 get_instance_root()
// 优先 MAIBOT_INSTANCE_DIR，否则取进程工作目录
func RepoRoot() string {
	if raw := strings.TrimSpace(os.Getenv(InstanceDirEnv)); raw != "" {
		abs, err := filepath.Abs(expandUser(raw))
		if err == nil {
			return abs
		}
		return filepath.Clean(expandUser(raw))
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

// ConfigPath 返回 config/a_memorix.toml，对齐 config_path()
func ConfigPath() string {
	return filepath.Join(RepoRoot(), "config", SystemID+".toml")
}

// DefaultDataDir 返回 data/a-memorix，对齐 default_data_dir()
func DefaultDataDir() string {
	return filepath.Join(RepoRoot(), "data", "a-memorix")
}

// ArtifactsRoot 返回 data/a-memorix/artifacts，对齐 artifacts_root()
func ArtifactsRoot() string {
	return filepath.Join(DefaultDataDir(), "artifacts")
}

// SchemaPath 返回 config_schema.json，对齐 schema_path()
func SchemaPath() string {
	return filepath.Join(PackageRoot(), "config_schema.json")
}

// WebRoot 返回插件 web 资源目录，对齐 web_root()
func WebRoot() string {
	return filepath.Join(PackageRoot(), "web")
}

// ScriptsRoot 返回插件 scripts 目录，对齐 scripts_root()
func ScriptsRoot() string {
	return filepath.Join(PackageRoot(), "scripts")
}

// ResolveRepoPath 解析仓库相对路径，对齐 resolve_repo_path()
// rawPath 为空时返回 fallback（同样为空时回退 default_data_dir）的绝对路径
func ResolveRepoPath(rawPath string, fallback string) string {
	raw := strings.TrimSpace(rawPath)
	if raw == "" {
		if strings.TrimSpace(fallback) == "" {
			fallback = DefaultDataDir()
		}
		return resolveAbsolute(fallback)
	}
	candidate := expandUser(raw)
	if filepath.IsAbs(candidate) {
		return resolveAbsolute(candidate)
	}
	return resolveAbsolute(filepath.Join(RepoRoot(), candidate))
}

func resolveAbsolute(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return abs
}

// expandUser 对齐 Path.expanduser() 的常见用法：仅展开开头的 ~ 与 ~/
func expandUser(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// PackageRootFor 允许测试注入插件资源根目录而不改环境变量
func PackageRootFor(root string) string {
	if strings.TrimSpace(root) == "" {
		return PackageRoot()
	}
	return resolveAbsolute(expandUser(root))
}

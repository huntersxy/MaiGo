package pluginmarket

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bytedance/sonic"
)

// 保留插件目录名，对齐 manifest_validator._RESERVED_PLUGIN_DIRECTORY_NAMES
var reservedPluginDirectoryNames = map[string]struct{}{"data": {}, "__pycache__": {}}

// resolveInstanceRoot 对齐 common.paths.get_instance_root：显式参数优先，其次 MAIBOT_INSTANCE_DIR
func resolveInstanceRoot(instanceDir string) (string, error) {
	root := strings.TrimSpace(instanceDir)
	if root == "" {
		root = strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR"))
	}
	if root == "" {
		workingDir, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("pluginmarket: 无法解析工作目录: %w", err)
		}
		root = workingDir
	}
	expanded, err := expandHome(root)
	if err != nil {
		return "", err
	}
	return filepath.Abs(expanded)
}

func expandHome(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

// ResolvePluginsDir 对齐 MaiBot get_plugins_dir：实例目录下的 plugins，缺失时创建
func ResolvePluginsDir(instanceDir string) (string, error) {
	root, err := resolveInstanceRoot(instanceDir)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("pluginmarket: 创建 plugins 目录失败: %w", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return abs, nil
}

func badRequestError(format string, args ...any) error {
	return &MarketError{StatusCode: http.StatusBadRequest, Message: fmt.Sprintf(format, args...)}
}

func notFoundError(format string, args ...any) error {
	return &MarketError{StatusCode: http.StatusNotFound, Message: fmt.Sprintf(format, args...)}
}

func conflictError(format string, args ...any) error {
	return &MarketError{StatusCode: http.StatusConflict, Message: fmt.Sprintf(format, args...)}
}

// ValidatePluginID 对齐 support.validate_plugin_id
func ValidatePluginID(pluginID string) error {
	if strings.TrimSpace(pluginID) == "" {
		return badRequestError("插件 ID 不能为空")
	}
	for _, pattern := range []string{"/", "\\", "\x00", "..", "\n", "\r", "\t"} {
		if strings.Contains(pluginID, pattern) {
			return badRequestError("插件 ID 包含非法字符")
		}
	}
	if strings.HasPrefix(pluginID, ".") || strings.HasSuffix(pluginID, ".") {
		return badRequestError("插件 ID 不能以点开头或结尾")
	}
	if pluginID == "." || pluginID == ".." {
		return badRequestError("插件 ID 不能为特殊目录名")
	}
	return nil
}

// validateSafePath 对齐 support.validate_safe_path：拒绝绝对路径与越界路径
func validateSafePath(userPath, basePath string) (string, error) {
	if strings.Contains(userPath, "..") || strings.Contains(userPath, "\x00") {
		return "", badRequestError("路径包含非法字符")
	}
	if strings.HasPrefix(userPath, "/") || strings.HasPrefix(userPath, "\\") || (len(userPath) > 1 && userPath[1] == ':') {
		return "", badRequestError("不允许使用绝对路径")
	}
	baseResolved, err := filepath.Abs(basePath)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(baseResolved, userPath))
	if err != nil {
		return "", err
	}
	if !isPathWithin(baseResolved, target) {
		return "", badRequestError("路径超出允许范围")
	}
	return target, nil
}

func isPathWithin(base, target string) bool {
	relative, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	if relative == "." {
		return true
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// resolvePluginFilePath 对齐 support.resolve_plugin_file_path
func resolvePluginFilePath(pluginRoot, relativePath string, allowMissing bool) (string, error) {
	rootResolved, err := filepath.Abs(pluginRoot)
	if err != nil {
		return "", err
	}
	target := filepath.Join(rootResolved, relativePath)
	if info, statErr := os.Lstat(target); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", badRequestError("插件文件不能是符号链接: %s", relativePath)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		resolved = filepath.Clean(target)
	}
	if !isPathWithin(rootResolved, resolved) {
		return "", badRequestError("插件文件超出允许范围: %s", relativePath)
	}
	if !allowMissing {
		if _, statErr := os.Stat(resolved); statErr != nil {
			return "", notFoundError("插件文件不存在: %s", relativePath)
		}
	}
	return resolved, nil
}

// parseRepositoryURL 对齐 support.parse_repository_url
func parseRepositoryURL(repositoryURL string) (normalized, owner, repo string, err error) {
	trimmed := strings.TrimSuffix(strings.TrimRight(repositoryURL, "/"), ".git")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 2 {
		return "", "", "", badRequestError("无效的仓库 URL")
	}
	return trimmed, parts[len(parts)-2], parts[len(parts)-1], nil
}

// LoadManifestJSON 对齐 support.load_manifest_json：拒绝符号链接与越界 manifest
func LoadManifestJSON(manifestPath string) (map[string]any, error) {
	info, err := os.Lstat(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, nil
	}
	resolved, err := filepath.EvalSymlinks(manifestPath)
	if err != nil {
		return nil, nil
	}
	parentResolved, err := filepath.EvalSymlinks(filepath.Dir(manifestPath))
	if err != nil {
		return nil, nil
	}
	if !isPathWithin(parentResolved, resolved) {
		return nil, nil
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return nil, nil
	}
	var manifest map[string]any
	if err := sonic.Unmarshal(raw, &manifest); err != nil {
		return nil, nil
	}
	return manifest, nil
}

// isReservedPluginDirectory 对齐 manifest_validator.is_reserved_plugin_directory
func isReservedPluginDirectory(name string) bool {
	folded := strings.ToLower(name)
	if _, ok := reservedPluginDirectoryNames[folded]; ok {
		return true
	}
	return strings.HasPrefix(folded, ".")
}

// iterPluginDirectories 对齐 support.iter_plugin_directories：跳过保留目录与越界目录
func iterPluginDirectories(pluginsRoot string) ([]string, error) {
	entries, err := os.ReadDir(pluginsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	rootResolved, err := filepath.Abs(pluginsRoot)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if isReservedPluginDirectory(entry.Name()) {
			continue
		}
		path := filepath.Join(rootResolved, entry.Name())
		info, statErr := os.Lstat(path)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			continue
		}
		out = append(out, path)
	}
	return out, nil
}

// CandidatePaths 对齐 support.get_plugin_candidate_paths：新目录 id 与旧目录 id.replace('.', '_')
func CandidatePaths(pluginsRoot, pluginID string) (target string, oldFormat string, err error) {
	target, err = validateSafePath(strings.ReplaceAll(pluginID, ".", "_"), pluginsRoot)
	if err != nil {
		return "", "", err
	}
	oldFormat, err = validateSafePath(pluginID, pluginsRoot)
	if err != nil {
		return "", "", err
	}
	return target, oldFormat, nil
}

// FindInstalledPlugin 对齐 support.find_plugin_path_by_id：先精确匹配，再忽略大小写
func FindInstalledPlugin(pluginsRoot, pluginID string) (string, error) {
	normalized := strings.ToLower(pluginID)
	casefoldMatch := ""
	directories, err := iterPluginDirectories(pluginsRoot)
	if err != nil {
		return "", err
	}
	for _, directory := range directories {
		manifestPath, pathErr := resolvePluginFilePath(directory, "_manifest.json", true)
		if pathErr != nil {
			continue
		}
		manifest, loadErr := LoadManifestJSON(manifestPath)
		if loadErr != nil || manifest == nil {
			continue
		}
		manifestID := strings.TrimSpace(safeString(manifest["id"]))
		if manifestID == pluginID {
			return directory, nil
		}
		if casefoldMatch == "" && strings.ToLower(manifestID) == normalized {
			casefoldMatch = directory
		}
	}
	if casefoldMatch != "" {
		return casefoldMatch, nil
	}
	return "", nil
}

// IsPluginInstallResidue 对齐 support.is_plugin_install_residue
func IsPluginInstallResidue(pluginPath string) bool {
	info, err := os.Lstat(pluginPath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	manifestPath, err := resolvePluginFilePath(pluginPath, "_manifest.json", true)
	if err == nil {
		if _, statErr := os.Stat(manifestPath); statErr == nil {
			return false
		}
	}
	entries, err := os.ReadDir(pluginPath)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || entry.Name() != "config.toml" {
			return false
		}
	}
	return true
}

// RemoveTree 对齐 support.remove_tree：拒绝删除符号链接路径，并在只读文件导致失败时补写权限重试
func RemoveTree(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("拒绝删除符号链接路径: %s", path)
	}
	firstErr := os.RemoveAll(path)
	if firstErr == nil {
		return nil
	}
	// 对齐 shutil.rmtree(onerror=...)：删除失败时清除只读位后重试
	if err := makeTreeWritable(path); err != nil {
		return firstErr
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	return nil
}

// makeTreeWritable 为受只读位保护的目录和普通文件补上写权限，跳过符号链接
func makeTreeWritable(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if info.IsDir() {
		if err := ensureWritable(root, info); err != nil {
			return err
		}
		return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			entryInfo, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			return ensureWritable(path, entryInfo)
		})
	}
	return ensureWritable(root, info)
}

// ensureWritable 只补写位，保留其余权限位
func ensureWritable(path string, info os.FileInfo) error {
	if info.Mode().Perm()&0o200 != 0 {
		return nil
	}
	return os.Chmod(path, info.Mode().Perm()|0o200)
}

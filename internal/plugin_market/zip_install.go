package pluginmarket

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	// MaxZipBytes 对齐 zip_install.MAX_ZIP_BYTES
	MaxZipBytes = 100 * 1024 * 1024
	// MaxExtractedBytes 对齐 zip_install.MAX_EXTRACTED_BYTES
	MaxExtractedBytes = 300 * 1024 * 1024
	// MaxZipEntries 对齐 zip_install.MAX_ZIP_ENTRIES
	MaxZipEntries = 10000
)

// windowsReservedNames 对齐 _WINDOWS_RESERVED_NAMES
var windowsReservedNames = func() map[string]struct{} {
	names := map[string]struct{}{"con": {}, "prn": {}, "aux": {}, "nul": {}}
	for _, prefix := range []string{"com", "lpt"} {
		for index := 1; index <= 9; index++ {
			names[fmt.Sprintf("%s%d", prefix, index)] = struct{}{}
		}
	}
	return names
}()

// zipEntry ZIP 目录表中的单个条目
type zipEntry struct {
	file *zip.File
	path string
}

// InstallZipArchive 对齐 zip_install.install_zip_archive：隔离解压并校验后发布到 plugins
func (s *Service) InstallZipArchive(readerAt io.ReaderAt, size int64) (map[string]any, error) {
	if size > MaxZipBytes {
		return nil, &MarketError{StatusCode: 413, Message: "ZIP 文件不能超过 100 MB"}
	}
	pluginsRoot, err := ResolvePluginsDir(s.instanceDirectory())
	if err != nil {
		return nil, err
	}
	stagingRoot, err := os.MkdirTemp(pluginsRoot, ".zip-install-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = RemoveTree(stagingRoot) }()

	root, err := extractZipArchive(readerAt, size, stagingRoot)
	if err != nil {
		return nil, err
	}
	candidate := filepath.Join(stagingRoot, filepath.FromSlash(root))
	if root == "." {
		candidate = stagingRoot
	}
	manifest, manifestErrors, _ := LoadManifestFromPluginPath(candidate, DefaultMaibotVersion(), true)
	if manifest == nil {
		return nil, badRequestError("插件校验失败：%s", strings.Join(manifestErrors, "；"))
	}
	release, err := s.Reserve(manifest.ID, "install")
	if err != nil {
		return nil, err
	}
	defer release()
	target, oldTarget, err := CandidatePaths(pluginsRoot, manifest.ID)
	if err != nil {
		return nil, err
	}
	if pathExists(target) || pathExists(oldTarget) {
		return nil, conflictError("插件 %s 已安装，请先卸载后再从 ZIP 安装", manifest.ID)
	}
	if installed, err := FindInstalledPlugin(pluginsRoot, manifest.ID); err != nil {
		return nil, err
	} else if installed != "" {
		return nil, conflictError("插件 %s 已安装，请先卸载后再从 ZIP 安装", manifest.ID)
	}
	if err := os.Rename(candidate, target); err != nil {
		return nil, err
	}
	return map[string]any{
		"success":   true,
		"plugin_id": manifest.ID,
		"message":   manifest.Name + " 安装成功，请重启麦麦使其生效",
	}, nil
}

// extractZipArchive 对齐 _archive_entries 与解压流程，返回条目与插件根目录
func extractZipArchive(readerAt io.ReaderAt, size int64, staging string) (string, error) {
	archive, err := zip.NewReader(readerAt, size)
	if err != nil {
		return "", badRequestError("ZIP 解压失败：%s", err.Error())
	}
	fileCount := 0
	var extracted uint64
	for _, file := range archive.File {
		fileCount++
		extracted += file.UncompressedSize64
	}
	if fileCount > MaxZipEntries || extracted > MaxExtractedBytes {
		return "", badRequestError("ZIP 解压后超过 300 MB 或文件数量超过 10000")
	}

	entries := make([]zipEntry, 0, len(archive.File))
	seen := map[string]struct{}{}
	for _, file := range archive.File {
		name := file.Name
		if err := validateZipEntryName(name, file); err != nil {
			return "", err
		}
		key := strings.ToLower(path.Clean(name))
		if _, ok := seen[key]; ok {
			return "", badRequestError("ZIP 包含重复路径：%s", name)
		}
		seen[key] = struct{}{}
		entries = append(entries, zipEntry{file: file, path: strings.TrimSuffix(name, "/")})
	}

	root, err := resolveZipPluginRoot(entries)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		destination := filepath.Join(staging, filepath.FromSlash(entry.path))
		if entry.file.FileInfo().IsDir() {
			if err := os.MkdirAll(destination, 0o755); err != nil {
				return "", badRequestError("ZIP 解压失败：%s", err.Error())
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return "", badRequestError("ZIP 解压失败：%s", err.Error())
		}
		if err := writeZipEntry(entry.file, destination); err != nil {
			return "", badRequestError("ZIP 解压失败：%s", err.Error())
		}
	}
	return root, nil
}

func writeZipEntry(file *zip.File, destination string) error {
	input, err := file.Open()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

// validateZipEntryName 在写入任何文件前拒绝路径别名、链接与加密条目
func validateZipEntryName(name string, file *zip.File) error {
	invalid := func() error {
		return badRequestError("ZIP 包含非法路径、链接或加密文件：%s", name)
	}
	if name == "" || strings.Contains(name, "\\") {
		return invalid()
	}
	isDir := file.FileInfo().IsDir()
	trimmed := strings.TrimSuffix(name, "/")
	if strings.HasPrefix(trimmed, "/") || strings.Contains(trimmed, "\x00") {
		return invalid()
	}
	if file.Flags&0x1 != 0 {
		return invalid()
	}
	mode := file.Mode()
	if mode&os.ModeSymlink != 0 {
		return invalid()
	}
	if mode&os.ModeType != 0 && !isDir && !mode.IsRegular() {
		return invalid()
	}
	for _, part := range strings.Split(trimmed, "/") {
		if err := validateZipPathPart(part); err != nil {
			return invalid()
		}
	}
	return nil
}

func validateZipPathPart(part string) error {
	if part == "" || part == "." || part == ".." {
		return fmt.Errorf("非法路径段")
	}
	if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
		return fmt.Errorf("非法路径段")
	}
	for _, char := range part {
		if char < 32 || strings.ContainsRune(`<>:"|?*`, char) {
			return fmt.Errorf("非法路径段")
		}
	}
	stem := strings.SplitN(strings.ToLower(part), ".", 2)[0]
	if _, ok := windowsReservedNames[stem]; ok {
		return fmt.Errorf("Windows 保留名")
	}
	lowered := strings.ToLower(part)
	if lowered == ".git" || lowered == ".maibot-release.json" {
		return fmt.Errorf("保留文件")
	}
	return nil
}

// resolveZipPluginRoot 对齐 install_zip_archive 的根目录判定
func resolveZipPluginRoot(entries []zipEntry) (string, error) {
	manifestPaths := make([]string, 0, 1)
	for _, entry := range entries {
		if entry.file.FileInfo().IsDir() {
			continue
		}
		if path.Base(entry.path) == "_manifest.json" {
			manifestPaths = append(manifestPaths, entry.path)
		}
	}
	for _, manifestPath := range manifestPaths {
		if manifestPath == "_manifest.json" {
			return ".", nil
		}
	}
	candidates := make([]string, 0, 1)
	for _, manifestPath := range manifestPaths {
		parts := strings.Split(manifestPath, "/")
		if len(parts) != 2 {
			continue
		}
		candidates = append(candidates, parts[0])
	}
	if len(candidates) != 1 {
		return "", badRequestError("ZIP 根目录或唯一一层插件目录中必须包含 _manifest.json")
	}
	root := candidates[0]
	for _, entry := range entries {
		if entry.path != root && !strings.HasPrefix(entry.path, root+"/") {
			return "", badRequestError("ZIP 必须只包含一个插件目录")
		}
	}
	return root, nil
}

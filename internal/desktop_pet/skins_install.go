package desktoppet

// 皮肤包安装，对齐 src/desktop_pet/skins.py 的 install_zip；只解压清单与静态图片

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// installSkinZip 解压并校验皮肤包，成功后移动到 data/desktop_pet/skins/<id>
func installSkinZip(root string, payload []byte) (SkinDocument, error) {
	if len(payload) > skinMaxBytes {
		return SkinDocument{}, fmt.Errorf("压缩包不能超过 32 MiB")
	}
	destination := filepath.Join(root, "data", "desktop_pet", "skins")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return SkinDocument{}, fmt.Errorf("创建皮肤目录失败: %w", err)
	}
	temporary, err := os.MkdirTemp(filepath.Dir(destination), "skin-")
	if err != nil {
		return SkinDocument{}, fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	reader, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		return SkinDocument{}, fmt.Errorf("皮肤包不是有效的 zip 文件")
	}
	var total uint64
	for _, entry := range reader.File {
		total += entry.UncompressedSize64
	}
	if len(reader.File) > 512 || total > skinMaxBytes {
		return SkinDocument{}, fmt.Errorf("皮肤包解压后超过限制")
	}
	remaining := int64(skinMaxBytes)
	for _, entry := range reader.File {
		written, err := extractSkinEntry(entry, temporary, remaining)
		if err != nil {
			return SkinDocument{}, err
		}
		remaining -= written
	}
	manifests, err := findSkinManifests(temporary)
	if err != nil {
		return SkinDocument{}, err
	}
	skin, err := loadSkin(manifests[0], false)
	if err != nil {
		return SkinDocument{}, err
	}
	for _, existing := range ListSkins(root).Skins {
		if existing.ID == skin.ID {
			return SkinDocument{}, fmt.Errorf("皮肤 ID 已存在，请使用不同 ID")
		}
	}
	target := filepath.Join(destination, skin.ID)
	if _, statErr := os.Stat(target); statErr == nil {
		return SkinDocument{}, fmt.Errorf("目标皮肤目录已存在")
	}
	if err := os.Rename(filepath.Dir(manifests[0]), target); err != nil {
		return SkinDocument{}, fmt.Errorf("安装皮肤失败: %w", err)
	}
	return skin, nil
}

// extractSkinEntry 校验单个 zip 条目并写入临时目录，拒绝绝对路径、穿越、符号链接与非图片
// 返回实际写入字节数，调用方按累计值防止解压后超过总上限
func extractSkinEntry(entry *zip.File, folder string, remaining int64) (int64, error) {
	name := entry.Name
	if strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, ":") {
		return 0, fmt.Errorf("压缩包包含不安全路径")
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return 0, fmt.Errorf("压缩包包含不安全路径")
		}
	}
	if entry.Mode()&os.ModeSymlink != 0 {
		return 0, fmt.Errorf("皮肤包不能包含符号链接")
	}
	if entry.FileInfo().IsDir() {
		return 0, nil
	}
	extension := strings.ToLower(path.Ext(name))
	if extension != ".json" && extension != ".png" && extension != ".webp" {
		return 0, fmt.Errorf("皮肤包只能包含清单和 PNG/WebP 图片")
	}
	target := filepath.Join(folder, filepath.FromSlash(path.Clean(name)))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, fmt.Errorf("创建目录失败: %w", err)
	}
	source, err := entry.Open()
	if err != nil {
		return 0, fmt.Errorf("读取压缩包条目失败: %w", err)
	}
	defer func() { _ = source.Close() }()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, fmt.Errorf("写入皮肤文件失败: %w", err)
	}
	defer func() { _ = output.Close() }()
	if remaining < 0 {
		return 0, fmt.Errorf("皮肤包解压后超过限制")
	}
	written, err := io.Copy(output, io.LimitReader(source, remaining+1))
	if err != nil {
		return 0, fmt.Errorf("写入皮肤文件失败: %w", err)
	}
	if written > remaining {
		return 0, fmt.Errorf("皮肤包解压后超过限制")
	}
	return written, nil
}

// findSkinManifests 收集解压目录下的 skin.json，必须恰好一个
func findSkinManifests(folder string) ([]string, error) {
	var manifests []string
	err := filepath.WalkDir(folder, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Name() == "skin.json" {
			manifests = append(manifests, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("读取皮肤包失败: %w", err)
	}
	if len(manifests) != 1 {
		return nil, fmt.Errorf("皮肤包必须恰好包含一个 skin.json")
	}
	return manifests, nil
}

// FindSkin 导出 find_skin，供 WebUI 路由按 ID 读取皮肤
func FindSkin(root, skinID string, assets bool) (SkinDocument, error) {
	return findSkin(root, skinID, assets)
}

// InstallSkinZip 导出 install_zip，供 WebUI 路由安装皮肤包
func InstallSkinZip(root string, payload []byte) (SkinDocument, error) {
	return installSkinZip(root, payload)
}

package imagesystem

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StoredImagePathError 图片存储路径不在实例目录内
type StoredImagePathError struct {
	Path   string
	Reason string
}

func (e *StoredImagePathError) Error() string {
	return e.Reason
}

// ResolveStoredImagePath 将数据库中的图片路径解析为实例目录内的真实路径
// 对齐 Python resolve_stored_image_path：相对路径按实例根目录解析，绝对路径也必须在根目录内
func ResolveStoredImagePath(root, storedPath string) (string, error) {
	raw := strings.TrimSpace(storedPath)
	if raw == "" {
		return "", &StoredImagePathError{Reason: "图片路径不能为空"}
	}
	rootAbs, err := resolveRoot(root)
	if err != nil {
		return "", err
	}
	target := raw
	if !filepath.IsAbs(target) {
		target = filepath.Join(rootAbs, target)
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", &StoredImagePathError{Path: raw, Reason: fmt.Sprintf("解析图片路径失败: %v", err)}
	}
	targetAbs = filepath.Clean(targetAbs)
	if resolved, err := filepath.EvalSymlinks(targetAbs); err == nil {
		targetAbs = resolved
	}
	if !pathWithin(rootAbs, targetAbs) {
		return "", &StoredImagePathError{Path: raw, Reason: "图片路径不在项目目录内: " + raw}
	}
	return targetAbs, nil
}

// SerializeStoredImagePath 将实例内图片路径序列化为相对实例根目录的存储路径
func SerializeStoredImagePath(root, fullPath string) (string, error) {
	raw := strings.TrimSpace(fullPath)
	if raw == "" {
		return "", &StoredImagePathError{Reason: "图片路径不能为空"}
	}
	rootAbs, err := resolveRoot(root)
	if err != nil {
		return "", err
	}
	fullAbs, err := filepath.Abs(raw)
	if err != nil {
		return "", &StoredImagePathError{Path: raw, Reason: fmt.Sprintf("解析图片路径失败: %v", err)}
	}
	fullAbs = filepath.Clean(fullAbs)
	if resolved, err := filepath.EvalSymlinks(fullAbs); err == nil {
		fullAbs = resolved
	}
	if !pathWithin(rootAbs, fullAbs) {
		return "", &StoredImagePathError{Path: raw, Reason: "图片路径不在项目目录内: " + raw}
	}
	relative, err := filepath.Rel(rootAbs, fullAbs)
	if err != nil {
		return "", &StoredImagePathError{Path: raw, Reason: fmt.Sprintf("计算相对路径失败: %v", err)}
	}
	return filepath.ToSlash(relative), nil
}

// StoredImagePathsEqual 按实例内真实路径语义比较两个图片存储路径
func StoredImagePathsEqual(root, left, right string) bool {
	leftPath, leftErr := ResolveStoredImagePath(root, left)
	rightPath, rightErr := ResolveStoredImagePath(root, right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return leftPath == rightPath
}

func resolveRoot(root string) (string, error) {
	value := strings.TrimSpace(root)
	if value == "" {
		value = "."
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("解析实例目录失败: %w", err)
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return abs, nil
}

func pathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if relative == "." {
		return true
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

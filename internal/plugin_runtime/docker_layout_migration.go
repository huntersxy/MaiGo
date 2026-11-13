// Docker 旧布局插件数据目录迁移，对齐 src/plugin_runtime/docker_layout_migration.py
package pluginruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PluginLayoutMigrationVersion 是迁移报告协议版本
const PluginLayoutMigrationVersion = 1

// 缺省路径与 Python 入口脚本保持一致
const (
	DefaultPluginLayoutSourceRoot = "/MaiMBot/plugins"
	DefaultPluginLayoutDataRoot   = "/MaiMBot/data/plugins"
	DefaultPluginLayoutReportPath = "/MaiMBot/data/plugin_layout_migration_v1.json"
)

var pluginLayoutSourceMarkers = []string{"plugin.py", "_manifest.json"}

// PluginLayoutMigrationIssue 记录需要人工确认或无法自动处理的目录
type PluginLayoutMigrationIssue struct {
	PluginID string `json:"plugin_id,omitempty"`
	Path     string `json:"path,omitempty"`
	Reason   string `json:"reason"`
	Paths    string `json:"paths,omitempty"`
}

// PluginLayoutMigrationReport 记录一次插件目录布局迁移的结果
type PluginLayoutMigrationReport struct {
	Version          int                          `json:"version"`
	Completed        bool                         `json:"completed"`
	Moved            []string                     `json:"moved"`
	RemovedEmpty     []string                     `json:"removed_empty"`
	SkippedAmbiguous []PluginLayoutMigrationIssue `json:"skipped_ambiguous"`
	Conflicts        []PluginLayoutMigrationIssue `json:"conflicts"`
	InvalidSources   []PluginLayoutMigrationIssue `json:"invalid_sources"`
}

// RequiresManualReview 返回是否存在需要人工确认的目录
func (r *PluginLayoutMigrationReport) RequiresManualReview() bool {
	if r == nil {
		return false
	}
	return len(r.SkippedAmbiguous) > 0 || len(r.Conflicts) > 0 || len(r.InvalidSources) > 0
}

// NewPluginLayoutMigrationReport 构造带空数组的报告，避免 JSON 输出 null
func NewPluginLayoutMigrationReport() *PluginLayoutMigrationReport {
	return &PluginLayoutMigrationReport{
		Version:          PluginLayoutMigrationVersion,
		Completed:        true,
		Moved:            []string{},
		RemovedEmpty:     []string{},
		SkippedAmbiguous: []PluginLayoutMigrationIssue{},
		Conflicts:        []PluginLayoutMigrationIssue{},
		InvalidSources:   []PluginLayoutMigrationIssue{},
	}
}

// LoadPluginLayoutMigrationReport 读取已有的一次性迁移报告并严格校验完成标记
func LoadPluginLayoutMigrationReport(reportPath string) (*PluginLayoutMigrationReport, error) {
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		return nil, err
	}
	var report PluginLayoutMigrationReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, fmt.Errorf("插件目录迁移报告解析失败: %w", err)
	}
	if report.Version != PluginLayoutMigrationVersion || !report.Completed {
		return nil, errors.New("插件目录迁移报告版本无效或迁移未完成")
	}
	normalizePluginLayoutReport(&report)
	return &report, nil
}

// MigratePluginLayout 尽力迁移能够明确识别的旧插件数据目录
// 返回 nil 表示源码目录和数据目录仍指向同一位置，不会执行任何文件操作
func MigratePluginLayout(sourceRoot, dataRoot, reportPath string) (*PluginLayoutMigrationReport, error) {
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		return nil, fmt.Errorf("创建插件源码目录失败: %w", err)
	}
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		return nil, fmt.Errorf("创建插件数据目录失败: %w", err)
	}
	sourceRoot, err := resolveMigrationPath(sourceRoot)
	if err != nil {
		return nil, err
	}
	dataRoot, err = resolveMigrationPath(dataRoot)
	if err != nil {
		return nil, err
	}

	sourceInfo, err := os.Stat(sourceRoot)
	if err != nil {
		return nil, err
	}
	dataInfo, err := os.Stat(dataRoot)
	if err != nil {
		return nil, err
	}
	if os.SameFile(sourceInfo, dataInfo) {
		return nil, nil
	}

	existing, err := loadExistingPluginLayoutReport(reportPath)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	report := NewPluginLayoutMigrationReport()
	sources, err := discoverPluginLayoutSources(sourceRoot, report)
	if err != nil {
		return nil, err
	}

	pluginIDs := make([]string, 0, len(sources))
	for pluginID := range sources {
		pluginIDs = append(pluginIDs, pluginID)
	}
	sort.Slice(pluginIDs, func(i, j int) bool {
		left, right := strings.ToLower(pluginIDs[i]), strings.ToLower(pluginIDs[j])
		if left == right {
			return pluginIDs[i] < pluginIDs[j]
		}
		return left < right
	})

	for _, pluginID := range pluginIDs {
		paths := sources[pluginID]
		if len(paths) != 1 {
			display := make([]string, 0, len(paths))
			for _, path := range paths {
				display = append(display, path)
			}
			report.Conflicts = append(report.Conflicts, PluginLayoutMigrationIssue{
				PluginID: pluginID,
				Reason:   "多个源码目录声明了同一个插件 ID",
				Paths:    strings.Join(display, ", "),
			})
			continue
		}

		pluginSourcePath := paths[0]
		oldDataPath := filepath.Join(sourceRoot, pluginID)
		oldInfo, statErr := os.Lstat(oldDataPath)
		if statErr != nil {
			if errors.Is(statErr, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("检查旧插件数据目录失败 %s: %w", oldDataPath, statErr)
		}
		if filepath.Clean(oldDataPath) == filepath.Clean(pluginSourcePath) {
			report.SkippedAmbiguous = append(report.SkippedAmbiguous, PluginLayoutMigrationIssue{
				PluginID: pluginID,
				Path:     oldDataPath,
				Reason:   "源码目录名与插件 ID 相同，无法自动区分历史运行数据",
			})
			continue
		}
		if oldInfo.Mode()&os.ModeSymlink != 0 {
			report.SkippedAmbiguous = append(report.SkippedAmbiguous, PluginLayoutMigrationIssue{
				PluginID: pluginID,
				Path:     oldDataPath,
				Reason:   "旧目录是符号链接",
			})
			continue
		}
		if !oldInfo.IsDir() {
			report.Conflicts = append(report.Conflicts, PluginLayoutMigrationIssue{
				PluginID: pluginID,
				Path:     oldDataPath,
				Reason:   "旧数据路径不是目录",
			})
			continue
		}
		if pluginLayoutHasSourceMarker(oldDataPath) {
			report.SkippedAmbiguous = append(report.SkippedAmbiguous, PluginLayoutMigrationIssue{
				PluginID: pluginID,
				Path:     oldDataPath,
				Reason:   "旧目录包含插件源码标志",
			})
			continue
		}

		newDataPath := filepath.Join(dataRoot, pluginID)
		newInfo, newErr := os.Lstat(newDataPath)
		if newErr != nil && !errors.Is(newErr, fs.ErrNotExist) {
			return nil, fmt.Errorf("检查新插件数据目录失败 %s: %w", newDataPath, newErr)
		}
		if newErr == nil {
			if newInfo.Mode()&os.ModeSymlink != 0 || !newInfo.IsDir() {
				report.Conflicts = append(report.Conflicts, PluginLayoutMigrationIssue{
					PluginID: pluginID,
					Path:     newDataPath,
					Reason:   "新数据路径不是普通目录",
				})
				continue
			}
			oldEmpty, err := pluginLayoutDirEmpty(oldDataPath)
			if err != nil {
				return nil, err
			}
			newEmpty, err := pluginLayoutDirEmpty(newDataPath)
			if err != nil {
				return nil, err
			}
			if oldEmpty {
				if err := os.Remove(oldDataPath); err != nil {
					return nil, fmt.Errorf("清理空旧数据目录失败 %s: %w", oldDataPath, err)
				}
				report.RemovedEmpty = append(report.RemovedEmpty, pluginID)
				continue
			}
			if newEmpty {
				if err := os.Remove(newDataPath); err != nil {
					return nil, fmt.Errorf("清理空新数据目录失败 %s: %w", newDataPath, err)
				}
				if err := os.Rename(oldDataPath, newDataPath); err != nil {
					return nil, fmt.Errorf("迁移插件数据目录失败 %s -> %s: %w", oldDataPath, newDataPath, err)
				}
				report.Moved = append(report.Moved, pluginID)
				continue
			}
			report.Conflicts = append(report.Conflicts, PluginLayoutMigrationIssue{
				PluginID: pluginID,
				Path:     oldDataPath,
				Reason:   "新旧数据目录都非空，拒绝自动合并",
			})
			continue
		}

		if err := os.Rename(oldDataPath, newDataPath); err != nil {
			return nil, fmt.Errorf("迁移插件数据目录失败 %s -> %s: %w", oldDataPath, newDataPath, err)
		}
		report.Moved = append(report.Moved, pluginID)
	}

	if err := writePluginLayoutMigrationReport(reportPath, report); err != nil {
		return nil, err
	}
	return report, nil
}

func resolveMigrationPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return abs, nil
	}
	return "", err
}

func loadExistingPluginLayoutReport(reportPath string) (*PluginLayoutMigrationReport, error) {
	if _, err := os.Stat(reportPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		return nil, err
	}
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	if _, ok := probe.(map[string]any); !ok {
		return nil, errors.New("插件目录迁移报告必须是 JSON 对象")
	}
	return LoadPluginLayoutMigrationReport(reportPath)
}

func discoverPluginLayoutSources(sourceRoot string, report *PluginLayoutMigrationReport) (map[string][]string, error) {
	entries, err := os.ReadDir(sourceRoot)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
	sources := make(map[string][]string)
	for _, entry := range entries {
		pluginPath := filepath.Join(sourceRoot, entry.Name())
		info, err := os.Lstat(pluginPath)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !info.IsDir() {
			continue
		}
		if !pluginLayoutSourceMarkersAreFiles(pluginPath) {
			continue
		}
		pluginID, err := readPluginLayoutID(pluginPath)
		if err != nil {
			report.InvalidSources = append(report.InvalidSources, PluginLayoutMigrationIssue{
				Path:   pluginPath,
				Reason: err.Error(),
			})
			continue
		}
		sources[pluginID] = append(sources[pluginID], pluginPath)
	}
	return sources, nil
}

func pluginLayoutSourceMarkersAreFiles(pluginPath string) bool {
	for _, marker := range pluginLayoutSourceMarkers {
		info, err := os.Stat(filepath.Join(pluginPath, marker))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

func readPluginLayoutID(pluginPath string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(pluginPath, "_manifest.json"))
	if err != nil {
		return "", err
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", err
	}
	pluginID, ok := manifest["id"].(string)
	if !ok {
		return "", errors.New("_manifest.json 缺少可安全用作目录名的插件 ID")
	}
	pluginID = strings.TrimSpace(pluginID)
	if !isSafePluginLayoutID(pluginID) {
		return "", errors.New("_manifest.json 缺少可安全用作目录名的插件 ID")
	}
	return pluginID, nil
}

func isSafePluginLayoutID(pluginID string) bool {
	return pluginID != "" && pluginID != "." && pluginID != ".." &&
		!strings.ContainsRune(pluginID, '/') && !strings.ContainsRune(pluginID, rune(92))
}

func pluginLayoutHasSourceMarker(path string) bool {
	if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
		return true
	}
	for _, marker := range pluginLayoutSourceMarkers {
		if _, err := os.Lstat(filepath.Join(path, marker)); err == nil {
			return true
		}
	}
	return false
}

func pluginLayoutDirEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

func writePluginLayoutMigrationReport(reportPath string, report *PluginLayoutMigrationReport) error {
	if err := os.MkdirAll(filepath.Dir(reportPath), 0o755); err != nil {
		return err
	}
	normalizePluginLayoutReport(report)
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return err
	}
	temporaryPath := filepath.Join(filepath.Dir(reportPath), "."+filepath.Base(reportPath)+".tmp")
	if err := os.WriteFile(temporaryPath, buf.Bytes(), 0o644); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, reportPath); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	return nil
}

func normalizePluginLayoutReport(report *PluginLayoutMigrationReport) {
	if report.Moved == nil {
		report.Moved = []string{}
	}
	if report.RemovedEmpty == nil {
		report.RemovedEmpty = []string{}
	}
	if report.SkippedAmbiguous == nil {
		report.SkippedAmbiguous = []PluginLayoutMigrationIssue{}
	}
	if report.Conflicts == nil {
		report.Conflicts = []PluginLayoutMigrationIssue{}
	}
	if report.InvalidSources == nil {
		report.InvalidSources = []PluginLayoutMigrationIssue{}
	}
}

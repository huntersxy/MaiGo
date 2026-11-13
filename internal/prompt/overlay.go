package prompt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"
)

const (
	// CustomPromptsDataSubdir 相对 MaiBot 实例根目录，对齐 data/custom_prompts
	CustomPromptsDataSubdir = "data/custom_prompts"
)

var safePromptNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// CustomPromptsRoot 返回实例目录下的 custom_prompts 根路径
func CustomPromptsRoot(instanceRoot string) string {
	return filepath.Join(filepath.Clean(instanceRoot), filepath.FromSlash(CustomPromptsDataSubdir))
}

// LoadFromDir 从 custom_prompts 目录加载覆盖层（可选；目录不存在则清空覆盖）
//
// 布局对齐 MaiBot prompt_i18n：
//   - {root}/{locale}/*.prompt 按 locale 覆盖
//   - {root}/*.prompt 遗留平铺文件，作用于当前合并结果的全局覆盖
//   - 同名 .meta.toml/.meta.json 与父目录元数据随模板一起加载
func (m *Manager) LoadFromDir(customRoot string) error {
	newCustom, newMeta, newLegacy, newLegacyMeta, snap, err := scanCustomPromptsDir(customRoot)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.customByLocale = newCustom
	m.customMetaByLocale = newMeta
	m.legacyCustom = newLegacy
	m.legacyMeta = newLegacyMeta
	m.customRoot = filepath.Clean(customRoot)
	m.customSnap = snap
	m.merged = mergeWithCustom(m.byLocale, m.customByLocale, m.legacyCustom, m.locale)
	m.applyRegisteredLocked()
	m.bumpCacheRevisionLocked()
	return nil
}

func scanCustomPromptsDir(customRoot string) (map[string]map[string]string, map[string]map[string]PromptMetadata, map[string]string, map[string]PromptMetadata, modSnap, error) {
	root := filepath.Clean(customRoot)
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil, nil, modSnap{}, nil
		}
		return nil, nil, nil, nil, modSnap{}, err
	}
	if !info.IsDir() {
		return nil, nil, nil, nil, modSnap{}, fmt.Errorf("custom_prompts 路径不是目录: %q", root)
	}

	customByLocale := make(map[string]map[string]string)
	customMetaByLocale := make(map[string]map[string]PromptMetadata)
	legacy := make(map[string]string)
	legacyMeta := make(map[string]PromptMetadata)

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, nil, nil, modSnap{}, fmt.Errorf("读取 custom_prompts 目录: %w", err)
	}

	for _, ent := range entries {
		if ent.IsDir() {
			locale, normErr := NormalizeLocale(ent.Name())
			if normErr != nil || locale != ent.Name() {
				continue
			}
			localeDir := filepath.Join(root, locale)
			layer, meta, layerErr := readPromptFilesInDir(localeDir)
			if layerErr != nil {
				return nil, nil, nil, nil, modSnap{}, layerErr
			}
			if len(layer) > 0 {
				customByLocale[locale] = layer
			}
			if len(meta) > 0 {
				customMetaByLocale[locale] = meta
			}
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(name, ".prompt") {
			continue
		}
		stem := strings.TrimSuffix(name, ".prompt")
		if !safePromptName(stem) {
			continue
		}
		content, readErr := os.ReadFile(filepath.Join(root, name))
		if readErr != nil {
			return nil, nil, nil, nil, modSnap{}, fmt.Errorf("读取遗留 custom prompt %q: %w", name, readErr)
		}
		legacy[stem] = string(content)
		legacyMeta[stem] = loadPromptMetadata(stem, func(fileName string) map[string]any {
			return readPromptMetadataFile(filepath.Join(root, fileName))
		})
	}

	snap, snapErr := dirModSnap(root)
	if snapErr != nil {
		return nil, nil, nil, nil, modSnap{}, snapErr
	}
	return customByLocale, customMetaByLocale, legacy, legacyMeta, snap, nil
}

func readPromptFilesInDir(dir string) (map[string]string, map[string]PromptMetadata, error) {
	out := make(map[string]string)
	meta := make(map[string]PromptMetadata)
	err := filepath.WalkDir(dir, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".prompt") {
			return nil
		}
		rel, relErr := filepath.Rel(dir, filePath)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		category := ""
		if index := strings.LastIndex(rel, "/"); index >= 0 {
			category = rel[:index]
			for _, part := range strings.Split(category, "/") {
				if !safePromptName(part) {
					return nil
				}
			}
		}
		stem := strings.TrimSuffix(name, ".prompt")
		if !safePromptName(stem) {
			return nil
		}
		key := stem
		if category != "" {
			key = category + "/" + stem
		}
		content, readErr := os.ReadFile(filePath)
		if readErr != nil {
			return fmt.Errorf("读取 custom prompt %q: %w", filePath, readErr)
		}
		if _, exists := out[key]; exists {
			return fmt.Errorf("locale 目录 %q 下重复的 prompt 名称 %q", dir, key)
		}
		out[key] = string(content)
		base := filepath.Dir(filePath)
		meta[key] = loadPromptMetadata(stem, func(fileName string) map[string]any {
			return readPromptMetadataFile(filepath.Join(base, fileName))
		})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return out, meta, nil
}

func safePromptName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return safePromptNamePattern.MatchString(name)
}

func mergeWithCustom(
	byLocale map[string]map[string]string,
	customByLocale map[string]map[string]string,
	legacyCustom map[string]string,
	locale string,
) map[string]string {
	merged := mergeLocaleLayers(byLocale, locale)
	if layer := customByLocale[DefaultLocale]; len(layer) > 0 {
		for name, tpl := range layer {
			merged[name] = tpl
		}
	}
	if locale != DefaultLocale {
		if layer := customByLocale[locale]; len(layer) > 0 {
			for name, tpl := range layer {
				merged[name] = tpl
			}
		}
	}
	for name, tpl := range legacyCustom {
		merged[name] = tpl
	}
	return merged
}

// OverlayWatcherOptions 控制 custom_prompts 轮询参数（与实例 TOML 监视器同量级）
type OverlayWatcherOptions struct {
	PollInterval time.Duration
	Debounce     time.Duration
	Logger       *zap.Logger
}

func (o *OverlayWatcherOptions) applyDefaults() {
	if o.PollInterval <= 0 {
		o.PollInterval = 500 * time.Millisecond
	}
	if o.Debounce <= 0 {
		o.Debounce = 600 * time.Millisecond
	}
	if o.Logger == nil {
		o.Logger = zap.L()
	}
}

// StartCustomOverlayWatcher 轮询 custom_prompts 目录，防抖后调用 LoadFromDir
func StartCustomOverlayWatcher(ctx context.Context, customRoot string, mgr *Manager, opts OverlayWatcherOptions) {
	if mgr == nil {
		return
	}
	opts.applyDefaults()
	log := opts.Logger
	root := filepath.Clean(customRoot)

	go func() {
		var committed modSnap
		var debounceUntil time.Time
		var settling modSnap
		var haveSettling bool

		ticker := time.NewTicker(opts.PollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				snap, err := dirModSnap(root)
				if err != nil {
					continue
				}
				if snap == committed {
					continue
				}
				if !haveSettling || snap != settling {
					settling = snap
					haveSettling = true
					debounceUntil = time.Now().Add(opts.Debounce)
				}
				if debounceUntil.IsZero() || time.Now().Before(debounceUntil) {
					continue
				}
				debounceUntil = time.Time{}
				haveSettling = false

				if err := mgr.LoadFromDir(root); err != nil {
					log.Warn("custom_prompts 热重载失败，沿用上一版覆盖",
						zap.String("custom_prompts_dir", root),
						zap.Error(err))
					continue
				}
				committed = snap
				log.Info("custom_prompts 热重载成功",
					zap.String("custom_prompts_dir", root),
					zap.Int("template_count", len(mgr.Names())))
			}
		}
	}()
}

type modSnap struct {
	latest time.Time
	files  int
}

func dirModSnap(root string) (modSnap, error) {
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return modSnap{}, nil
		}
		return modSnap{}, err
	}
	if !info.IsDir() {
		return modSnap{}, fmt.Errorf("不是目录")
	}
	var snap modSnap
	// 遍历失败不应静默：返回已收集快照并记录告警
	if walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !isOverlayMetadataFile(d.Name()) && !strings.HasSuffix(d.Name(), ".prompt") {
			return nil
		}
		fi, statErr := d.Info()
		if statErr != nil {
			return statErr
		}
		snap.files++
		if fi.ModTime().After(snap.latest) {
			snap.latest = fi.ModTime()
		}
		return nil
	}); walkErr != nil {
		zap.L().Warn("扫描 custom_prompts 目录失败，返回已收集快照",
			zap.String("root", root), zap.Error(walkErr))
	}
	return snap, nil
}

func isOverlayMetadataFile(name string) bool {
	return name == ".meta.toml" || name == ".meta.json" || strings.HasSuffix(name, ".meta.toml") || strings.HasSuffix(name, ".meta.json")
}

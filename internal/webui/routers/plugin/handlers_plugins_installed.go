package plugin

import (
	"MaiGo/internal/webui/httputil"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	maibotcfg "MaiGo/internal/config/maibot"
	"MaiGo/internal/plugin_market"
	pluginruntime "MaiGo/internal/plugin_runtime"
	"MaiGo/internal/plugin_runtime/circuitbreaker"
	chinesetypo "MaiGo/plugins/built_in/chinese_typo"
)

// handlePluginInstalled 对齐 GET /plugins/installed
func (d Deps) handlePluginInstalled(w http.ResponseWriter, r *http.Request) {
	records := make(map[string]pluginmarketHostRecord)
	if d.PluginHost != nil {
		for _, record := range d.PluginHost.ListPlugins() {
			records[record.PluginID] = pluginmarketHostRecord{loaded: true, configError: record.ConfigError}
		}
	}
	usageAvailable := true
	usage := map[string]float64{}
	if d.PluginHost != nil {
		usageAvailable = d.PluginHost.UsageAvailable()
		if usageAvailable {
			if snapshot := d.PluginHost.UsageSnapshot(); snapshot != nil {
				usage = snapshot
			}
		}
	}
	circuits := circuitbreaker.Default().Statuses()
	candidates, err := d.pluginCandidates()
	if err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"detail": "服务器错误: " + err.Error()})
		return
	}
	plugins := make([]map[string]any, 0, len(candidates))
	seen := make(map[string]struct{})
	for _, candidate := range candidates {
		name := candidate.name
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "__") || name == "data" {
			continue
		}
		pluginPath := candidate.path
		manifest, loadErr := candidate.loadManifest()
		if loadErr != nil || manifest == nil {
			continue
		}
		if _, ok := manifest["name"]; !ok {
			continue
		}
		if _, ok := manifest["version"]; !ok {
			continue
		}
		pluginID := strings.TrimSpace(stringFromAny(manifest["id"]))
		if pluginID == "" {
			continue
		}
		if _, duplicate := seen[pluginID]; duplicate {
			continue
		}
		seen[pluginID] = struct{}{}

		enabled := true
		configError := ""
		configPath := d.pluginCandidateConfigPath(candidate, pluginID)
		var enabledValue bool
		var enabledErr error
		if pluginID == chinesetypo.PluginID {
			raw, err := readPluginConfigText(configPath)
			enabledErr = err
			if err == nil {
				enabledValue, enabledErr = readBuiltinTypoEnabled(raw)
			}
		} else if pluginruntime.IsNativeAdapter(pluginID) {
			enabledValue, enabledErr = readNativePluginEnabled(configPath)
		} else {
			enabledValue, enabledErr = readPluginEnabled(configPath)
		}
		if enabledErr != nil {
			configError = "读取插件 " + pluginID + " 启用配置失败：" + enabledErr.Error()
		} else {
			enabled = enabledValue
		}
		record, registered := records[pluginID]
		loadStatus := "not_loaded"
		loadError := ""
		if d.PluginHost != nil {
			statuses := d.PluginHost.PluginLoadStatuses()
			reasons := d.PluginHost.PluginLoadFailureReasons()
			if status, ok := statuses[pluginID]; ok {
				loadStatus = status
				if status == "failed" || status == "timed_out" {
					loadError = reasons[pluginID]
				}
			} else {
				loadStatus = d.PluginHost.StartupLoadStatus()
				if loadStatus == "failed" || loadStatus == "timed_out" {
					loadError = d.PluginHost.StartupLoadReason()
				}
			}
			// 对齐 Python integration.py:309 的 _plugin_transitions：加载/停止中优先展示过渡状态
			if transition := d.PluginHost.PluginTransition(pluginID); transition != "" {
				loadStatus = transition
			}
		} else if registered {
			loadStatus = "success"
		}
		if record.configError != "" {
			loadStatus = "failed"
			loadError = record.configError
		}
		if configError != "" {
			loadStatus = "failed"
			loadError = configError
		}
		effectiveStatus := loadStatus
		// 配置禁用后运行时状态可能滞后；启停过渡仍展示过渡态，其余一律按禁用展示
		if !enabled && loadStatus != "loading" && loadStatus != "stopping" {
			effectiveStatus = "disabled"
		}
		release, _ := pluginmarket.ReadReleaseReceipt(pluginPath)
		var circuitStatus any
		if status, ok := circuits[pluginID]; ok {
			circuitStatus = status
		}
		plugin := map[string]any{
			"id":             pluginID,
			"manifest":       manifest,
			"builtin":        candidate.builtin,
			"path":           pluginPath,
			"enabled":        enabled,
			"disabled":       !enabled,
			"loaded":         effectiveStatus == "success",
			"load_status":    effectiveStatus,
			"status_badges":  pluginStatusBadges(d.PluginHost, pluginID, effectiveStatus),
			"load_error":     loadErrorOrEmpty(loadError, effectiveStatus),
			"circuit_status": circuitStatus,
			"changelog":      readLocalPluginChangelog(pluginPath),
			"release":        release,
		}
		if usageAvailable {
			plugin["usage_seconds"] = usage[pluginID]
		}
		plugins = append(plugins, plugin)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "plugins": plugins, "total": len(plugins)})
}

type pluginmarketHostRecord struct {
	loaded      bool
	configError string
}

func loadErrorOrEmpty(loadError, status string) string {
	if status == "failed" || status == "timed_out" {
		return loadError
	}
	return ""
}

// pluginStatusBadges 只给已加载插件返回运行标记
func pluginStatusBadges(host *pluginruntime.Host, pluginID, status string) []map[string]any {
	if host == nil || status != "success" {
		return []map[string]any{}
	}
	badges := host.RuntimeStatusBadgesSnapshot()[pluginID]
	if badges == nil {
		return []map[string]any{}
	}
	return badges
}

// handlePluginUsage 对齐 GET /plugins/usage
func (d Deps) handlePluginUsage(w http.ResponseWriter, r *http.Request) {
	if d.PluginHost != nil && !d.PluginHost.UsageAvailable() {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"usage_seconds": nil})
		return
	}
	usage := map[string]float64{}
	if d.PluginHost != nil {
		if snapshot := d.PluginHost.UsageSnapshot(); snapshot != nil {
			usage = snapshot
		}
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"usage_seconds": usage})
}

// handlePluginMarketplaceReviewIdentity 对齐 GET /plugins/marketplace/review-identity
func (d Deps) handlePluginMarketplaceReviewIdentity(w http.ResponseWriter, r *http.Request) {
	username := ""
	if dir := strings.TrimSpace(d.InstanceDir); dir != "" {
		cfg, err := maibotcfg.LoadFromInstanceDir(dir)
		if err != nil {
			httputil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"detail": "服务器错误: " + err.Error()})
			return
		}
		username = strings.TrimSpace(cfg.Bot.Bot.Nickname)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"username": username})
}

type pluginCandidate struct {
	name     string
	path     string
	builtin  bool
	manifest map[string]any
}

func (c pluginCandidate) loadManifest() (map[string]any, error) {
	if c.manifest != nil {
		return c.manifest, nil
	}
	return pluginmarket.LoadManifestJSON(filepath.Join(c.path, "_manifest.json"))
}

func (d Deps) pluginCandidateConfigPath(candidate pluginCandidate, id string) string {
	if id == chinesetypo.PluginID {
		return d.builtinTypoConfigPath()
	}
	return pluginruntime.ResolvePluginConfigPath(candidate.path)
}

// pluginCandidates 返回第三方与内置插件目录
func (d Deps) pluginCandidates() ([]pluginCandidate, error) {
	root, err := pluginmarket.ResolvePluginsDir(d.InstanceDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	path := pluginruntime.FindBuiltinPluginPath(chinesetypo.PluginID)
	if path == "" {
		path = filepath.Dir(d.builtinTypoConfigPath())
	}
	candidates := []pluginCandidate{{name: chinesetypo.Directory, path: path, builtin: true, manifest: chinesetypo.Manifest()}}
	for _, entry := range entries {
		if entry.IsDir() {
			candidates = append(candidates, pluginCandidate{name: entry.Name(), path: filepath.Join(root, entry.Name())})
		}
	}
	builtinRoot := pluginruntime.BuiltinPluginDir()
	if builtinRoot == "" {
		return candidates, nil
	}
	builtinEntries, err := os.ReadDir(builtinRoot)
	if err != nil {
		return nil, err
	}
	for _, entry := range builtinEntries {
		if entry.IsDir() && entry.Name() != chinesetypo.Directory {
			candidates = append(candidates, pluginCandidate{name: entry.Name(), path: filepath.Join(builtinRoot, entry.Name()), builtin: true})
		}
	}
	return candidates, nil
}

func readPluginEnabled(configPath string) (bool, error) {
	raw, err := os.ReadFile(filepath.Clean(configPath))
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return true, err
	}
	var document map[string]any
	if err := toml.Unmarshal(raw, &document); err != nil {
		return true, err
	}
	pluginSection, ok := document["plugin"].(map[string]any)
	if !ok {
		return true, nil
	}
	value, ok := pluginSection["enabled"]
	if !ok {
		return true, nil
	}
	return coerceEnabledValue(value), nil
}

// readNativePluginEnabled 原生适配器门闩：缺文件/缺键=false
func readNativePluginEnabled(configPath string) (bool, error) {
	raw, err := os.ReadFile(filepath.Clean(configPath))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var document map[string]any
	if err := toml.Unmarshal(raw, &document); err != nil {
		return false, err
	}
	pluginSection, ok := document["plugin"].(map[string]any)
	if !ok {
		return false, nil
	}
	value, ok := pluginSection["enabled"]
	if !ok {
		return false, nil
	}
	return coerceEnabledValue(value), nil
}

var pluginChangelogFilenames = []string{"CHANGELOG.md", "Changelog.md", "changelog.md", "CHANGELOG.MD"}

func readLocalPluginChangelog(pluginPath string) any {
	for _, name := range pluginChangelogFilenames {
		raw, err := os.ReadFile(filepath.Join(pluginPath, name))
		if err != nil {
			continue
		}
		text := string(raw)
		runes := []rune(text)
		if len(runes) > 500000 {
			text = string(runes[:500000])
		}
		return text
	}
	return nil
}

func stringFromAny(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

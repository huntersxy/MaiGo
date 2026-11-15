package webuiapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	webuiutils "MaiGo/internal/webui/utils"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	maibotcfg "MaiGo/internal/config/maibot"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"
	"github.com/pelletier/go-toml/v2"
)

const (
	botConfigFileName   = "bot_config.toml"
	modelConfigFileName = "model_config.toml"
	modelVersionDirName = "versions"
)

// configDir 返回实例配置目录（MaiBot CONFIG_DIR）
func (d Deps) configDir() (string, bool) {
	dir := strings.TrimSpace(d.InstanceDir)
	if dir == "" {
		return "", false
	}
	return filepath.Join(filepath.Clean(dir), "config"), true
}

func (d Deps) botConfigPath() (string, bool) {
	dir, ok := d.configDir()
	if !ok {
		return "", false
	}
	return filepath.Join(dir, botConfigFileName), true
}

func (d Deps) modelConfigPath() (string, bool) {
	dir, ok := d.configDir()
	if !ok {
		return "", false
	}
	return filepath.Join(dir, modelConfigFileName), true
}

func modelVersionsDir(configDir string) string {
	return filepath.Join(configDir, modelVersionDirName, "model")
}

func modelVersionManifestPath(configDir string) string {
	return filepath.Join(modelVersionsDir(configDir), "manifest.json")
}

// writeInstanceDirMissing 实例目录未配置时的统一响应
func writeInstanceDirMissing(w http.ResponseWriter, path string) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"detail":      "需要设置 MAIBOT_INSTANCE_DIR 指向 MaiBot 实例根目录",
		"maibot_path": path,
		"implemented": true,
		"success":     false,
	})
}

// decodeAnyBody 解析任意 JSON 请求体
func decodeAnyBody(r *http.Request) (any, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var value any
	if err := sonic.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func readBody(r *http.Request) []byte {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		return nil
	}
	return raw
}

func jsonUnmarshalStrict(data []byte, target any) error {
	return sonic.Unmarshal(data, target)
}

func chiURLParam(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}

// writeConfigReadError 将读配置错误映射为 Python 的 404/500 文案
func writeConfigReadError(w http.ResponseWriter, err error) {
	var missing *os.PathError
	if errors.As(err, &missing) && errors.Is(missing.Err, os.ErrNotExist) {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "配置文件不存在"})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "读取配置文件失败: " + err.Error()})
}

// readTOMLDocument 读取并解析 TOML 文档（map 形态）
// readTOMLDocument 委托 webui/utils，与 Python src/webui/utils/toml_utils.py 同层
func readTOMLDocument(path string) (map[string]any, error) {
	return webuiutils.ReadTOMLDocument(path)
}

// writeTOMLDocument 委托 webui/utils（不保留原注释，见该包已知差异说明）
func writeTOMLDocument(path string, doc map[string]any) error {
	return webuiutils.WriteTOMLDocument(path, doc)
}

// writeFileAtomic 保留为包内入口，实现委托 webui/utils
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	return webuiutils.WriteFileAtomic(path, data, perm)
}

// coerceIntegralFloats 将 JSON 反序列化产生的整数值 float64 转为 int64
func coerceIntegralFloats(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			typed[key] = coerceIntegralFloats(item)
		}
		return typed
	case []any:
		for index, item := range typed {
			typed[index] = coerceIntegralFloats(item)
		}
		return typed
	case float64:
		if typed == math.Trunc(typed) && typed >= math.MinInt64 && typed <= math.MaxInt64 {
			return int64(typed)
		}
		return typed
	default:
		return value
	}
}

// validateBotConfigMap 校验 bot_config 数据结构
func validateBotConfigMap(doc map[string]any) error {
	raw, err := toml.Marshal(coerceIntegralFloats(doc))
	if err != nil {
		return err
	}
	var file maibotcfg.BotConfigFile
	if err := toml.Unmarshal(raw, &file); err != nil {
		return err
	}
	if err := maibotcfg.ValidateFilesConfig(file); err != nil {
		return err
	}
	if err := maibotcfg.ValidateKeywordReaction(file); err != nil {
		return err
	}
	return maibotcfg.ValidateReplyPostProcess(file)
}

// validateModelConfigMap 校验 model_config 数据结构
func validateModelConfigMap(doc map[string]any) error {
	raw, err := toml.Marshal(coerceIntegralFloats(doc))
	if err != nil {
		return err
	}
	var file maibotcfg.ModelConfigFile
	if err := toml.Unmarshal(raw, &file); err != nil {
		return err
	}
	return maibotcfg.ValidateModelConfig(file, nil)
}

// validateModelConfigFile 校验磁盘上的 model_config 文件，错误文案对齐 Python
func validateModelConfigFile(path string) error {
	doc, err := readTOMLDocument(path)
	if err != nil {
		return fmt.Errorf("模型配置副本无效: %w", err)
	}
	if err := validateModelConfigMap(doc); err != nil {
		return fmt.Errorf("模型配置副本无效: %w", err)
	}
	return nil
}

// deepMergeConfig 递归合并字典，列表与标量直接替换
func deepMergeConfig(target, patch map[string]any) {
	for key, value := range patch {
		patchMap, patchOK := value.(map[string]any)
		if patchOK {
			if existing, ok := target[key].(map[string]any); ok {
				deepMergeConfig(existing, patchMap)
				continue
			}
		}
		target[key] = value
	}
}

// decodeConfigBody 解析 JSON 请求体为配置字典
func decodeConfigBody(r *http.Request) (map[string]any, error) {
	var body map[string]any
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil {
		return nil, err
	}
	return normalizeJSONNumbers(body).(map[string]any), nil
}

// normalizeJSONNumbers 将 json.Number 转为 int64/float64
func normalizeJSONNumbers(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			typed[key] = normalizeJSONNumbers(item)
		}
		return typed
	case []any:
		for index, item := range typed {
			typed[index] = normalizeJSONNumbers(item)
		}
		return typed
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		if float, err := typed.Float64(); err == nil {
			return float
		}
		return typed.String()
	default:
		return value
	}
}

var modelVersionIDPattern = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._-]*$")

func safeModelVersionID(versionID string) (string, error) {
	normalized := strings.TrimSpace(versionID)
	if normalized == "" || normalized == "." || normalized == ".." || normalized == "active" || !modelVersionIDPattern.MatchString(normalized) {
		return "", fmt.Errorf("无效的模型配置副本 ID")
	}
	return normalized, nil
}

func modelVersionPath(configDir, versionID string) (string, error) {
	normalized, err := safeModelVersionID(versionID)
	if err != nil {
		return "", err
	}
	dir := modelVersionsDir(configDir)
	path := filepath.Join(dir, normalized+".toml")
	if rel, err := filepath.Rel(dir, path); err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("模型配置副本路径越界")
	}
	return path, nil
}

type modelVersionManifest struct {
	ActiveLabel string           `json:"active_label"`
	Versions    []map[string]any `json:"versions"`
}

func readModelVersionManifest(configDir string) (*modelVersionManifest, error) {
	path := modelVersionManifestPath(configDir)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &modelVersionManifest{ActiveLabel: "默认配置"}, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest modelVersionManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("模型配置副本清单损坏: %s", path)
	}
	if strings.TrimSpace(manifest.ActiveLabel) == "" {
		manifest.ActiveLabel = "默认配置"
	}
	filtered := make([]map[string]any, 0, len(manifest.Versions))
	for _, item := range manifest.Versions {
		if item != nil {
			filtered = append(filtered, item)
		}
	}
	manifest.Versions = filtered
	return &manifest, nil
}

func writeModelVersionManifest(configDir string, manifest *modelVersionManifest) error {
	path := modelVersionManifestPath(configDir)
	if strings.TrimSpace(manifest.ActiveLabel) == "" {
		manifest.ActiveLabel = "默认配置"
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(raw, '\n'), 0o644)
}

func (m *modelVersionManifest) metadataByID() map[string]map[string]any {
	out := make(map[string]map[string]any, len(m.Versions))
	for _, item := range m.Versions {
		id, _ := item["id"].(string)
		if id != "" {
			out[id] = item
		}
	}
	return out
}

func (m *modelVersionManifest) upsert(versionID, label string, createdAt float64) {
	if strings.TrimSpace(label) == "" {
		label = defaultModelVersionLabel()
	}
	for _, item := range m.Versions {
		if item["id"] != versionID {
			continue
		}
		item["label"] = label
		if existing, ok := item["created_at"].(float64); !ok || existing == 0 {
			item["created_at"] = createdAt
		}
		return
	}
	m.Versions = append(m.Versions, map[string]any{
		"id": versionID, "label": label, "created_at": createdAt,
	})
}

func (m *modelVersionManifest) remove(versionID string) {
	filtered := m.Versions[:0]
	for _, item := range m.Versions {
		if item["id"] != versionID {
			filtered = append(filtered, item)
		}
	}
	m.Versions = filtered
}

func defaultModelVersionLabel() string {
	return "模型配置副本 " + time.Now().Format("2006-01-02 15:04:05")
}

func createModelVersionID(configDir string) string {
	base := "v" + time.Now().Format("20060102150405")
	versionID := base
	suffix := 2
	for {
		if _, err := os.Stat(filepath.Join(modelVersionsDir(configDir), versionID+".toml")); errors.Is(err, os.ErrNotExist) {
			return versionID
		}
		versionID = base + "-" + strconv.Itoa(suffix)
		suffix++
	}
}

// modelVersionInfo 对应 Python ModelConfigVersionInfo
type modelVersionInfo struct {
	ID                 string  `json:"id"`
	Label              string  `json:"label"`
	CreatedAt          float64 `json:"created_at"`
	ModifiedAt         float64 `json:"modified_at"`
	Size               int64   `json:"size"`
	Active             bool    `json:"active"`
	InnerConfigVersion *string `json:"inner_config_version"`
	Valid              bool    `json:"valid"`
	Error              *string `json:"error"`
}

func readModelConfigInnerVersion(path string) *string {
	doc, err := readTOMLDocument(path)
	if err != nil {
		return nil
	}
	inner, ok := doc["inner"].(map[string]any)
	if !ok {
		return nil
	}
	version, ok := inner["version"].(string)
	if !ok {
		return nil
	}
	return &version
}

func buildModelVersionInfo(versionID, label, path string, createdAt *float64, active bool) (*modelVersionInfo, error) {
	stat, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("模型配置副本文件不存在")
		}
		return nil, err
	}
	created := float64(stat.ModTime().UnixNano()) / 1e9
	if createdAt != nil && *createdAt > 0 {
		created = *createdAt
	}
	info := &modelVersionInfo{
		ID: versionID, Label: label, CreatedAt: created,
		ModifiedAt: float64(stat.ModTime().UnixNano()) / 1e9, Size: stat.Size(),
		Active: active, InnerConfigVersion: readModelConfigInnerVersion(path), Valid: true,
	}
	if err := validateModelConfigFile(path); err != nil {
		info.Valid = false
		message := err.Error()
		info.Error = &message
	}
	return info, nil
}

func listModelVersions(configDir string) ([]*modelVersionInfo, error) {
	manifest, err := readModelVersionManifest(configDir)
	if err != nil {
		return nil, err
	}
	metadata := manifest.metadataByID()
	entries, err := os.ReadDir(modelVersionsDir(configDir))
	if errors.Is(err, os.ErrNotExist) {
		return []*modelVersionInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	type candidate struct {
		path string
		mod  time.Time
	}
	candidates := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{filepath.Join(modelVersionsDir(configDir), entry.Name()), info.ModTime()})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].mod.After(candidates[j].mod) })
	out := make([]*modelVersionInfo, 0, len(candidates))
	for _, item := range candidates {
		versionID := strings.TrimSuffix(filepath.Base(item.path), ".toml")
		label := versionID
		var created *float64
		if meta, ok := metadata[versionID]; ok {
			if text, ok := meta["label"].(string); ok && text != "" {
				label = text
			}
			if value, ok := meta["created_at"].(float64); ok {
				created = &value
			}
		}
		info, err := buildModelVersionInfo(versionID, label, item.path, created, false)
		if err != nil {
			continue
		}
		out = append(out, info)
	}
	return out, nil
}

func copyModelConfigToVersion(configDir, sourcePath, label string) (*modelVersionInfo, error) {
	if _, err := os.Stat(sourcePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("当前模型配置文件不存在")
		}
		return nil, err
	}
	if err := validateModelConfigFile(sourcePath); err != nil {
		return nil, err
	}
	versionID := createModelVersionID(configDir)
	versionPath, err := modelVersionPath(configDir, versionID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(versionPath), 0o755); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(versionPath, raw, 0o644); err != nil {
		return nil, err
	}
	manifest, err := readModelVersionManifest(configDir)
	if err != nil {
		return nil, err
	}
	createdAt := float64(time.Now().UnixNano()) / 1e9
	manifest.upsert(versionID, label, createdAt)
	if err := writeModelVersionManifest(configDir, manifest); err != nil {
		return nil, err
	}
	displayLabel := strings.TrimSpace(label)
	if displayLabel == "" {
		displayLabel = defaultModelVersionLabel()
	}
	return buildModelVersionInfo(versionID, displayLabel, versionPath, &createdAt, false)
}

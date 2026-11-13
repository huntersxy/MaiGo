package webuiapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"maibot-go/internal/prompt"

	"github.com/pelletier/go-toml/v2"
)

const legacyCustomPromptVersionID = "legacy-current"

var promptVersionIDPattern = regexp.MustCompile("^[A-Za-z0-9_.-]+$")

// promptsRoot 返回实例内置 Prompt 资源目录（对齐 MaiBot resource_path("prompts")）
func (d Deps) promptsRoot() (string, bool) {
	dir := strings.TrimSpace(d.InstanceDir)
	if dir == "" {
		return "", false
	}
	return filepath.Join(filepath.Clean(dir), "prompts"), true
}

// promptResource 返回内置 Prompt 只读资源树
//
// 部署目录带 prompts/ 时按 resource_path 语义读磁盘，否则回退到编译期内嵌模板；
// 实例目录缺失时仍能列出与编辑默认 Prompt，与 Python 的包内资源一致。
func (d Deps) promptResource() fs.FS {
	if root, ok := d.promptsRoot(); ok && pathExists(root) {
		return os.DirFS(root)
	}
	return prompt.EmbeddedFiles()
}

// customPromptsRoot 返回用户覆盖目录 data/custom_prompts
func (d Deps) customPromptsRoot() (string, bool) {
	dir := strings.TrimSpace(d.InstanceDir)
	if dir == "" {
		return "", false
	}
	return filepath.Join(filepath.Clean(dir), "data", "custom_prompts"), true
}

func safePromptSegment(language, filename string) error {
	normalizedLanguage := strings.TrimSpace(language)
	normalizedFilename := strings.TrimSpace(filename)
	if normalizedLanguage == "" || strings.ContainsAny(normalizedLanguage, "/\\") || strings.Contains(normalizedLanguage, "..") {
		return fmt.Errorf("无效的 Prompt 语言目录")
	}
	if !strings.HasSuffix(normalizedFilename, ".prompt") || strings.ContainsAny(normalizedFilename, "/\\") || strings.Contains(normalizedFilename, "..") {
		return fmt.Errorf("无效的 Prompt 文件名")
	}
	return nil
}

func safePromptPath(root, language, filename string) (string, error) {
	if err := safePromptSegment(language, filename); err != nil {
		return "", err
	}
	resolvedRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	path := filepath.Join(resolvedRoot, strings.TrimSpace(language), strings.TrimSpace(filename))
	rel, err := filepath.Rel(resolvedRoot, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("Prompt 路径越界")
	}
	return path, nil
}

func safePromptVersionID(versionID string) (string, error) {
	normalized := strings.TrimSpace(versionID)
	if normalized == "" || normalized == "." || normalized == ".." || !promptVersionIDPattern.MatchString(normalized) {
		return "", fmt.Errorf("无效的 Prompt 版本 ID")
	}
	return normalized, nil
}

func promptVersionsDir(customRoot, language, filename string) (string, error) {
	customPath, err := safePromptPath(customRoot, language, filename)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(filepath.Dir(customPath), ".versions", strings.TrimSuffix(filepath.Base(customPath), ".prompt"))
	resolvedRoot, absErr := filepath.Abs(customRoot)
	if absErr != nil {
		return "", absErr
	}
	rel, relErr := filepath.Rel(resolvedRoot, dir)
	if relErr != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("Prompt 版本路径越界")
	}
	return dir, nil
}

func promptVersionFilePath(customRoot, language, filename, versionID string) (string, error) {
	normalized, err := safePromptVersionID(versionID)
	if err != nil {
		return "", err
	}
	dir, err := promptVersionsDir(customRoot, language, filename)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, normalized+".prompt"), nil
}

type promptVersionManifest struct {
	ActiveVersionID *string          `json:"active_version_id"`
	Versions        []map[string]any `json:"versions"`
}

func readPromptVersionManifest(customRoot, language, filename string) (*promptVersionManifest, error) {
	dir, err := promptVersionsDir(customRoot, language, filename)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if errors.Is(err, os.ErrNotExist) {
		return &promptVersionManifest{}, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest promptVersionManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("Prompt 版本清单损坏: %s", filepath.Join(dir, "manifest.json"))
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

func writePromptVersionManifest(customRoot, language, filename string, manifest *promptVersionManifest) error {
	dir, err := promptVersionsDir(customRoot, language, filename)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, "manifest.json"), append(raw, '\n'), 0o644)
}

type promptVersionInfo struct {
	ID         string  `json:"id"`
	Label      string  `json:"label"`
	CreatedAt  float64 `json:"created_at"`
	ModifiedAt float64 `json:"modified_at"`
	Size       int64   `json:"size"`
	Active     bool    `json:"active"`
}

func listPromptVersions(customRoot, language, filename string) ([]promptVersionInfo, error) {
	manifest, err := readPromptVersionManifest(customRoot, language, filename)
	if err != nil {
		return nil, err
	}
	versions := make([]promptVersionInfo, 0, len(manifest.Versions))
	activeID := ""
	if manifest.ActiveVersionID != nil {
		activeID = *manifest.ActiveVersionID
	}
	for _, item := range manifest.Versions {
		versionID, _ := item["id"].(string)
		if versionID == "" {
			continue
		}
		versionPath, err := promptVersionFilePath(customRoot, language, filename, versionID)
		if err != nil {
			continue
		}
		stat, err := os.Stat(versionPath)
		if err != nil {
			continue
		}
		label := versionID
		if text, ok := item["label"].(string); ok {
			label = text
		}
		created := float64(stat.ModTime().UnixNano()) / 1e9
		if value, ok := item["created_at"].(float64); ok && value > 0 {
			created = value
		}
		versions = append(versions, promptVersionInfo{
			ID: versionID, Label: label, CreatedAt: created,
			ModifiedAt: float64(stat.ModTime().UnixNano()) / 1e9,
			Size:       stat.Size(), Active: versionID == activeID,
		})
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].ModifiedAt > versions[j].ModifiedAt })
	return versions, nil
}

func activePromptVersionID(customRoot, language, filename string) (string, error) {
	manifest, err := readPromptVersionManifest(customRoot, language, filename)
	if err != nil {
		return "", err
	}
	if manifest.ActiveVersionID != nil && *manifest.ActiveVersionID != "" {
		return *manifest.ActiveVersionID, nil
	}
	customPath, err := safePromptPath(customRoot, language, filename)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(customPath); err == nil {
		return legacyCustomPromptVersionID, nil
	}
	return "", nil
}

func setActivePromptVersion(customRoot, language, filename, versionID string) error {
	manifest, err := readPromptVersionManifest(customRoot, language, filename)
	if err != nil {
		return err
	}
	if versionID == "" {
		manifest.ActiveVersionID = nil
	} else {
		manifest.ActiveVersionID = &versionID
	}
	return writePromptVersionManifest(customRoot, language, filename, manifest)
}

func savePromptVersion(customRoot, language, filename, content, versionID, label string, createVersion bool) (string, error) {
	manifest, err := readPromptVersionManifest(customRoot, language, filename)
	if err != nil {
		return "", err
	}
	existing := make(map[string]bool, len(manifest.Versions))
	for _, item := range manifest.Versions {
		if id, ok := item["id"].(string); ok {
			existing[id] = true
		}
	}
	normalized := strings.TrimSpace(versionID)
	if normalized != "" {
		if _, err := safePromptVersionID(normalized); err != nil {
			return "", err
		}
	}
	if normalized != "" && !createVersion && normalized != legacyCustomPromptVersionID && !existing[normalized] {
		return "", fmt.Errorf("Prompt 自定义版本不存在")
	}
	shouldCreate := createVersion || normalized == "" || normalized == legacyCustomPromptVersionID
	if shouldCreate {
		normalized = createPromptVersionID(customRoot, language, filename)
		manifest.Versions = append(manifest.Versions, map[string]any{
			"id": normalized, "label": normalizePromptLabel(filename, label), "created_at": float64(time.Now().UnixNano()) / 1e9,
		})
	}
	versionPath, err := promptVersionFilePath(customRoot, language, filename, normalized)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(versionPath), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(versionPath, []byte(content), 0o644); err != nil {
		return "", err
	}
	for _, item := range manifest.Versions {
		if item["id"] != normalized {
			continue
		}
		existingLabel, _ := item["label"].(string)
		item["label"] = normalizePromptLabel(filename, fallbackString(label, existingLabel))
		item["modified_at"] = float64(time.Now().UnixNano()) / 1e9
		break
	}
	manifest.ActiveVersionID = &normalized
	return normalized, writePromptVersionManifest(customRoot, language, filename, manifest)
}

func createPromptVersionID(customRoot, language, filename string) string {
	base := "v" + time.Now().Format("20060102150405")
	versionID := base
	suffix := 2
	for {
		path, err := promptVersionFilePath(customRoot, language, filename, versionID)
		if err != nil || !pathExists(path) {
			return versionID
		}
		versionID = base + "-" + itoa(suffix)
		suffix++
	}
}

func normalizePromptLabel(filename, label string) string {
	normalized := strings.TrimSpace(label)
	if normalized != "" {
		return normalized
	}
	name := strings.TrimSuffix(filename, ".prompt")
	return name + " 自定义版本 " + time.Now().Format("2006-01-02 15:04:05")
}

type promptValidationResult struct {
	Valid               bool     `json:"valid"`
	MissingPlaceholders []string `json:"missing_placeholders"`
	ExtraPlaceholders   []string `json:"extra_placeholders"`
	Message             string   `json:"message"`
}

// MarshalJSON 保证占位符列表输出空数组而不是 null，对齐 Python Field(default_factory=list)
func (v promptValidationResult) MarshalJSON() ([]byte, error) {
	missing := v.MissingPlaceholders
	if missing == nil {
		missing = []string{}
	}
	extra := v.ExtraPlaceholders
	if extra == nil {
		extra = []string{}
	}
	return json.Marshal(struct {
		Valid               bool     `json:"valid"`
		MissingPlaceholders []string `json:"missing_placeholders"`
		ExtraPlaceholders   []string `json:"extra_placeholders"`
		Message             string   `json:"message"`
	}{v.Valid, missing, extra, v.Message})
}

// extractPromptPlaceholders 对齐 Python string.Formatter().parse 的字段名提取
func extractPromptPlaceholders(template string) (map[string]bool, error) {
	out := make(map[string]bool)
	for index := 0; index < len(template); index++ {
		switch template[index] {
		case '{':
			if index+1 < len(template) && template[index+1] == '{' {
				index++
				continue
			}
			end := strings.IndexByte(template[index+1:], '}')
			if end < 0 {
				return nil, fmt.Errorf("expected '}' before end of string")
			}
			field := template[index+1 : index+1+end]
			if cut := strings.IndexAny(field, "!:"); cut >= 0 {
				field = field[:cut]
			}
			if field == "" {
				index += end + 1
				continue
			}
			if dot := strings.IndexByte(field, '.'); dot >= 0 {
				field = field[:dot]
			}
			if bracket := strings.IndexByte(field, '['); bracket >= 0 {
				field = field[:bracket]
			}
			out[field] = true
			index += end + 1
		case '}':
			if index+1 < len(template) && template[index+1] == '}' {
				index++
				continue
			}
			return nil, fmt.Errorf("single '}' encountered in format string")
		}
	}
	return out, nil
}

func buildPromptValidation(defaultContent, customContent string) promptValidationResult {
	defaultPlaceholders, err := extractPromptPlaceholders(defaultContent)
	if err != nil {
		return promptValidationResult{Valid: false, Message: "Prompt 参数格式错误: " + err.Error()}
	}
	customPlaceholders, err := extractPromptPlaceholders(customContent)
	if err != nil {
		return promptValidationResult{Valid: false, Message: "Prompt 参数格式错误: " + err.Error()}
	}
	missing := make([]string, 0)
	for name := range defaultPlaceholders {
		if !customPlaceholders[name] {
			missing = append(missing, name)
		}
	}
	extra := make([]string, 0)
	for name := range customPlaceholders {
		if !defaultPlaceholders[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) == 0 && len(extra) == 0 {
		return promptValidationResult{Valid: true}
	}
	parts := make([]string, 0, 2)
	if len(missing) > 0 {
		parts = append(parts, "缺少参数: "+strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		parts = append(parts, "多余参数: "+strings.Join(extra, ", "))
	}
	return promptValidationResult{
		Valid: false, MissingPlaceholders: missing, ExtraPlaceholders: extra,
		Message: "自定义 Prompt 参数必须与默认 Prompt 完全一致，" + strings.Join(parts, "；"),
	}
}

func ensurePromptParametersMatch(resource fs.FS, resourcePath, customContent string) (promptValidationResult, error) {
	raw, err := fs.ReadFile(resource, resourcePath)
	if err != nil {
		return promptValidationResult{}, err
	}
	validation := buildPromptValidation(string(raw), customContent)
	if !validation.Valid {
		return validation, fmt.Errorf("%s", validation.Message)
	}
	return validation, nil
}

type promptMetadata struct {
	DisplayName string
	Advanced    bool
	Description string
}

func readPromptMetadata(resource fs.FS, language string) map[string]promptMetadata {
	out := make(map[string]promptMetadata)
	raw, err := fs.ReadFile(resource, path.Join(language, ".meta.toml"))
	if err != nil {
		return out
	}
	var doc map[string]map[string]any
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return out
	}
	for name, entry := range doc {
		meta := promptMetadata{}
		if text, ok := entry["display_name"].(string); ok {
			meta.DisplayName = text
		}
		if value, ok := entry["advanced"].(bool); ok {
			meta.Advanced = value
		}
		if text, ok := entry["description"].(string); ok {
			meta.Description = text
		}
		out[name] = meta
	}
	return out
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fallbackString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func itoa(value int) string {
	return fmt.Sprintf("%d", value)
}

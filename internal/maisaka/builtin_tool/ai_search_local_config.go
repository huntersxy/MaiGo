package tools

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	maibotcfg "maibot-go/internal/config/maibot"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/pelletier/go-toml/v2"
)

// 本地配置读取对齐 MaiBot src/webui/services/ai_search_local_config.py
const (
	localConfigMaxReadCount = 8
	localConfigRedacted     = "<已隐藏>"
	botConfigFileName       = "bot_config.toml"
	modelConfigFileName     = "model_config.toml"
)

var localConfigSensitiveKeyWords = map[string]struct{}{
	"key": {}, "keys": {}, "token": {}, "secret": {}, "secrets": {},
	"password": {}, "passwd": {}, "credential": {}, "credentials": {},
	"authorization": {}, "headers": {}, "query": {}, "env": {}, "args": {},
}

var localConfigInternalFields = map[string]struct{}{
	"field_docs": {}, "suppress_any_warning": {},
}

var botConfigRootSet, modelConfigRootSet map[string]struct{}
var allConfigRootNames []string

func init() {
	botConfigRootSet = make(map[string]struct{})
	for _, name := range tomlConfigRootNames(reflect.TypeOf(maibotcfg.BotConfigFile{})) {
		botConfigRootSet[name] = struct{}{}
	}
	modelConfigRootSet = make(map[string]struct{})
	for _, name := range tomlConfigRootNames(reflect.TypeOf(maibotcfg.ModelConfigFile{})) {
		modelConfigRootSet[name] = struct{}{}
	}
	merged := make(map[string]struct{}, len(botConfigRootSet)+len(modelConfigRootSet))
	for name := range botConfigRootSet {
		merged[name] = struct{}{}
	}
	for name := range modelConfigRootSet {
		merged[name] = struct{}{}
	}
	allConfigRootNames = make([]string, 0, len(merged))
	for name := range merged {
		allConfigRootNames = append(allConfigRootNames, name)
	}
	sort.Strings(allConfigRootNames)
}

// LocalConfigReader 从实例 config 目录读取 bot_config.toml 与 model_config.toml
type LocalConfigReader struct {
	instanceDir func() string
	readFile    func(string) ([]byte, error)
}

// NewLocalConfigReader 构造固定实例目录的配置读取器
func NewLocalConfigReader(instanceDir string) *LocalConfigReader {
	dir := strings.TrimSpace(instanceDir)
	return &LocalConfigReader{
		instanceDir: func() string { return dir },
		readFile:    os.ReadFile,
	}
}

// NewDefaultLocalConfigReader 按 MAIBOT_INSTANCE_DIR、当前工作目录顺序解析实例目录
func NewDefaultLocalConfigReader() *LocalConfigReader {
	return &LocalConfigReader{
		instanceDir: func() string {
			if dir := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR")); dir != "" {
				return dir
			}
			dir, err := os.Getwd()
			if err != nil {
				return ""
			}
			return dir
		},
		readFile: os.ReadFile,
	}
}

// Read 读取两份额外配置并解析请求的路径；文件缺失或 TOML 损坏时返回真实错误
func (r *LocalConfigReader) Read(paths []string) ([]map[string]any, error) {
	if r == nil {
		return nil, fmt.Errorf("本地配置读取器未初始化")
	}
	dir := ""
	if r.instanceDir != nil {
		dir = strings.TrimSpace(r.instanceDir())
	}
	if dir == "" {
		return nil, fmt.Errorf("需要设置 MAIBOT_INSTANCE_DIR 指向 MaiBot 实例根目录")
	}
	readFile := r.readFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	configDir := filepath.Join(filepath.Clean(dir), "config")
	bot, err := readLocalConfigFile(readFile, filepath.Join(configDir, botConfigFileName), botConfigFileName)
	if err != nil {
		return nil, err
	}
	model, err := readLocalConfigFile(readFile, filepath.Join(configDir, modelConfigFileName), modelConfigFileName)
	if err != nil {
		return nil, err
	}
	return ReadLocalConfigSnapshot(paths, bot, model), nil
}

func readLocalConfigFile(readFile func(string) ([]byte, error), path, name string) (map[string]any, error) {
	raw, err := readFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", name, err)
	}
	var document map[string]any
	if err := toml.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", name, err)
	}
	if document == nil {
		document = map[string]any{}
	}
	return document, nil
}

// ReadLocalConfigSnapshot 在已加载的 TOML 快照上执行多路径读取、脱敏与节头生成
func ReadLocalConfigSnapshot(paths []string, bot, model map[string]any) []map[string]any {
	normalized := normalizeLocalConfigPaths(paths)
	documents := make([]map[string]any, 0, len(normalized))
	for _, path := range normalized {
		documents = append(documents, readLocalConfigPath(path, bot, model))
	}
	return documents
}

func normalizeLocalConfigPaths(paths []string) []string {
	normalized := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, rawPath := range paths {
		path := strings.Trim(strings.TrimSpace(rawPath), ".")
		if path == "" {
			continue
		}
		if _, duplicated := seen[path]; duplicated {
			continue
		}
		seen[path] = struct{}{}
		normalized = append(normalized, path)
	}
	if len(normalized) > localConfigMaxReadCount {
		normalized = normalized[:localConfigMaxReadCount]
	}
	return normalized
}

func readLocalConfigPath(path string, bot, model map[string]any) map[string]any {
	segments := strings.Split(path, ".")
	root := segments[0]
	source := ""
	var document map[string]any
	if _, ok := botConfigRootSet[root]; ok {
		source = botConfigFileName
		document = bot
	} else if _, ok := modelConfigRootSet[root]; ok {
		source = modelConfigFileName
		document = model
	} else {
		return map[string]any{
			"title":     path,
			"error":     "配置路径不存在",
			"available": append([]string(nil), allConfigRootNames...),
		}
	}

	value := redactLocalConfigValue(root, document[root])
	for depth := 1; depth < len(segments); depth++ {
		if text, ok := value.(string); ok && text == localConfigRedacted {
			break
		}
		segment := segments[depth]
		if mapping, ok := value.(map[string]any); ok {
			next, exists := mapping[segment]
			if !exists {
				return localConfigPathError(path, source, strings.Join(segments[:depth+1], "."), sortedLocalConfigKeys(mapping))
			}
			value = next
			continue
		}
		if next, ok := localConfigSliceIndex(value, segment); ok {
			value = next
			continue
		}
		return localConfigPathError(path, source, strings.Join(segments[:depth+1], "."), []string{})
	}

	sectionSegments := segments[:len(segments)-1]
	if _, ok := value.(map[string]any); ok {
		sectionSegments = segments
	}
	result := map[string]any{
		"title":  path,
		"source": source,
		"value":  value,
	}
	if len(sectionSegments) > 0 {
		result["section"] = buildLocalConfigSectionHeader(sectionSegments)
	}
	return result
}

func localConfigPathError(path, source, missingPath string, available []string) map[string]any {
	if available == nil {
		available = []string{}
	}
	return map[string]any{
		"title":     path,
		"source":    source,
		"error":     "配置路径不存在: " + missingPath,
		"available": available,
	}
}

func sortedLocalConfigKeys(mapping map[string]any) []string {
	keys := make([]string, 0, len(mapping))
	for key := range mapping {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func localConfigSliceIndex(value any, segment string) (any, bool) {
	if !isASCIIDigits(segment) {
		return nil, false
	}
	index, err := strconv.Atoi(segment)
	if err != nil {
		return nil, false
	}
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() || reflected.Kind() != reflect.Slice {
		return nil, false
	}
	if index < 0 || index >= reflected.Len() {
		return nil, false
	}
	return reflected.Index(index).Interface(), true
}

func isASCIIDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func buildLocalConfigSectionHeader(segments []string) string {
	tableSegments := make([]string, 0, len(segments))
	for _, segment := range segments {
		if isASCIIDigits(segment) {
			continue
		}
		tableSegments = append(tableSegments, segment)
	}
	path := strings.Join(tableSegments, ".")
	if len(tableSegments) != len(segments) {
		return "[[" + path + "]]"
	}
	return "[" + path + "]"
}

func redactLocalConfigValue(key string, value any) any {
	keyWords := splitLocalConfigKeyWords(key)
	if containsAnyLocalConfigKeyWord(keyWords, localConfigSensitiveKeyWords) {
		if isTruthyLocalConfigValue(value) {
			return localConfigRedacted
		}
		return value
	}
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for itemKey, itemValue := range typed {
			if _, internal := localConfigInternalFields[itemKey]; internal {
				continue
			}
			result[itemKey] = redactLocalConfigValue(itemKey, itemValue)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = redactLocalConfigValue(key, item)
		}
		return result
	case []map[string]any:
		result := make([]map[string]any, len(typed))
		for index, item := range typed {
			redacted, _ := redactLocalConfigValue(key, item).(map[string]any)
			result[index] = redacted
		}
		return result
	case []string:
		result := make([]string, len(typed))
		for index, item := range typed {
			if redacted, ok := redactLocalConfigValue(key, item).(string); ok {
				result[index] = redacted
			}
		}
		return result
	case string:
		if containsLocalConfigKeyWord(keyWords, "url") {
			return stripLocalConfigURLCredentials(typed)
		}
		return typed
	default:
		return value
	}
}

func splitLocalConfigKeyWords(key string) []string {
	return strings.Split(strings.ReplaceAll(strings.ToLower(key), "-", "_"), "_")
}

func containsAnyLocalConfigKeyWord(words []string, candidates map[string]struct{}) bool {
	for _, word := range words {
		if _, ok := candidates[word]; ok {
			return true
		}
	}
	return false
}

func containsLocalConfigKeyWord(words []string, target string) bool {
	for _, word := range words {
		if word == target {
			return true
		}
	}
	return false
}

func isTruthyLocalConfigValue(value any) bool {
	if value == nil {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed != ""
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case uint64:
		return typed != 0
	case float64:
		return typed != 0
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		return reflected.Len() > 0
	}
	return true
}

// stripLocalConfigURLCredentials 去掉 URL 中可能携带的用户信息、查询参数和片段
func stripLocalConfigURLCredentials(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return value
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String()
}

func tomlConfigRootNames(value reflect.Type) []string {
	for value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	names := make([]string, 0, value.NumField())
	for index := 0; index < value.NumField(); index++ {
		field := value.Field(index)
		tag := field.Tag.Get("toml")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" {
			continue
		}
		names = append(names, name)
	}
	return names
}

// ReadLocalConfigInput 是 read_local_config 的工具入参
type ReadLocalConfigInput struct {
	Paths []string `json:"paths" jsonschema:"description=点号分隔的配置路径，可以是配置节或具体字段，列表元素用下标表示，例如 chat.reply_timing、emoji.steal_emoji、api_providers.0，最多 8 个"`
}

// ReadLocalConfigOutput 是 read_local_config 的工具结果
type ReadLocalConfigOutput struct {
	Documents []map[string]any `json:"documents"`
}

// NewReadLocalConfigTool 构造 read_local_config 工具；reader 为 nil 时读取 MAIBOT_INSTANCE_DIR 下的配置
func NewReadLocalConfigTool(reader *LocalConfigReader) (tool.InvokableTool, error) {
	if reader == nil {
		reader = NewDefaultLocalConfigReader()
	}
	return utils.InferTool(
		"read_local_config",
		"读取本机 bot_config.toml 与 model_config.toml 中配置节或字段当前实际生效的值，用于核对用户现在的设置。密钥、令牌等敏感内容会被隐藏。",
		func(_ context.Context, input *ReadLocalConfigInput) (*ReadLocalConfigOutput, error) {
			if input == nil {
				return &ReadLocalConfigOutput{Documents: []map[string]any{}}, nil
			}
			documents, err := reader.Read(input.Paths)
			if err != nil {
				return nil, fmt.Errorf("读取本地配置失败: %w", err)
			}
			if documents == nil {
				documents = []map[string]any{}
			}
			return &ReadLocalConfigOutput{Documents: documents}, nil
		},
	)
}

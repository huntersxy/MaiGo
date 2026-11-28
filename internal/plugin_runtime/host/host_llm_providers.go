package host

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"MaiGo/internal/llm_models"
	"MaiGo/internal/plugin_runtime/protocol"
	providerformats "MaiGo/internal/plugin_runtime/provider_formats"
)

// LLMProviderEntry Host 侧插件 LLM Provider 注册项，对齐 ClientProviderRegistration 的可观察字段
type LLMProviderEntry struct {
	ClientType  string                          `json:"client_type"`
	Name        string                          `json:"name"`
	Description string                          `json:"description"`
	Version     string                          `json:"version"`
	Metadata    map[string]any                  `json:"metadata"`
	PluginID    string                          `json:"plugin_id"`
	Formats     providerformats.ProviderFormats `json:"formats"`
}

// LLMProviderRegistry 插件 LLM Provider 注册表，对齐 llm_models.model_client.base_client 的插件分区
type LLMProviderRegistry struct {
	mu          sync.RWMutex
	providers   map[string]LLMProviderEntry
	byPlugin    map[string]map[string]struct{}
	hostLLM     bool
	hostInvoker *Host
}

// NewLLMProviderRegistry 构造空注册表
func NewLLMProviderRegistry() *LLMProviderRegistry {
	return &LLMProviderRegistry{
		providers: make(map[string]LLMProviderEntry),
		byPlugin:  make(map[string]map[string]struct{}),
		hostLLM:   true,
	}
}

// ValidatePluginProviders 校验插件 Provider 声明，不修改注册表
//
// 对齐 validate_plugin_provider_replacement：client_type 去空白后不能为空、同一插件不能重复、
// 不能与其他插件或内置客户端冲突
func (r *LLMProviderRegistry) ValidatePluginProviders(pluginID string, declarations []protocol.LLMProviderDeclaration) error {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return fmt.Errorf("plugin_id 不能为空")
	}
	normalized := make([]string, 0, len(declarations))
	for _, declaration := range declarations {
		clientType := strings.TrimSpace(declaration.ClientType)
		if clientType == "" {
			return fmt.Errorf("client_type 不能为空")
		}
		normalized = append(normalized, clientType)
	}
	counts := make(map[string]int, len(normalized))
	for _, clientType := range normalized {
		counts[clientType]++
	}
	duplicates := make([]string, 0)
	for clientType, count := range counts {
		if count > 1 {
			duplicates = append(duplicates, clientType)
		}
	}
	if len(duplicates) > 0 {
		sort.Strings(duplicates)
		return fmt.Errorf("插件 %s 重复声明 LLM Provider: %s", pluginID, strings.Join(duplicates, ", "))
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, clientType := range normalized {
		if isBuiltinLLMClientType(clientType) {
			return fmt.Errorf("LLM Provider client_type 冲突: %s 已由 host 注册", clientType)
		}
		existing, ok := r.providers[clientType]
		if !ok || existing.PluginID == pluginID {
			continue
		}
		return fmt.Errorf("LLM Provider client_type 冲突: %s 已由 %s 注册", clientType, existing.PluginID)
	}
	return nil
}

// ReplacePluginProviders 原子替换一个插件拥有的全部 Provider
func (r *LLMProviderRegistry) ReplacePluginProviders(pluginID string, declarations []protocol.LLMProviderDeclaration) error {
	if err := r.ValidatePluginProviders(pluginID, declarations); err != nil {
		return err
	}
	pluginID = strings.TrimSpace(pluginID)
	entries := make([]LLMProviderEntry, 0, len(declarations))
	for _, declaration := range declarations {
		version := strings.TrimSpace(declaration.Version)
		if version == "" {
			version = "1.0.0"
		}
		metadata := map[string]any{}
		for key, value := range declaration.Metadata {
			metadata[key] = value
		}
		entries = append(entries, LLMProviderEntry{
			ClientType:  strings.TrimSpace(declaration.ClientType),
			Name:        declaration.Name,
			Description: declaration.Description,
			Version:     version,
			Metadata:    metadata,
			PluginID:    pluginID,
			Formats:     decodeProviderFormats(declaration.Formats),
		})
	}
	r.mu.Lock()
	previous := r.byPlugin[pluginID]
	next := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		r.providers[entry.ClientType] = entry
		next[entry.ClientType] = struct{}{}
	}
	for clientType := range previous {
		if _, keep := next[clientType]; keep {
			continue
		}
		delete(r.providers, clientType)
	}
	r.byPlugin[pluginID] = next
	r.mu.Unlock()
	r.syncRuntimeProviders(pluginID, entries, previous, next)
	return nil
}

// RemovePluginProviders 注销插件全部 Provider，返回移除数量
func (r *LLMProviderRegistry) RemovePluginProviders(pluginID string) int {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return 0
	}
	r.mu.Lock()
	previous := r.byPlugin[pluginID]
	delete(r.byPlugin, pluginID)
	for clientType := range previous {
		delete(r.providers, clientType)
	}
	r.mu.Unlock()
	r.syncRuntimeProviders(pluginID, nil, previous, nil)
	return len(previous)
}

// CountPlugin 返回插件已注册的 Provider 数
func (r *LLMProviderRegistry) CountPlugin(pluginID string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byPlugin[strings.TrimSpace(pluginID)])
}

// Count 返回全部插件 Provider 数
func (r *LLMProviderRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.providers)
}

// Get 按 client_type 查询插件 Provider
func (r *LLMProviderRegistry) Get(clientType string) (LLMProviderEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.providers[strings.TrimSpace(clientType)]
	return entry, ok
}

// List 返回按 client_type 排序的全部插件 Provider
func (r *LLMProviderRegistry) List() []LLMProviderEntry {
	r.mu.RLock()
	out := make([]LLMProviderEntry, 0, len(r.providers))
	for _, entry := range r.providers {
		out = append(out, entry)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ClientType < out[j].ClientType })
	return out
}

// syncRuntimeProviders 同步 internal/llm 的插件 Provider 解析表
func (r *LLMProviderRegistry) syncRuntimeProviders(pluginID string, entries []LLMProviderEntry, previous, next map[string]struct{}) {
	if !r.hostLLM {
		return
	}
	for clientType := range previous {
		if _, keep := next[clientType]; keep {
			continue
		}
		llm.UnregisterPluginProvider(clientType)
	}
	if r.hostInvoker == nil || len(entries) == 0 {
		return
	}
	invoker := hostPluginLLMInvoker{host: r.hostInvoker}
	for _, entry := range entries {
		if err := llm.RegisterPluginProvider(entry.ClientType, pluginID, invoker); err != nil {
			continue
		}
		llm.RegisterPluginProviderFormats(entry.ClientType, entry.Formats)
	}
}

func decodeProviderFormats(raw map[string]any) providerformats.ProviderFormats {
	if len(raw) == 0 {
		return providerformats.ProviderFormats{}
	}
	formats := providerformats.ProviderFormats{
		ASR: map[string]providerformats.SpeechPreset{},
		TTS: map[string]providerformats.SpeechPreset{},
	}
	if asrRaw, ok := raw["asr"].(map[string]any); ok {
		for key, value := range asrRaw {
			if presetMap, ok := value.(map[string]any); ok {
				formats.ASR[key] = decodeSpeechPreset(presetMap)
			}
		}
	}
	if ttsRaw, ok := raw["tts"].(map[string]any); ok {
		for key, value := range ttsRaw {
			if presetMap, ok := value.(map[string]any); ok {
				formats.TTS[key] = decodeSpeechPreset(presetMap)
			}
		}
	}
	if webRaw, ok := raw["web"].([]any); ok {
		for _, item := range webRaw {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				formats.Web = append(formats.Web, text)
			}
		}
	}
	return formats
}

func decodeSpeechPreset(raw map[string]any) providerformats.SpeechPreset {
	preset := providerformats.SpeechPreset{Voices: map[string]string{}}
	if label, ok := raw["label"].(string); ok {
		preset.Label = strings.TrimSpace(label)
	}
	if voices, ok := raw["voices"].(map[string]any); ok {
		for key, value := range voices {
			preset.Voices[key] = strings.TrimSpace(fmt.Sprint(value))
		}
	}
	return preset
}

// hostPluginLLMInvoker 把 internal/llm 的 Provider 调用桥接回 Runner RPC
type hostPluginLLMInvoker struct {
	host *Host
}

func (i hostPluginLLMInvoker) InvokeLLMProvider(ctx context.Context, pluginID, clientType, operation string, request map[string]any, timeoutMS int64) (llm.PluginLLMResult, error) {
	env, err := i.host.InvokeLLMProvider(ctx, pluginID, clientType, operation, request, timeoutMS)
	if err != nil {
		return llm.PluginLLMResult{}, err
	}
	return llm.PluginLLMResult{Payload: env.Payload, Error: env.Error}, nil
}

func isBuiltinLLMClientType(clientType string) bool {
	switch strings.ToLower(strings.TrimSpace(clientType)) {
	case "openai", "openai_responses", "gemini":
		return true
	default:
		return false
	}
}

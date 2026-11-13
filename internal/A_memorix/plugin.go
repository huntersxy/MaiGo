package memorix

// A_memorix 插件入口：对齐 src/A_memorix/plugin.py 的生命周期与工具目录
// Python 侧 plugin.py 是 legacy 兼容入口，主线已经走 host_service
// Go 侧同样把插件实现为宿主服务门面，避免同时维护两套内核生命周期

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// AMemorixPlugin A_memorix 插件生命周期入口
type AMemorixPlugin struct {
	mu      sync.Mutex
	service *AMemorixHostService

	pluginConfig map[string]any
	registered   bool
}

// NewAMemorixPlugin 构造插件入口
func NewAMemorixPlugin(service *AMemorixHostService) *AMemorixPlugin {
	return &AMemorixPlugin{service: service}
}

// Service 返回插件持有的宿主服务
func (p *AMemorixPlugin) Service() *AMemorixHostService {
	if p == nil {
		return nil
	}
	return p.service
}

// SetPluginConfig 对齐 set_plugin_config：只保留下一次初始化使用的配置
// 说明：宿主服务从 HostConfigStore 读取配置，本字段用于兼容 legacy 调用与自检
func (p *AMemorixPlugin) SetPluginConfig(config map[string]any) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if config == nil {
		p.pluginConfig = map[string]any{}
		return
	}
	p.pluginConfig = deepCloneMap(normalizeConfigMap(config))
}

// PluginConfig 返回插件自身保存的配置副本
func (p *AMemorixPlugin) PluginConfig() map[string]any {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return deepCloneMap(p.pluginConfig)
}

// Register 把宿主服务注册为默认 Host
func (p *AMemorixPlugin) Register(context.Context) error {
	if p == nil || p.service == nil {
		return errors.New("A_Memorix 宿主服务未注入")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.registered {
		DefaultRegistry().SetHost(p.service)
		p.registered = true
	}
	return nil
}

// Start 对齐 host_service.start
func (p *AMemorixPlugin) Start(ctx context.Context) error {
	if p == nil || p.service == nil {
		return errors.New("A_Memorix 宿主服务未注入")
	}
	if err := p.Register(ctx); err != nil {
		return err
	}
	return p.service.Start(ctx)
}

// Stop 对齐 host_service.stop
func (p *AMemorixPlugin) Stop(ctx context.Context) error {
	if p == nil || p.service == nil {
		return errors.New("A_Memorix 宿主服务未注入")
	}
	return p.service.Stop(ctx)
}

// Reload 对齐 host_service.reload
func (p *AMemorixPlugin) Reload(ctx context.Context) error {
	if p == nil || p.service == nil {
		return errors.New("A_Memorix 宿主服务未注入")
	}
	return p.service.Reload(ctx)
}

// OnLoad 对齐 plugin.on_load
func (p *AMemorixPlugin) OnLoad(ctx context.Context) error {
	return p.Start(ctx)
}

// OnUnload 对齐 plugin.on_unload
func (p *AMemorixPlugin) OnUnload(ctx context.Context) error {
	return p.Stop(ctx)
}

// OnConfigUpdate 对齐 plugin.on_config_update
// scope=self 时更新插件配置并停机；scope=bot/model 时仅在已有内核时停机
func (p *AMemorixPlugin) OnConfigUpdate(ctx context.Context, scope string, config map[string]any) error {
	if p == nil || p.service == nil {
		return errors.New("A_Memorix 宿主服务未注入")
	}
	switch trimLower(scope) {
	case "self":
		p.SetPluginConfig(config)
		return p.service.Stop(ctx)
	case "bot", "model":
		if p.service.State() != hostStateStopped {
			return p.service.Stop(ctx)
		}
	}
	return nil
}

// OnConfigReload 对齐 host_service.on_config_reload
func (p *AMemorixPlugin) OnConfigReload(scopes []string) error {
	if p == nil || p.service == nil {
		return errors.New("A_Memorix 宿主服务未注入")
	}
	return p.service.OnConfigReload(scopes)
}

// Tools 返回插件暴露的全部工具，对齐 plugin.py 的 @Tool 列表
func (p *AMemorixPlugin) Tools() []HostToolSpec {
	return AMemorixToolSpecs()
}

// CallTool 按工具名分发到宿主服务，工具名与 Python 侧组件名一致
func (p *AMemorixPlugin) CallTool(ctx context.Context, name string, args map[string]any) (any, error) {
	if p == nil || p.service == nil {
		return nil, errors.New("A_Memorix 宿主服务未注入")
	}
	component := strings.TrimSpace(name)
	if !IsAMemorixTool(component) {
		return nil, fmt.Errorf("不支持的 A_Memorix 工具: %s", name)
	}
	return p.service.Invoke(ctx, component, args, nil)
}

// AMemorixToolSpecs 返回聊天工具 + admin 工具的完整目录
func AMemorixToolSpecs() []HostToolSpec {
	specs := AMemorixChatToolSpecs()
	specs = append(specs, AMemorixAdminToolSpecs()...)
	return specs
}

// AMemorixChatToolSpecs 返回 6 个聊天工具
func AMemorixChatToolSpecs() []HostToolSpec {
	return []HostToolSpec{
		{
			Name:        "search_memory",
			Description: "搜索长期记忆",
			Parameters: []HostToolParam{
				{Name: "query", Type: "STRING", Description: "查询文本"},
				{Name: "limit", Type: "INTEGER", Description: "返回条数"},
				{Name: "mode", Type: "STRING", Description: "检索模式：search/time/hybrid/episode/aggregate"},
				{Name: "chat_id", Type: "STRING", Description: "聊天流 ID"},
				{Name: "person_id", Type: "STRING", Description: "人物 ID"},
				{Name: "time_start", Type: "FLOAT", Description: "起始时间戳"},
				{Name: "time_end", Type: "FLOAT", Description: "结束时间戳"},
				{Name: "respect_filter", Type: "BOOLEAN", Description: "是否应用聊天过滤配置"},
			},
		},
		{
			Name:        "ingest_summary",
			Description: "写入聊天摘要到长期记忆",
			Parameters: []HostToolParam{
				{Name: "external_id", Type: "STRING", Description: "外部幂等 ID", Required: true},
				{Name: "chat_id", Type: "STRING", Description: "聊天流 ID", Required: true},
				{Name: "text", Type: "STRING", Description: "摘要文本", Required: true},
				{Name: "time_start", Type: "FLOAT", Description: "起始时间戳"},
				{Name: "time_end", Type: "FLOAT", Description: "结束时间戳"},
				{Name: "respect_filter", Type: "BOOLEAN", Description: "是否应用聊天过滤配置"},
			},
		},
		{
			Name:        "ingest_text",
			Description: "写入普通长期记忆文本",
			Parameters: []HostToolParam{
				{Name: "external_id", Type: "STRING", Description: "外部幂等 ID", Required: true},
				{Name: "source_type", Type: "STRING", Description: "来源类型", Required: true},
				{Name: "text", Type: "STRING", Description: "原始文本", Required: true},
				{Name: "chat_id", Type: "STRING", Description: "聊天流 ID"},
				{Name: "timestamp", Type: "FLOAT", Description: "时间戳"},
				{Name: "respect_filter", Type: "BOOLEAN", Description: "是否应用聊天过滤配置"},
			},
		},
		{
			Name:        "get_person_profile",
			Description: "获取人物画像",
			Parameters: []HostToolParam{
				{Name: "person_id", Type: "STRING", Description: "人物 ID", Required: true},
				{Name: "chat_id", Type: "STRING", Description: "聊天流 ID"},
				{Name: "limit", Type: "INTEGER", Description: "证据条数"},
			},
		},
		{
			Name:        "maintain_memory",
			Description: "维护长期记忆关系状态",
			Parameters: []HostToolParam{
				{Name: "action", Type: "STRING", Description: "维护动作：reinforce/protect/restore/freeze/recycle_bin", Required: true},
				{Name: "target", Type: "STRING", Description: "目标哈希或查询文本"},
				{Name: "hours", Type: "FLOAT", Description: "保护时长（小时）"},
				{Name: "limit", Type: "INTEGER", Description: "查询条数（用于 recycle_bin）"},
			},
		},
		{
			Name:        "memory_stats",
			Description: "获取长期记忆统计",
			Parameters:  []HostToolParam{},
		},
	}
}

type adminToolDefinition struct {
	name        string
	description string
}

// AMemorixAdminToolSpecs 返回 13 个 admin 工具
func AMemorixAdminToolSpecs() []HostToolSpec {
	definitions := []adminToolDefinition{
		{name: "memory_graph_admin", description: "长期记忆图谱管理接口"},
		{name: "memory_source_admin", description: "长期记忆来源管理接口"},
		{name: "memory_episode_admin", description: "Episode 管理接口"},
		{name: "memory_profile_admin", description: "人物画像管理接口"},
		{name: "memory_fact_admin", description: "结构化事实账本管理接口"},
		{name: "memory_runtime_admin", description: "长期记忆运行时管理接口"},
		{name: "memory_import_admin", description: "长期记忆导入管理接口"},
		{name: "memory_bundle_admin", description: "可分享记忆包管理接口"},
		{name: "memory_tuning_admin", description: "长期记忆调优管理接口"},
		{name: "memory_v5_admin", description: "长期记忆 V5 管理接口"},
		{name: "memory_delete_admin", description: "长期记忆删除管理接口"},
		{name: "memory_correction_admin", description: "长期记忆修正管理接口"},
		{name: "memory_fuzzy_modify_admin", description: "长期记忆修正管理兼容接口"},
	}
	specs := make([]HostToolSpec, 0, len(definitions))
	for _, definition := range definitions {
		specs = append(specs, HostToolSpec{
			Name:        definition.name,
			Description: definition.description,
			Parameters: []HostToolParam{
				{Name: "action", Type: "STRING", Description: "管理动作", Required: true},
				{Name: "target", Type: "STRING", Description: "可选目标标识"},
			},
		})
	}
	return specs
}

// IsAMemorixTool 判断工具名是否属于插件目录
func IsAMemorixTool(name string) bool {
	trimmed := strings.TrimSpace(name)
	for _, spec := range AMemorixToolSpecs() {
		if spec.Name == trimmed {
			return true
		}
	}
	return false
}

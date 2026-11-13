package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"

	"maibot-go/internal/core/tooling"
)

// ServerConfig MCP 服务器配置
type ServerConfig struct {
	Name               string              `json:"name"`
	Enabled            bool                `json:"enabled"`
	Type               string              `json:"type"` // stdio/sse/streamable_http
	URL                string              `json:"url"`  // SSE/Streamable HTTP 服务器 URL
	Command            string              `json:"command"`
	Args               []string            `json:"args"`
	Env                []string            `json:"env"`            // KEY=VALUE，覆盖进程环境
	ToolNameList       []string            `json:"tool_name_list"` // 可选，仅加载名单内工具
	CustomHeaders      map[string]string   `json:"custom_headers"` // 可选，自定义 HTTP 头
	HTTPTimeoutSeconds float64             `json:"http_timeout_seconds,omitempty"`
	ReadTimeoutSeconds float64             `json:"read_timeout_seconds,omitempty"`
	Authorization      AuthorizationConfig `json:"authorization,omitempty"`
}

// Manager MCP 客户端管理器
type Manager struct {
	mu sync.RWMutex

	clientCfg        ClientConfig
	host             HostCallbacks
	discoverExtended bool
	exposure         ToolExposureMode
	reserved         map[string]struct{}

	connections     map[string]*serverConnection
	connectionOrder []string
	servers         []ServerStatus
	serverConfigs   map[string]ServerConfig
	reconnecting    map[string]struct{}

	tools       []tool.BaseTool
	routes      []ToolRoute
	routeByName map[string]ToolRoute

	closed       bool
	statusChange func()
	refreshMu    sync.Mutex

	promptToServer   map[string]string
	resourceToServer map[string]string
	templateToServer map[string]string
}

// ManagerOption 配置 Manager 行为
type ManagerOption func(*Manager)

// WithClientConfig 设置客户端宿主能力配置
func WithClientConfig(cfg ClientConfig) ManagerOption {
	return func(m *Manager) { m.clientCfg = cfg.Normalize() }
}

// WithHostCallbacks 注入宿主回调（sampling/elicitation/logging/message）
func WithHostCallbacks(host HostCallbacks) ManagerOption {
	return func(m *Manager) { m.host = host }
}

// WithDiscoverExtendedFeatures 控制是否在连接时发现 Prompt 与 Resource
func WithDiscoverExtendedFeatures(enabled bool) ManagerOption {
	return func(m *Manager) { m.discoverExtended = enabled }
}

// WithToolExposure 设置工具暴露命名策略
func WithToolExposure(mode ToolExposureMode) ManagerOption {
	return func(m *Manager) {
		switch mode {
		case ToolExposureBare, ToolExposurePrefixed:
			m.exposure = mode
		}
	}
}

// WithReservedToolNames 追加内置工具名，避免 MCP 裸名抢占已有工具
func WithReservedToolNames(names ...string) ManagerOption {
	return func(m *Manager) {
		for _, name := range names {
			trimmed := strings.TrimSpace(name)
			if trimmed != "" {
				m.reserved[trimmed] = struct{}{}
			}
		}
	}
}

// NewMCPManager 创建 MCP 管理器
func NewMCPManager(options ...ManagerOption) *Manager {
	manager := &Manager{
		clientCfg:        DefaultClientConfig(),
		discoverExtended: true,
		exposure:         ToolExposureBare,
		reserved:         make(map[string]struct{}, len(maiBotBuiltinToolNames)),
		connections:      make(map[string]*serverConnection),
		serverConfigs:    make(map[string]ServerConfig),
		reconnecting:     make(map[string]struct{}),
		routeByName:      make(map[string]ToolRoute),
		promptToServer:   make(map[string]string),
		resourceToServer: make(map[string]string),
		templateToServer: make(map[string]string),
	}
	for _, name := range maiBotBuiltinToolNames {
		manager.reserved[name] = struct{}{}
	}
	for _, option := range options {
		if option != nil {
			option(manager)
		}
	}
	return manager
}

// DefaultClientConfig 返回 MaiBot 默认的客户端宿主能力配置
func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		ClientName:           DefaultClientName,
		ClientVersion:        DefaultClientVersion,
		SamplingTaskName:     DefaultSamplingTaskName,
		ElicitationAllowForm: true,
	}.Normalize()
}

// Load 连接指定的 MCP 服务器；单个失败会返回汇总错误，已成功连接的服务器仍保留
func (m *Manager) Load(ctx context.Context, servers []ServerConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadLocked(ctx, servers)
}

// Reconnect 关闭现有连接并按配置重建
func (m *Manager) Reconnect(ctx context.Context, servers []ServerConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeLocked()
	return m.loadLocked(ctx, servers)
}

type connectOutcome struct {
	cfg  ServerConfig
	conn *serverConnection
	err  error
}

func (m *Manager) loadLocked(ctx context.Context, servers []ServerConfig) error {
	if ctx == nil {
		ctx = context.Background()
	}
	enabled := make([]ServerConfig, 0, len(servers))
	for _, cfg := range servers {
		if !cfg.Enabled {
			zap.L().Debug("MCP 服务器已禁用，跳过", zap.String("name", cfg.Name))
			continue
		}
		enabled = append(enabled, cfg)
	}
	m.closed = false
	m.servers = make([]ServerStatus, len(enabled))
	m.serverConfigs = make(map[string]ServerConfig, len(enabled))
	for index, cfg := range enabled {
		m.servers[index] = ServerStatus{Name: cfg.Name, Transport: cfg.TransportType()}
		m.serverConfigs[cfg.Name] = cfg
	}
	if len(enabled) == 0 {
		return nil
	}

	// 并行建连，但按配置顺序注册，对齐 MaiBot MCPManager._connect_all
	outcomes := make([]connectOutcome, len(enabled))
	var wg sync.WaitGroup
	for index := range enabled {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			cfg := enabled[index]
			connectCtx, cancel := context.WithTimeout(ctx, connectTimeout(cfg))
			defer cancel()
			conn, err := connectServer(connectCtx, cfg, m.clientCfg, m.host, m.discoverExtended)
			if err != nil && errors.Is(connectCtx.Err(), context.DeadlineExceeded) {
				err = newError(ErrorKindTimeout, cfg.Name, "connect",
					fmt.Sprintf("连接超过 %g 秒", connectTimeout(cfg).Seconds()), err)
			}
			outcomes[index] = connectOutcome{cfg: cfg, conn: conn, err: err}
		}(index)
	}
	wg.Wait()

	var loadErrors []error
	for index, outcome := range outcomes {
		status := m.servers[index]
		if outcome.err != nil || outcome.conn == nil {
			status.Connected = false
			status.Error = errorMessage(outcome.err, "连接失败")
			m.servers[index] = status
			loadErrors = append(loadErrors, fmt.Errorf("加载 MCP 服务器 %q 失败: %w", outcome.cfg.Name, outcome.err))
			zap.L().Warn("连接 MCP 服务器失败",
				zap.String("name", outcome.cfg.Name),
				zap.Error(outcome.err))
			continue
		}
		conn := outcome.conn
		conn.watchConnectionLost(m.connectionLostFrom)
		conn.watchNotifications(m.handleNotification)
		m.connections[conn.cfg.Name] = conn
		m.connectionOrder = append(m.connectionOrder, conn.cfg.Name)
		registeredTools := m.registerTools(conn)
		registeredPrompts := m.registerPrompts(conn)
		registeredResources := m.registerResources(conn)
		registeredTemplates := m.registerTemplates(conn)
		status.Connected = true
		status.ProtocolVersion = conn.protocolVersion
		status.ToolCount = registeredTools
		status.PromptCount = registeredPrompts
		status.ResourceCount = registeredResources
		status.TemplateCount = registeredTemplates
		m.servers[index] = status
		zap.L().Info("已连接 MCP 服务器",
			zap.String("name", conn.cfg.Name),
			zap.String("transport", conn.cfg.TransportType()),
			zap.Int("tool_count", registeredTools),
			zap.Int("prompt_count", registeredPrompts),
			zap.Int("resource_count", registeredResources),
			zap.Int("template_count", registeredTemplates))
	}

	if len(m.connectionOrder) > 0 {
		if err := m.applyToolExposureLocked(); err != nil {
			closeErrs := m.closeLocked()
			if len(closeErrs) > 0 {
				zap.L().Warn("MCP 工具命名失败后关闭连接出错", zap.Errors("errors", closeErrs))
			}
			loadErrors = append(loadErrors, err)
		}
	}
	return errors.Join(loadErrors...)
}

// SetStatusChangeCallback 注册连接状态变化回调，用于 Service 刷新 WebUI 快照
func (m *Manager) SetStatusChangeCallback(callback func()) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.statusChange = callback
	m.mu.Unlock()
}

// connectionLost 处理传输层断连：刷新该服务器的 Connected/Error 并通知服务层
func (m *Manager) connectionLost(serverName string, err error) {
	m.connectionLostFrom(serverName, nil, err)
}

// connectionLostFrom 处理带连接指针的断连事件；旧连接的迟到事件不得覆盖新连接状态
func (m *Manager) connectionLostFrom(serverName string, lost *serverConnection, err error) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	if lost != nil && m.connections[serverName] != lost {
		m.mu.Unlock()
		return
	}
	for index := range m.servers {
		if m.servers[index].Name != serverName {
			continue
		}
		m.servers[index].Connected = false
		m.servers[index].Error = errorMessage(err, "MCP 连接已断开")
		break
	}
	// 断连后立即撤下已失效的工具/Prompt/Resource，避免 Agent 继续拿到死路由
	if rebuildErr := m.rebuildRegistryLocked(); rebuildErr != nil {
		zap.L().Warn("MCP 断连后刷新能力注册表失败",
			zap.String("server", serverName), zap.Error(rebuildErr))
	}
	callback := m.statusChange
	m.mu.Unlock()
	zap.L().Warn("MCP 服务器连接已断开",
		zap.String("server", serverName),
		zap.Error(err))
	if callback != nil {
		callback()
	}
}

// connectTimeout 对齐 MaiBot 建连超时：stdio 30s，远程 min(60, max(5, http+5))
func connectTimeout(cfg ServerConfig) time.Duration {
	seconds := 30.0
	if cfg.TransportType() != "stdio" {
		httpSeconds := cfg.HTTPTimeoutSeconds
		if httpSeconds <= 0 {
			httpSeconds = DefaultHTTPTimeoutSeconds
		}
		seconds = min(60.0, max(5.0, httpSeconds+5.0))
	}
	return time.Duration(seconds * float64(time.Second))
}

func errorMessage(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return fallback
	}
	return message
}

// applyToolExposureLocked 为已连接服务器的工具分配暴露名并重命名
func (m *Manager) applyToolExposureLocked() error {
	entries := make([]ToolRoute, 0, len(m.tools))
	for _, serverName := range m.connectionOrder {
		conn := m.connections[serverName]
		if conn == nil || !m.serverConnectedLocked(serverName) {
			continue
		}
		for _, def := range conn.toolDefs {
			entries = append(entries, ToolRoute{
				Original:    def.Name,
				ServerName:  serverName,
				Title:       def.Title,
				Description: def.Description,
			})
		}
	}

	exposedByServerTool := make(map[string]string, len(entries))
	if m.exposure == ToolExposurePrefixed {
		taken := make(map[string]struct{}, len(entries)+len(m.reserved))
		for name := range m.reserved {
			taken[name] = struct{}{}
		}
		for index := range entries {
			candidate := prefixedToolName(entries[index].ServerName, entries[index].Original)
			if _, exists := taken[candidate]; exists {
				return fmt.Errorf("MCP 工具规范化后重名 %q（服务器 %q，原工具 %q）",
					candidate, entries[index].ServerName, entries[index].Original)
			}
			taken[candidate] = struct{}{}
			entries[index].Exposed = candidate
		}
	} else {
		routes, err := buildBareToolRoutes(entries, m.reserved)
		if err != nil {
			return err
		}
		entries = routes
	}
	for _, route := range entries {
		exposedByServerTool[route.ServerName+"\x00"+route.Original] = route.Exposed
	}

	named := make([]tool.BaseTool, 0, len(m.tools))
	byServer := make(map[string][]tool.BaseTool, len(m.connectionOrder))
	for _, serverName := range m.connectionOrder {
		conn := m.connections[serverName]
		if conn == nil || !m.serverConnectedLocked(serverName) {
			continue
		}
		renamed, err := renameToolsWithPlan(conn, conn.tools, exposedByServerTool, serverName)
		if err != nil {
			return err
		}
		byServer[serverName] = renamed
	}
	for _, serverName := range m.connectionOrder {
		named = append(named, byServer[serverName]...)
	}
	m.tools = named
	m.routes = entries
	m.routeByName = make(map[string]ToolRoute, len(entries)*2)
	for _, route := range entries {
		m.routeByName[route.Exposed] = route
		if alias := prefixedToolName(route.ServerName, route.Original); alias != route.Exposed {
			m.routeByName[alias] = route
		}
	}
	return nil
}

// renameToolsWithPlan 按预先分配的暴露名重命名单个服务器的工具
func renameToolsWithPlan(conn *serverConnection, items []tool.BaseTool, plan map[string]string, serverName string) ([]tool.BaseTool, error) {
	out := make([]tool.BaseTool, 0, len(items))
	defs := make(map[string]ToolDefinition, len(conn.toolDefs))
	for _, def := range conn.toolDefs {
		defs[def.Name] = def
	}
	for _, item := range items {
		if item == nil {
			return nil, fmt.Errorf("MCP 服务器 %q 返回空工具", serverName)
		}
		info, err := item.Info(context.Background())
		if err != nil {
			return nil, fmt.Errorf("读取 MCP 服务器 %q 的工具名称失败: %w", serverName, err)
		}
		if info == nil || strings.TrimSpace(info.Name) == "" {
			return nil, fmt.Errorf("MCP 服务器 %q 返回的工具名称为空", serverName)
		}
		original := info.Name
		exposed, ok := plan[serverName+"\x00"+original]
		if !ok {
			return nil, fmt.Errorf("MCP 服务器 %q 的工具 %q 未出现在声明列表中", serverName, original)
		}
		invokable, ok := item.(tool.InvokableTool)
		if !ok {
			return nil, fmt.Errorf("MCP 工具 %q 不可调用", original)
		}
		def := defs[original]
		out = append(out, renamedTool{
			InvokableTool: invokable,
			name:          exposed,
			serverName:    serverName,
			original:      original,
			conn:          conn,
			outputSchema:  def.OutputSchema,
		})
	}
	return out, nil
}

func (m *Manager) registerTools(conn *serverConnection) int {
	if len(conn.tools) != len(conn.toolDefs) {
		zap.L().Warn("MCP 工具声明与可调用工具数量不一致",
			zap.String("server", conn.cfg.Name),
			zap.Int("declared", len(conn.toolDefs)),
			zap.Int("invokable", len(conn.tools)))
	}
	m.tools = append(m.tools, conn.tools...)
	return len(conn.toolDefs)
}

func (m *Manager) registerPrompts(conn *serverConnection) int {
	registered := 0
	for _, prompt := range conn.prompts {
		name := prompt.Name
		if existing, exists := m.promptToServer[name]; exists {
			zap.L().Warn("MCP Prompt 重名，已跳过",
				zap.String("prompt", name),
				zap.String("server", conn.cfg.Name),
				zap.String("existing_server", existing))
			continue
		}
		m.promptToServer[name] = conn.cfg.Name
		registered++
	}
	return registered
}

func (m *Manager) registerResources(conn *serverConnection) int {
	registered := 0
	for _, resource := range conn.resources {
		uri := resource.URI
		if existing, exists := m.resourceToServer[uri]; exists {
			zap.L().Warn("MCP Resource 重名，已跳过",
				zap.String("uri", uri),
				zap.String("server", conn.cfg.Name),
				zap.String("existing_server", existing))
			continue
		}
		m.resourceToServer[uri] = conn.cfg.Name
		registered++
	}
	return registered
}

func (m *Manager) registerTemplates(conn *serverConnection) int {
	registered := 0
	for _, template := range conn.resourceTemplates {
		uriTemplate := resourceTemplatePreviewFromMCP(template).URITemplate
		if uriTemplate == "" {
			continue
		}
		if existing, exists := m.templateToServer[uriTemplate]; exists {
			zap.L().Warn("MCP Resource Template 重名，已跳过",
				zap.String("uri_template", uriTemplate),
				zap.String("server", conn.cfg.Name),
				zap.String("existing_server", existing))
			continue
		}
		m.templateToServer[uriTemplate] = conn.cfg.Name
		registered++
	}
	return registered
}

// GetTools 获取所有 MCP 工具
func (m *Manager) GetTools() []tool.BaseTool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]tool.BaseTool, len(m.tools))
	copy(out, m.tools)
	return out
}

// ToolRoutes 返回当前暴露名到原始工具名的路由表
func (m *Manager) ToolRoutes() []ToolRoute {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ToolRoute, len(m.routes))
	copy(out, m.routes)
	return out
}

// ToolSpecs 获取全部已注册 MCP 工具的统一声明，对齐 get_tool_specs
func (m *Manager) ToolSpecs() []tooling.ToolSpec {
	m.mu.RLock()
	defer m.mu.RUnlock()
	specs := make([]tooling.ToolSpec, 0, len(m.routes))
	for _, serverName := range m.connectionOrder {
		conn := m.connections[serverName]
		if conn == nil || !m.serverConnectedLocked(serverName) {
			continue
		}
		defs := make(map[string]ToolDefinition, len(conn.toolDefs))
		for _, def := range conn.toolDefs {
			defs[def.Name] = def
		}
		for _, route := range m.routes {
			if route.ServerName != serverName {
				continue
			}
			def, ok := defs[route.Original]
			if !ok {
				continue
			}
			specs = append(specs, buildToolSpec(route, def))
		}
	}
	return specs
}

// GetOpenAITools 获取兼容旧模型层的 MCP 工具定义，对齐 get_openai_tools
func (m *Manager) GetOpenAITools() []map[string]any {
	specs := m.ToolSpecs()
	out := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		if isMCPBuiltinToolName(spec.Name) {
			continue
		}
		serverName, _ := spec.Metadata["server_name"].(string)
		if route, ok := m.ResolveTool(spec.Name); !ok || route.ServerName != serverName {
			continue
		}
		parameters := spec.ParametersSchema
		if parameters == nil {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        spec.Name,
				"description": spec.BuildLLMDescription(),
				"parameters":  parameters,
			},
		})
	}
	return out
}

// buildToolSpec 把路由与原始声明投影为统一 ToolSpec
func buildToolSpec(route ToolRoute, def ToolDefinition) tooling.ToolSpec {
	description := strings.TrimSpace(def.Description)
	if description == "" {
		description = "来自 " + route.ServerName + " 的 MCP 工具"
	}
	metadata := make(map[string]any, len(def.Metadata)+1)
	for key, value := range def.Metadata {
		metadata[key] = value
	}
	metadata["server_name"] = route.ServerName
	return tooling.ToolSpec{
		Name:             route.Exposed,
		Title:            def.Title,
		Description:      description,
		ParametersSchema: def.InputSchema,
		OutputSchema:     def.OutputSchema,
		ProviderName:     maiBotProviderName,
		ProviderType:     maiBotProviderName,
		Enabled:          true,
		Icons:            convertIcons(def.Icons),
		Annotation:       convertAnnotation(def.Annotation),
		Metadata:         metadata,
	}
}

func convertIcons(icons []Icon) []tooling.ToolIcon {
	if len(icons) == 0 {
		return nil
	}
	out := make([]tooling.ToolIcon, 0, len(icons))
	for _, icon := range icons {
		out = append(out, tooling.ToolIcon{Src: icon.Src, MimeType: icon.MIMEType, Sizes: icon.Sizes})
	}
	return out
}

func convertAnnotation(annotation *Annotation) *tooling.ToolAnnotation {
	if annotation == nil {
		return nil
	}
	return &tooling.ToolAnnotation{
		Audience:    annotation.Audience,
		Priority:    annotation.Priority,
		Title:       annotation.Title,
		ReadOnly:    annotation.ReadOnly,
		Destructive: annotation.Destructive,
		Idempotent:  annotation.Idempotent,
		OpenWorld:   annotation.OpenWorld,
		Metadata:    annotation.Metadata,
	}
}

// isMCPBuiltinToolName 判断是否与内置工具名冲突
func isMCPBuiltinToolName(name string) bool {
	for _, builtin := range maiBotBuiltinToolNames {
		if builtin == name {
			return true
		}
	}
	return false
}

// ToolDefinitions 返回全部已连接服务器的原始工具声明（含 input/output schema）
func (m *Manager) ToolDefinitions() []ToolDefinition {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ToolDefinition, 0, len(m.tools))
	for _, serverName := range m.connectionOrder {
		conn := m.connections[serverName]
		if conn == nil || !m.serverConnectedLocked(serverName) {
			continue
		}
		out = append(out, conn.toolDefs...)
	}
	return out
}

// ResolveTool 按暴露名（裸名/冲突前缀名/旧 mcp- 名）解析路由
func (m *Manager) ResolveTool(name string) (ToolRoute, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	route, ok := m.routeByName[strings.TrimSpace(name)]
	return route, ok
}

// IsMCPTool 判断名称是否为已注册 MCP 工具，对齐 is_mcp_tool
func (m *Manager) IsMCPTool(name string) bool {
	_, ok := m.ResolveTool(name)
	return ok
}

// IsMCPPrompt 判断名称是否为已注册 MCP Prompt，对齐 is_mcp_prompt
func (m *Manager) IsMCPPrompt(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.promptToServer[strings.TrimSpace(name)]
	return ok
}

// IsMCPResource 判断 URI 是否为已注册 MCP Resource，对齐 is_mcp_resource
func (m *Manager) IsMCPResource(uri string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.resourceToServer[strings.TrimSpace(uri)]
	return ok
}

// PromptSpecs 获取全部已注册 MCP Prompt 声明，对齐 get_prompt_specs
func (m *Manager) PromptSpecs() []PromptSpec {
	m.mu.RLock()
	defer m.mu.RUnlock()
	specs := make([]PromptSpec, 0, len(m.promptToServer))
	for _, serverName := range m.connectionOrder {
		conn := m.connections[serverName]
		if conn == nil || !m.serverConnectedLocked(serverName) {
			continue
		}
		for _, prompt := range conn.prompts {
			if m.promptToServer[prompt.Name] != serverName {
				continue
			}
			specs = append(specs, buildPromptSpec(prompt, serverName))
		}
	}
	return specs
}

// ResourceSpecs 获取全部已注册 MCP Resource 声明，对齐 get_resource_specs
func (m *Manager) ResourceSpecs() []ResourceSpec {
	m.mu.RLock()
	defer m.mu.RUnlock()
	specs := make([]ResourceSpec, 0, len(m.resourceToServer))
	for _, serverName := range m.connectionOrder {
		conn := m.connections[serverName]
		if conn == nil || !m.serverConnectedLocked(serverName) {
			continue
		}
		for _, resource := range conn.resources {
			if m.resourceToServer[resource.URI] != serverName {
				continue
			}
			specs = append(specs, buildResourceSpec(resource, serverName))
		}
	}
	return specs
}

// ResourceTemplateSpecs 获取全部已注册 MCP Resource Template 声明
func (m *Manager) ResourceTemplateSpecs() []ResourceTemplateSpec {
	m.mu.RLock()
	defer m.mu.RUnlock()
	specs := make([]ResourceTemplateSpec, 0, len(m.templateToServer))
	for _, serverName := range m.connectionOrder {
		conn := m.connections[serverName]
		if conn == nil || !m.serverConnectedLocked(serverName) {
			continue
		}
		for _, template := range conn.resourceTemplates {
			uriTemplate := resourceTemplatePreviewFromMCP(template).URITemplate
			if m.templateToServer[uriTemplate] != serverName {
				continue
			}
			specs = append(specs, buildResourceTemplateSpec(template, serverName))
		}
	}
	return specs
}

// GetPrompt 读取指定 MCP Prompt，对齐 MCPManager.get_prompt
func (m *Manager) GetPrompt(ctx context.Context, promptName string, arguments map[string]string) (*PromptResult, error) {
	m.mu.RLock()
	serverName, ok := m.promptToServer[promptName]
	conn := m.connections[serverName]
	connected := m.serverConnectedLocked(serverName)
	m.mu.RUnlock()
	if !ok || conn == nil || !connected {
		return nil, newError(ErrorKindNotFound, serverName, "prompts/get",
			fmt.Sprintf("MCP Prompt %q 未找到", promptName), nil)
	}
	return conn.GetPrompt(ctx, promptName, arguments)
}

// ReadResource 读取指定 MCP Resource，对齐 MCPManager.read_resource
func (m *Manager) ReadResource(ctx context.Context, uri string) (*ResourceReadResult, error) {
	m.mu.RLock()
	serverName, ok := m.resourceToServer[uri]
	conn := m.connections[serverName]
	connected := m.serverConnectedLocked(serverName)
	m.mu.RUnlock()
	if !ok || conn == nil || !connected {
		return nil, newError(ErrorKindNotFound, serverName, "resources/read",
			fmt.Sprintf("MCP Resource %q 未找到", uri), nil)
	}
	return conn.ReadResource(ctx, uri)
}

// ToolCount 返回已注册工具数量
func (m *Manager) ToolCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.routes)
}

// PromptCount 返回已注册 Prompt 数量
func (m *Manager) PromptCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.promptToServer)
}

// ResourceCount 返回已注册 Resource 数量
func (m *Manager) ResourceCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.resourceToServer)
}

// TemplateCount 返回已注册 Resource Template 数量
func (m *Manager) TemplateCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.templateToServer)
}

// ServerCount 返回已连接服务器数量
func (m *Manager) ServerCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	count := 0
	for _, serverName := range m.connectionOrder {
		if m.serverConnectedLocked(serverName) {
			count++
		}
	}
	return count
}

// FeatureSummary 返回各服务器能力摘要，对齐 get_feature_summary
func (m *Manager) FeatureSummary() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	parts := make([]string, 0, len(m.connectionOrder))
	for _, serverName := range m.connectionOrder {
		conn := m.connections[serverName]
		if conn == nil || !m.serverConnectedLocked(serverName) {
			continue
		}
		prompts := 0
		for _, prompt := range conn.prompts {
			if m.promptToServer[prompt.Name] == serverName {
				prompts++
			}
		}
		resources := 0
		for _, resource := range conn.resources {
			if m.resourceToServer[resource.URI] == serverName {
				resources++
			}
		}
		templates := 0
		for _, template := range conn.resourceTemplates {
			if m.templateToServer[resourceTemplatePreviewFromMCP(template).URITemplate] == serverName {
				templates++
			}
		}
		parts = append(parts, fmt.Sprintf("  • %s: 工具 %d / Prompt %d / 资源 %d / 模板 %d",
			serverName, len(conn.tools), prompts, resources, templates))
	}
	return strings.Join(parts, "\n")
}

// StatusSnapshot 返回不含客户端句柄的状态副本
func (m *Manager) StatusSnapshot() StatusSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snapshot := StatusSnapshot{
		// 对齐 MaiBot MCPManager.get_status_snapshot：管理器已加载即视为 initialized，
		// 即使没有启用服务器或全部连接失败
		Initialized:   true,
		ToolCount:     len(m.tools),
		PromptCount:   len(m.promptToServer),
		ResourceCount: len(m.resourceToServer),
		TemplateCount: len(m.templateToServer),
		Servers:       make([]ServerStatus, 0, len(m.servers)),
	}
	for _, status := range m.servers {
		row := status
		// 断连后保留真实 Error，不再用残留连接覆盖；只有仍处于 connected 才刷新能力计数
		if conn := m.connections[status.Name]; conn != nil && status.Connected {
			row.ProtocolVersion = conn.protocolVersion
			row.PromptCount = 0
			row.ResourceCount = 0
			row.TemplateCount = 0
			for _, prompt := range conn.prompts {
				if m.promptToServer[prompt.Name] == status.Name {
					row.PromptCount++
					row.Prompts = append(row.Prompts, promptPreviewFromMCP(prompt))
				}
			}
			for _, resource := range conn.resources {
				if m.resourceToServer[resource.URI] == status.Name {
					row.ResourceCount++
					row.Resources = append(row.Resources, resourcePreviewFromMCP(resource))
				}
			}
			for _, template := range conn.resourceTemplates {
				if m.templateToServer[resourceTemplatePreviewFromMCP(template).URITemplate] == status.Name {
					row.TemplateCount++
					row.Templates = append(row.Templates, resourceTemplatePreviewFromMCP(template))
				}
			}
			row.Error = ""
		}
		if row.Connected {
			snapshot.ServerCount++
		}
		snapshot.Servers = append(snapshot.Servers, row)
	}
	return snapshot
}

// Close 关闭所有 MCP 连接
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if errs := m.closeLocked(); len(errs) > 0 {
		zap.L().Warn("关闭 MCP 客户端失败", zap.Errors("errors", errs))
	}
}

func (m *Manager) closeLocked() []error {
	m.closed = true
	var errs []error
	for _, serverName := range m.connectionOrder {
		conn := m.connections[serverName]
		if conn == nil {
			continue
		}
		if err := conn.close(); err != nil {
			errs = append(errs, err)
		}
	}
	m.connections = make(map[string]*serverConnection)
	m.connectionOrder = nil
	m.servers = nil
	m.serverConfigs = make(map[string]ServerConfig)
	m.reconnecting = make(map[string]struct{})
	m.tools = nil
	m.routes = nil
	m.routeByName = make(map[string]ToolRoute)
	m.promptToServer = make(map[string]string)
	m.resourceToServer = make(map[string]string)
	m.templateToServer = make(map[string]string)
	return errs
}

// renamedTool 把 MCP 工具改名成暴露名，调用能力原样透传
type renamedTool struct {
	tool.InvokableTool
	name         string
	serverName   string
	original     string
	conn         *serverConnection
	outputSchema map[string]any
}

func (t renamedTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	info, err := t.InvokableTool.Info(ctx)
	if err != nil || info == nil {
		return info, err
	}
	cloned := *info
	cloned.Name = t.name
	// 对齐 MaiBot get_tool_specs：服务端未提供描述时补出来源说明
	if strings.TrimSpace(cloned.Desc) == "" && t.serverName != "" {
		cloned.Desc = "来自 " + t.serverName + " 的 MCP 工具"
	}
	if cloned.Extra == nil {
		cloned.Extra = make(map[string]any, 3)
	} else {
		extra := make(map[string]any, len(cloned.Extra)+3)
		for key, value := range cloned.Extra {
			extra[key] = value
		}
		cloned.Extra = extra
	}
	if t.serverName != "" {
		cloned.Extra["server_name"] = t.serverName
	}
	if t.original != "" {
		cloned.Extra["mcp_tool_name"] = t.original
	}
	if len(t.outputSchema) > 0 {
		cloned.Extra["output_schema"] = t.outputSchema
	}
	return &cloned, nil
}

// isClosed 返回管理器是否已关闭
func (m *Manager) isClosed() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.closed
}

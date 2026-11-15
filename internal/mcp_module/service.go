package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/cloudwego/eino/components/tool"
	"go.uber.org/zap"

	"MaiGo/internal/core/tooling"
)

// Runtime Agent 侧只读 MCP 运行态视图
type Runtime interface {
	GetTools() []tool.BaseTool
	StatusSnapshot() StatusSnapshot
	ToolRoutes() []ToolRoute
	Close()
}

var (
	_ Runtime = (*Manager)(nil)
	_ Runtime = (*ServiceSession)(nil)
)

// Service 进程级 MCP 共享服务，对齐 MaiBot MCPService
//
// 连接按有效运行时配置签名热重载；执行中的调用持有租约，退休的管理器在租约释放后关闭
type Service struct {
	reloadMu sync.Mutex

	mu        sync.RWMutex
	clientCfg ClientConfig
	host      HostCallbacks
	manager   *Manager
	signature string
	leases    map[*Manager]int
	retired   map[*Manager]*Manager
	closed    bool
	reserved  []string

	// discoverExtended 对齐 Python MCPService 主流程：默认不发现 Prompt/Resource
	discoverExtended bool

	statusMu sync.RWMutex
	status   StatusSnapshot

	dial func(ctx context.Context, servers []ServerConfig, options []ManagerOption) (*Manager, error)
}

// ServiceOption 配置 Service
type ServiceOption func(*Service)

// WithServiceHostCallbacks 注入宿主回调，热重载时沿用
func WithServiceHostCallbacks(host HostCallbacks) ServiceOption {
	return func(s *Service) { s.host = host }
}

// WithServiceReservedToolNames 注入需要避让的内置工具名，热重载时沿用
func WithServiceReservedToolNames(names ...string) ServiceOption {
	return func(s *Service) { s.reserved = normalizeReservedToolNames(names) }
}

// WithServiceDiscoverExtendedFeatures 控制是否发现 Prompt/Resource；默认关闭，对齐 Python MCPService
func WithServiceDiscoverExtendedFeatures(enabled bool) ServiceOption {
	return func(s *Service) { s.discoverExtended = enabled }
}

// NewService 创建进程级 MCP 服务
func NewService(clientCfg ClientConfig, options ...ServiceOption) *Service {
	service := &Service{
		clientCfg: clientCfg.Normalize(),
		signature: "\x00uninitialized",
		leases:    make(map[*Manager]int),
		retired:   make(map[*Manager]*Manager),
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	service.dial = service.defaultDial
	return service
}

func (s *Service) defaultDial(ctx context.Context, servers []ServerConfig, options []ManagerOption) (*Manager, error) {
	manager := NewMCPManager(options...)
	err := manager.Load(ctx, servers)
	return manager, err
}

var defaultService = NewService(DefaultClientConfig())

// DefaultService 返回进程级 MCP 服务单例，对齐 get_mcp_service
func DefaultService() *Service { return defaultService }

// SetHostCallbacks 更新宿主回调；下一次热重载生效
func (s *Service) SetHostCallbacks(host HostCallbacks) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.host = host
	s.mu.Unlock()
}

// SetReservedToolNames 更新需要避让的内置工具名；下一次热重载生效
func (s *Service) SetReservedToolNames(names ...string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.reserved = normalizeReservedToolNames(names)
	s.mu.Unlock()
}

func normalizeReservedToolNames(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

func signatureWithReserved(signature string, reserved []string) string {
	if len(reserved) == 0 {
		return signature
	}
	encoded, err := json.Marshal(reserved)
	if err != nil {
		return signature
	}
	return signature + "\x00reserved:" + string(encoded)
}

// SetClientConfig 更新客户端宿主能力配置；下一次热重载生效
func (s *Service) SetClientConfig(cfg ClientConfig) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.clientCfg = cfg.Normalize()
	s.mu.Unlock()
}

// ClientConfig 返回当前客户端配置
func (s *Service) ClientConfig() ClientConfig {
	if s == nil {
		return DefaultClientConfig()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.clientCfg
}

// Signature 返回当前生效的配置签名
func (s *Service) Signature() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.signature
}

// EnsureInitialized 确保共享服务已按当前配置初始化，对齐 ensure_initialized
func (s *Service) EnsureInitialized(ctx context.Context, servers []ServerConfig) error {
	return s.Reload(ctx, servers)
}

// Reload 按配置签名热重载；签名一致时直接复用现有连接
func (s *Service) Reload(ctx context.Context, servers []ServerConfig) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()

	s.mu.RLock()
	clientCfg := s.clientCfg
	host := s.host
	closed := s.closed
	signature := s.signature
	reserved := append([]string(nil), s.reserved...)
	discoverExtended := s.discoverExtended
	s.mu.RUnlock()

	nextSignature := signatureWithReserved(ConfigSignature(servers, clientCfg), reserved)
	if !closed && nextSignature == signature {
		return nil
	}

	options := []ManagerOption{
		WithClientConfig(clientCfg),
		WithHostCallbacks(host),
		WithReservedToolNames(reserved...),
		WithDiscoverExtendedFeatures(discoverExtended),
	}
	next, loadErr := s.dial(ctx, servers, options)
	if next == nil {
		if loadErr == nil {
			loadErr = fmt.Errorf("MCP 共享服务构建失败")
		}
		return loadErr
	}

	s.mu.Lock()
	previous := s.manager
	s.manager = next
	s.signature = nextSignature
	s.closed = false
	closeNow := s.retireLocked(previous)
	s.mu.Unlock()

	next.SetStatusChangeCallback(func() {
		s.mu.RLock()
		current := s.manager
		s.mu.RUnlock()
		if current == next {
			s.updateStatus(next.StatusSnapshot())
		}
	})
	s.updateStatus(next.StatusSnapshot())
	if previous != nil {
		zap.L().Debug("MCP 共享服务已替换管理器",
			zap.Int("previous_servers", previous.ServerCount()),
			zap.Int("servers", next.ServerCount()),
			zap.Int("tools", next.ToolCount()))
	}
	if closeNow != nil {
		closeNow.Close()
	}
	return loadErr
}

// retireLocked 退休旧管理器；仍有租约时延迟关闭
func (s *Service) retireLocked(manager *Manager) *Manager {
	if manager == nil {
		return nil
	}
	if s.leases[manager] > 0 {
		s.retired[manager] = manager
		return nil
	}
	return manager
}

// acquire 租用当前管理器，防止热重载关闭仍在执行调用的连接
func (s *Service) acquire() (*Manager, func()) {
	if s == nil {
		return nil, func() {}
	}
	s.mu.Lock()
	manager := s.manager
	if manager != nil {
		s.leases[manager]++
	}
	s.mu.Unlock()

	release := func() {
		if manager == nil {
			return
		}
		var closeNow *Manager
		s.mu.Lock()
		count := s.leases[manager] - 1
		if count > 0 {
			s.leases[manager] = count
		} else {
			delete(s.leases, manager)
			closeNow = s.retired[manager]
			delete(s.retired, manager)
		}
		s.mu.Unlock()
		if closeNow != nil {
			closeNow.Close()
		}
	}
	return manager, release
}

// ListTools 列出共享管理器当前可用的工具，对齐 list_tools
func (s *Service) ListTools() []tool.BaseTool {
	manager, release := s.acquire()
	defer release()
	if manager == nil {
		return nil
	}
	return manager.GetTools()
}

// GetTools 实现 Runtime 接口
func (s *Service) GetTools() []tool.BaseTool { return s.ListTools() }

// ToolSpecs 列出共享管理器当前可用的统一工具声明，对齐 list_tools
func (s *Service) ToolSpecs() []tooling.ToolSpec {
	manager, release := s.acquire()
	defer release()
	if manager == nil {
		return nil
	}
	return manager.ToolSpecs()
}

// GetOpenAITools 列出兼容旧模型层的 MCP 工具定义
func (s *Service) GetOpenAITools() []map[string]any {
	manager, release := s.acquire()
	defer release()
	if manager == nil {
		return nil
	}
	return manager.GetOpenAITools()
}

// PromptSpecs 列出共享管理器已注册的 Prompt
func (s *Service) PromptSpecs() []PromptSpec {
	manager, release := s.acquire()
	defer release()
	if manager == nil {
		return nil
	}
	return manager.PromptSpecs()
}

// ResourceSpecs 列出共享管理器已注册的 Resource
func (s *Service) ResourceSpecs() []ResourceSpec {
	manager, release := s.acquire()
	defer release()
	if manager == nil {
		return nil
	}
	return manager.ResourceSpecs()
}

// ResourceTemplateSpecs 列出共享管理器已注册的 Resource Template
func (s *Service) ResourceTemplateSpecs() []ResourceTemplateSpec {
	manager, release := s.acquire()
	defer release()
	if manager == nil {
		return nil
	}
	return manager.ResourceTemplateSpecs()
}

// GetPrompt 读取共享管理器上的 Prompt，对齐 MCPService 的租约语义
func (s *Service) GetPrompt(ctx context.Context, promptName string, arguments map[string]string) (*PromptResult, error) {
	manager, release := s.acquire()
	defer release()
	if manager == nil {
		return nil, newError(ErrorKindNotFound, "", "prompts/get",
			fmt.Sprintf("MCP Prompt %q 未找到", promptName), nil)
	}
	return manager.GetPrompt(ctx, promptName, arguments)
}

// ReadResource 读取共享管理器上的 Resource
func (s *Service) ReadResource(ctx context.Context, uri string) (*ResourceReadResult, error) {
	manager, release := s.acquire()
	defer release()
	if manager == nil {
		return nil, newError(ErrorKindNotFound, "", "resources/read",
			fmt.Sprintf("MCP Resource %q 未找到", uri), nil)
	}
	return manager.ReadResource(ctx, uri)
}

// CallTool 调用共享管理器上的工具，对齐 call_tool_invocation
func (s *Service) CallTool(ctx context.Context, serverName, originalToolName string, arguments map[string]any) (map[string]any, error) {
	manager, release := s.acquire()
	defer release()
	if manager == nil {
		return nil, ErrNotConfigured
	}
	return manager.CallTool(ctx, serverName, originalToolName, arguments)
}

// CallToolResult 调用共享管理器上的工具并保留结构化结果
//
// 无可用管理器时按 Python 返回失败结果并带上原因文案；ToolName 留空，
// 让调用方沿用请求里的工具名，同时保留错误语义。
func (s *Service) CallToolResult(ctx context.Context, serverName, originalToolName string, arguments map[string]any) (ToolExecutionResult, error) {
	manager, release := s.acquire()
	defer release()
	if manager == nil {
		return ToolExecutionResult{
			ToolName:     originalToolName,
			Success:      false,
			ErrorMessage: "MCP 共享服务尚未连接任何服务器",
			ContentItems: []ToolContentItem{},
		}, ErrNotConfigured
	}
	return manager.CallToolResult(ctx, serverName, originalToolName, arguments)
}

// ToolDefinitions 返回共享管理器上的原始工具声明（含 input/output schema）
func (s *Service) ToolDefinitions() []ToolDefinition {
	manager, release := s.acquire()
	defer release()
	if manager == nil {
		return nil
	}
	return manager.ToolDefinitions()
}

// InvokeTool 调用共享管理器上的工具并返回文本结果
func (s *Service) InvokeTool(ctx context.Context, serverName, originalToolName string, arguments map[string]any) (string, error) {
	manager, release := s.acquire()
	defer release()
	if manager == nil {
		return "", ErrNotConfigured
	}
	return manager.InvokeTool(ctx, serverName, originalToolName, arguments)
}

// InvokeToolByExposedName 按暴露名调用共享管理器上的工具
func (s *Service) InvokeToolByExposedName(ctx context.Context, exposedName string, arguments map[string]any) (string, error) {
	manager, release := s.acquire()
	defer release()
	if manager == nil {
		return "", ErrNotConfigured
	}
	return manager.InvokeToolByExposedName(ctx, exposedName, arguments)
}

// ToolRoutes 返回当前暴露名路由快照
func (s *Service) ToolRoutes() []ToolRoute {
	manager, release := s.acquire()
	defer release()
	if manager == nil {
		return nil
	}
	return manager.ToolRoutes()
}

// StatusSnapshot 返回线程安全的状态副本，对齐 get_status_snapshot
func (s *Service) StatusSnapshot() StatusSnapshot {
	if s == nil {
		return StatusSnapshot{}
	}
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	snapshot := s.status
	snapshot.Servers = make([]ServerStatus, len(s.status.Servers))
	for index, server := range s.status.Servers {
		server.Prompts = append([]PromptPreview(nil), server.Prompts...)
		server.Resources = append([]ResourcePreview(nil), server.Resources...)
		server.Templates = append([]ResourceTemplatePreview(nil), server.Templates...)
		snapshot.Servers[index] = server
	}
	return snapshot
}

func (s *Service) updateStatus(snapshot StatusSnapshot) {
	s.statusMu.Lock()
	s.status = snapshot
	s.statusMu.Unlock()
}

// Close 关闭当前及已退休的全部管理器，对齐 MCPService.close
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()

	s.mu.Lock()
	current := s.manager
	s.manager = nil
	managers := make([]*Manager, 0, len(s.retired)+1)
	for _, manager := range s.retired {
		managers = append(managers, manager)
	}
	s.retired = make(map[*Manager]*Manager)
	if current != nil {
		managers = append(managers, current)
	}
	s.signature = ""
	s.closed = true
	s.mu.Unlock()

	var closeErr error
	for _, manager := range managers {
		manager.Close()
	}
	s.updateStatus(StatusSnapshot{Initialized: false})
	return closeErr
}

// Lease 出租当前管理器；热重载会等待租约释放后再关闭旧连接
func (s *Service) Lease() (*Manager, func()) {
	return s.acquire()
}

// Session 返回不拥有共享连接的 Agent 运行态视图
func (s *Service) Session() *ServiceSession {
	return &ServiceSession{service: s}
}

// ServiceSession Agent 侧运行态视图；Close 不释放共享连接
type ServiceSession struct {
	service *Service
}

// GetTools 返回共享管理器当前可用的工具
func (v *ServiceSession) GetTools() []tool.BaseTool {
	if v == nil || v.service == nil {
		return nil
	}
	return v.service.ListTools()
}

// StatusSnapshot 返回共享管理器状态快照
func (v *ServiceSession) StatusSnapshot() StatusSnapshot {
	if v == nil || v.service == nil {
		return StatusSnapshot{}
	}
	return v.service.StatusSnapshot()
}

// ToolRoutes 返回共享管理器路由快照
func (v *ServiceSession) ToolRoutes() []ToolRoute {
	if v == nil || v.service == nil {
		return nil
	}
	return v.service.ToolRoutes()
}

// Close 释放视图，不影响共享连接
func (v *ServiceSession) Close() {}

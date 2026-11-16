package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"

	"MaiGo/internal/agent"
	"MaiGo/internal/agent/files"
	"MaiGo/internal/chat/session"
	"MaiGo/internal/common/media"
	"MaiGo/internal/files"

	"github.com/bytedance/sonic"
)

const maibotAgentID = "__maibot_agent__"

// SetACPService 注入外接 Agent 服务，供 agent_* 工具使用
func (a *Agent) SetACPService(service *agent.Service) {
	if a == nil {
		return
	}
	a.acpService = service
}

// SetFileService 注入文件服务，供 files_* 工具使用
func (a *Agent) SetFileService(service *filesvc.Service) {
	if a == nil {
		return
	}
	a.fileService = service
}

// newAgentFilesHost 按真实聊天流构造 agent_* 工具宿主
func (a *Agent) newAgentFilesHost(sessionID string) agentfiles.Host {
	return &conversationAgentFilesHost{agent: a, sessionID: strings.TrimSpace(sessionID)}
}

// newFileToolHost 按真实聊天流构造 files_* 工具宿主（对应 src/files/provider.py 的宿主侧）
func (a *Agent) newFileToolHost(sessionID string) filesvc.FileToolHost {
	return &conversationAgentFilesHost{agent: a, sessionID: strings.TrimSpace(sessionID)}
}

type conversationAgentFilesHost struct {
	agent     *Agent
	sessionID string
}

// TellAgent 普通聊天向独立 Agent 传话，对齐 messaging.tell 的非 Agent 分支
func (h *conversationAgentFilesHost) TellAgent(ctx context.Context, text, target string, images []string) (map[string]any, error) {
	if h.agent == nil || h.agent.acpService == nil {
		return nil, fmt.Errorf("Agent 未启动，请在设置的“Agent连接”中启用连接。")
	}
	if strings.TrimSpace(text) == "" && len(images) == 0 {
		return nil, fmt.Errorf("传话内容不能为空。")
	}
	if target != "" && target != maibotAgentID {
		return nil, fmt.Errorf("普通聊天只能向指定 Agent 传话。")
	}
	if h.sessionID == "" {
		return nil, fmt.Errorf("找不到真实聊天流，请使用聊天列表工具查询。")
	}
	if h.agent.chatSessions == nil {
		return nil, fmt.Errorf("找不到真实聊天流，请使用聊天列表工具查询。")
	}
	record, err := h.agent.chatSessions.GetExistingBySessionID(ctx, h.sessionID)
	if err != nil || record == nil {
		return nil, fmt.Errorf("找不到真实聊天流，请使用聊天列表工具查询。")
	}
	name := chatsession.DisplayName(record)
	if name == "" {
		name = "None"
	}
	resolved, attachments, err := h.resolveChatRelayImages(ctx, images)
	if err != nil {
		return nil, err
	}
	relayID := randomRelayID()
	relayText := fmt.Sprintf("[传话 %s，来自 %s，回复目标 %s]\n%s\n", relayID, name, h.sessionID, text)
	if len(attachments) > 0 {
		encoded, err := sonic.MarshalString(attachments)
		if err != nil {
			return nil, err
		}
		relayText += "附图：" + encoded + "\n"
	}
	relayText += "这是传话内容，可通过 agent_talk 回复；按需要回复，不必礼貌性来回传话。"
	paths := make([]string, 0, len(resolved))
	for _, image := range resolved {
		paths = append(paths, image.Path)
	}
	task, err := h.agent.acpService.Enqueue(relayText, paths, "user", h.sessionID)
	if err != nil {
		return nil, err
	}
	status := h.agent.acpService.Status()
	result := map[string]any{
		"delivered": true,
		"relay_id":  relayID,
		"target":    maibotAgentID,
		"name":      status.Name,
		"images":    attachments,
		"task_id":   task.ID,
		"queued":    true,
		"paused":    status.Paused,
	}
	if status.Paused {
		result["error"] = status.Error
		result["notice"] = "Agent 已暂停，消息仅入队；请在 WebUI 恢复后执行。"
	}
	return result, nil
}

// resolveChatRelayImages 对齐 messaging.tell 非 Agent 分支：从当前会话 source_images 解析附图
func (h *conversationAgentFilesHost) resolveChatRelayImages(ctx context.Context, references []string) ([]agent.RelayImage, []map[string]string, error) {
	if len(references) == 0 {
		return nil, []map[string]string{}, nil
	}
	workspace := filepath.Join(h.agent.acpService.DataDir(), "workspace")
	resolved, err := agent.ResolveRelayImages(ctx, &chatRelayImageResolver{
		source:    func() map[string][]media.Ref { return h.agent.SessionSourceImages(h.sessionID) },
		workspace: workspace,
	}, references)
	if err != nil {
		return nil, nil, err
	}
	attachments := make([]map[string]string, 0, len(resolved))
	for _, image := range resolved {
		attachments = append(attachments, map[string]string{"hash": image.Hash, "path": image.Path})
	}
	return resolved, attachments, nil
}

type chatRelayImageResolver struct {
	source    func() map[string][]media.Ref
	workspace string
}

func (r *chatRelayImageResolver) SourceImages() map[string][]media.Ref {
	if r == nil || r.source == nil {
		return nil
	}
	return r.source()
}

func (r *chatRelayImageResolver) Workspace() string {
	if r == nil {
		return ""
	}
	return r.workspace
}

func (r *chatRelayImageResolver) AllowAgentOnlyRefs() bool                    { return false }
func (r *chatRelayImageResolver) ResolveWorkspacePath(string) (string, error) { return "", nil }

// AgentStatus 返回 Agent 状态
func (h *conversationAgentFilesHost) AgentStatus(ctx context.Context) (map[string]any, []agentfiles.Image, error) {
	if h.agent == nil || h.agent.acpService == nil {
		return nil, nil, fmt.Errorf("Agent 未启动，请在设置的“Agent连接”中启用连接。")
	}
	status := h.agent.acpService.Status()
	raw, err := statusMap(status)
	if err != nil {
		return nil, nil, err
	}
	payload, images := agentfiles.StatusPayload(raw)
	return payload, images, nil
}

// AgentChats 仅独立 Agent 可用
func (h *conversationAgentFilesHost) AgentChats(context.Context) ([]map[string]any, error) {
	return nil, fmt.Errorf("跨聊天读取由独立 Agent 执行；普通聊天请使用 agent_talk。")
}

// AgentRecent 仅独立 Agent 可用
func (h *conversationAgentFilesHost) AgentRecent(context.Context, string, int) (map[string]any, error) {
	return nil, fmt.Errorf("跨聊天读取由独立 Agent 执行；普通聊天请使用 agent_talk。")
}

// ListFiles 列出当前聊天的附件
func (h *conversationAgentFilesHost) ListFiles(ctx context.Context, offset int) (any, error) {
	service, err := h.fileService(ctx)
	if err != nil {
		return nil, err
	}
	return service.PluginList(h.sessionID, offset)
}

// DownloadFile 下载当前聊天的附件并返回公开信息
func (h *conversationAgentFilesHost) DownloadFile(ctx context.Context, fileRef string) (any, error) {
	service, err := h.fileService(ctx)
	if err != nil {
		return nil, err
	}
	asset, err := service.Ensure(ctx, fileRef, h.sessionID, false)
	if err != nil {
		return nil, err
	}
	return filesvc.PublicView(asset), nil
}

// ReadTextFile 读取当前聊天的文本附件
func (h *conversationAgentFilesHost) ReadTextFile(ctx context.Context, fileRef string, offset, limit int, encoding string) (any, error) {
	service, err := h.fileService(ctx)
	if err != nil {
		return nil, err
	}
	return service.PluginReadText(fileRef, h.sessionID, offset, limit, encoding)
}

// CreateTextFile 保存文本附件
func (h *conversationAgentFilesHost) CreateTextFile(ctx context.Context, name, text string) (any, error) {
	service, err := h.fileService(ctx)
	if err != nil {
		return nil, err
	}
	asset, err := service.SaveContent(h.sessionID, name, "text/plain", []byte(text))
	if err != nil {
		return nil, err
	}
	return filesvc.PublicView(asset), nil
}

// SendFile 把附件发送到当前聊天
func (h *conversationAgentFilesHost) SendFile(ctx context.Context, fileRef string) (any, error) {
	service, err := h.fileService(ctx)
	if err != nil {
		return nil, err
	}
	return service.PluginSend(ctx, fileRef, h.sessionID)
}

func (h *conversationAgentFilesHost) fileService(ctx context.Context) (*filesvc.Service, error) {
	if h.agent == nil || h.agent.fileService == nil || !h.agent.fileService.Enabled() {
		return nil, filesvc.ErrDisabled
	}
	if h.sessionID == "" {
		return nil, fmt.Errorf("文件工具需要可信聊天上下文")
	}
	if h.agent.chatSessions != nil {
		if _, err := h.agent.chatSessions.GetExistingBySessionID(ctx, h.sessionID); err != nil {
			return nil, fmt.Errorf("聊天流不存在")
		}
	}
	return h.agent.fileService, nil
}

func statusMap(status agent.Status) (map[string]any, error) {
	encoded, err := sonic.MarshalString(status)
	if err != nil {
		return nil, fmt.Errorf("Agent 状态序列化失败: %w", err)
	}
	out := map[string]any{}
	if err := sonic.UnmarshalString(encoded, &out); err != nil {
		return nil, fmt.Errorf("Agent 状态解析失败: %w", err)
	}
	return out, nil
}

func randomRelayID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%x", statusFallbackID())
	}
	return hex.EncodeToString(raw[:])
}

var relayFallbackCounter atomic.Uint64

func statusFallbackID() uint64 {
	return relayFallbackCounter.Add(1)
}

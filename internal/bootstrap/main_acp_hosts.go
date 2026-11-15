package bootstrap

// ACP 工具宿主：把真实聊天流、文件服务与电脑状态接到外接 Agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"MaiGo/internal/common/media"
	"strconv"
	"strings"
	"time"

	"MaiGo/internal/agent"
	"MaiGo/internal/chat/session"
	"MaiGo/internal/common/database"
	"MaiGo/internal/computer"
	"MaiGo/internal/core/internal_chat"
	"MaiGo/internal/files"
	maisaruntime "MaiGo/internal/maisaka/runtime"
	"MaiGo/internal/platform_io"
	"MaiGo/internal/platform_io/drivers/onebot"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"
)

type acpAgentHost struct {
	mem      *memory.Manager
	agent    *maisaruntime.Agent
	sessions *chatsession.Manager
	name     string
	// workspace Agent 工作区目录，传话附图落盘到其下 relay_images
	workspace string
	// computer 桌面截图来源；nil 时 screenshot 引用返回真实错误
	computer *computer.Service
}

var _ agent.AgentToolHost = (*acpAgentHost)(nil)

func newACPAgentHost(mem *memory.Manager, maiAgent *maisaruntime.Agent, name string) *acpAgentHost {
	host := &acpAgentHost{mem: mem, agent: maiAgent, name: strings.TrimSpace(name)}
	if mem != nil {
		host.sessions = chatsession.NewManager(mem.GetDB())
	}
	return host
}

func (h *acpAgentHost) ListAgentChats(ctx context.Context) ([]map[string]any, error) {
	if h == nil || h.mem == nil {
		return nil, fmt.Errorf("聊天存储未就绪")
	}
	var rows []chatsession.Record
	if err := h.mem.GetDB().WithContext(ctx).
		Order("last_active_timestamp DESC NULLS LAST, session_id").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		name := chatsession.DisplayName(row)
		if name == "" {
			name = row.SessionID
		}
		lastActive := ""
		if row.LastActiveTimestamp != nil {
			lastActive = row.LastActiveTimestamp.Format(time.RFC3339)
		}
		out = append(out, map[string]any{
			"session_id": row.SessionID, "name": name,
			"platform": row.Platform, "last_active": lastActive,
		})
	}
	return out, nil
}

func (h *acpAgentHost) RecentAgentMessages(ctx context.Context, target string, limit int) (map[string]any, error) {
	if h == nil || h.mem == nil {
		return nil, fmt.Errorf("聊天存储未就绪")
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, fmt.Errorf("找不到真实聊天流，请使用聊天列表工具查询。")
	}
	if limit < 0 {
		return nil, fmt.Errorf("limit 不能为负数，0 表示全部。")
	}
	var row chatsession.Record
	if err := h.mem.GetDB().WithContext(ctx).Where("session_id = ?", target).First(&row).Error; err != nil {
		return nil, fmt.Errorf("找不到真实聊天流，请使用聊天列表工具查询。")
	}
	queryLimit := limit
	if queryLimit == 0 {
		queryLimit = 500
	}
	logs, err := h.mem.ListRecentMessagesBySessionID(ctx, target, queryLimit, 0)
	if err != nil {
		return nil, err
	}
	lines := make([]string, 0, len(logs))
	for i := range logs {
		item := &logs[i]
		text := strings.TrimSpace(item.ProcessedPlainText)
		if text == "" {
			text = strings.TrimSpace(item.ProcessedPlainText)
		}
		if text == "" {
			continue
		}
		nickname := strings.TrimSpace(item.UserNickname)
		if nickname == "" {
			nickname = strconv.FormatInt(item.UserID, 10)
		}
		lines = append(lines, fmt.Sprintf("[%s] %s: %s", item.Timestamp.Format("15:04:05"), nickname, text))
	}
	name := chatsession.DisplayName(&row)
	if name == "" {
		name = target
	}
	return map[string]any{"name": name, "messages": strings.Join(lines, "\n")}, nil
}

func (h *acpAgentHost) TellAgent(ctx context.Context, text, target string, images []string) (map[string]any, error) {
	if h == nil || h.agent == nil || h.mem == nil {
		return nil, fmt.Errorf("Agent 传话通道未接线")
	}
	text = strings.TrimSpace(text)
	if text == "" && len(images) == 0 {
		return nil, fmt.Errorf("传话内容不能为空。")
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, fmt.Errorf("找不到真实聊天流，请使用聊天列表工具查询。")
	}
	var row chatsession.Record
	if err := h.mem.GetDB().WithContext(ctx).Where("session_id = ?", target).First(&row).Error; err != nil {
		return nil, fmt.Errorf("找不到真实聊天流，请使用聊天列表工具查询。")
	}
	kind, targetID, err := chatTargetFromRecord(&row)
	if err != nil {
		return nil, err
	}
	relayID := fmt.Sprintf("%d", time.Now().UnixNano())
	name := h.name
	if name == "" {
		name = "麦麦的 Agent"
	}
	// 对齐 Python tell()：先解析附图，附件清单以 JSON 写入传话文本
	relayImages, attachments, err := h.resolveRelayImages(ctx, images)
	if err != nil {
		return nil, err
	}
	relayText := fmt.Sprintf("[传话 %s，来自 %s，回复目标 %s]\n%s\n", relayID, name, target, text)
	if len(attachments) > 0 {
		encoded, err := json.Marshal(attachments)
		if err != nil {
			return nil, err
		}
		relayText += "附图：" + string(encoded) + "\n"
	}
	relayText += fmt.Sprintf("这是传话内容，可通过 agent_talk 回复；按需要回复，不必礼貌性来回传话。")
	receipt, err := h.agent.EnqueueInternalTaskWithImages(ctx, kind, targetID, relayText, "agent_relay", name+"传话", relayImages)
	if err != nil {
		return nil, err
	}
	chatName := chatsession.DisplayName(&row)
	if chatName == "" {
		chatName = target
	}
	out := map[string]any{
		"delivered": true, "relay_id": relayID, "target": target,
		"name": chatName, "images": attachments,
	}
	for key, value := range receipt {
		out[key] = value
	}
	return out, nil
}

type acpFileHost struct {
	files *filesvc.Service
	mem   *memory.Manager
}

var _ agent.FileToolHost = (*acpFileHost)(nil)

func newACPFileHost(files *filesvc.Service, mem *memory.Manager) *acpFileHost {
	return &acpFileHost{files: files, mem: mem}
}

func (h *acpFileHost) sessionID(ctx context.Context) (string, error) {
	if h == nil || h.mem == nil {
		return "", fmt.Errorf("文件服务未接线")
	}
	var row chatsession.Record
	err := h.mem.GetDB().WithContext(ctx).
		Where("platform = ? AND group_id IS NULL AND user_id = ?", internalchat.Platform, internalchat.OwnerUserID).
		Order("last_active_timestamp DESC NULLS LAST, session_id").
		First(&row).Error
	if err != nil || strings.TrimSpace(row.SessionID) == "" {
		return "", fmt.Errorf("找不到真实聊天流，请使用聊天列表工具查询。")
	}
	return row.SessionID, nil
}

func (h *acpFileHost) ListFiles(ctx context.Context, offset int) (any, error) {
	if h == nil || h.files == nil {
		return nil, fmt.Errorf("文件服务未接线")
	}
	sessionID, err := h.sessionID(ctx)
	if err != nil {
		return nil, err
	}
	if offset < 0 {
		return nil, fmt.Errorf("分页参数无效")
	}
	return h.files.Store().List(sessionID, false, offset, 50)
}

func (h *acpFileHost) DownloadFile(ctx context.Context, fileRef string) (any, error) {
	if h == nil || h.files == nil {
		return nil, fmt.Errorf("文件服务未接线")
	}
	sessionID, err := h.sessionID(ctx)
	if err != nil {
		return nil, err
	}
	asset, err := h.files.Ensure(ctx, fileRef, sessionID, false)
	if err != nil {
		return nil, err
	}
	return filesvc.PublicView(asset), nil
}

func (h *acpFileHost) ReadTextFile(ctx context.Context, fileRef string, offset int, limit int, encoding string) (any, error) {
	if h == nil || h.files == nil {
		return nil, fmt.Errorf("文件服务未接线")
	}
	sessionID, err := h.sessionID(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := h.files.Ensure(ctx, fileRef, sessionID, false); err != nil {
		return nil, err
	}
	return h.files.ReadText(fileRef, sessionID, false, offset, limit, encoding)
}

func (h *acpFileHost) CreateTextFile(ctx context.Context, name string, text string) (any, error) {
	if h == nil || h.files == nil {
		return nil, fmt.Errorf("文件服务未接线")
	}
	sessionID, err := h.sessionID(ctx)
	if err != nil {
		return nil, err
	}
	asset, err := h.files.SaveContent(sessionID, name, "text/plain", []byte(text))
	if err != nil {
		return nil, err
	}
	return filesvc.PublicView(asset), nil
}

func (h *acpFileHost) SendFile(ctx context.Context, fileRef string) (any, error) {
	if h == nil || h.files == nil {
		return nil, fmt.Errorf("文件服务未接线")
	}
	sessionID, err := h.sessionID(ctx)
	if err != nil {
		return nil, err
	}
	return h.files.PluginSend(ctx, fileRef, sessionID)
}

func chatTargetFromRecord(row *chatsession.Record) (string, int64, error) {
	if row == nil {
		return "", 0, fmt.Errorf("聊天会话不存在")
	}
	if row.IsGroup() {
		groupID := strings.TrimSpace(*row.GroupID)
		if internalchat.IsVirtualGroupID(groupID) {
			return memory.ConversationKindGroup, internalchat.VirtualTargetID(groupID), nil
		}
		parsed, err := strconv.ParseInt(groupID, 10, 64)
		if err != nil || parsed <= 0 {
			return "", 0, fmt.Errorf("群聊目标无效")
		}
		return memory.ConversationKindGroup, parsed, nil
	}
	if row.UserID == nil {
		return "", 0, fmt.Errorf("私聊目标无效")
	}
	userID := strings.TrimSpace(*row.UserID)
	if strings.EqualFold(row.Platform, internalchat.Platform) && userID == internalchat.OwnerUserID {
		return memory.ConversationKindPrivate, internalchat.OwnerNumericID, nil
	}
	parsed, err := strconv.ParseInt(userID, 10, 64)
	if err != nil || parsed <= 0 {
		return "", 0, fmt.Errorf("私聊目标无效")
	}
	return memory.ConversationKindPrivate, parsed, nil
}

func acpComputerStatus(service *computer.Service) map[string]any {
	if service == nil {
		return map[string]any{"enabled": false}
	}
	raw, err := sonic.Marshal(service.Status())
	if err != nil {
		return map[string]any{"enabled": service.Enabled()}
	}
	out := map[string]any{}
	if err := sonic.Unmarshal(raw, &out); err != nil {
		return map[string]any{"enabled": service.Enabled()}
	}
	return out
}

// directRemoteURLResolver 只接受适配器已经给出的直链；file_id 解析仍由平台适配器负责
func directRemoteURLResolver(_ context.Context, source map[string]any) (string, error) {
	for _, key := range []string{"url", "file_url", "download_url"} {
		if raw, ok := source[key].(string); ok && strings.TrimSpace(raw) != "" {
			return strings.TrimSpace(raw), nil
		}
	}
	return "", fmt.Errorf("适配器尚未实现文件解析能力")
}

// ingestInboundFiles 把入站消息里的文件组件登记到 filesvc，失败保留消息原文
func ingestInboundFiles(ctx context.Context, files *filesvc.Service, msg *onebot.ConversationMessage, sessionID string) error {
	if files == nil || msg == nil || len(msg.Files) == 0 {
		return nil
	}
	var source map[string]any
	if msg.AdditionalConfig != nil {
		if raw, ok := msg.AdditionalConfig["file_source"].(map[string]any); ok {
			source = raw
		}
	}
	groupID := ""
	if msg.ConversationKind == memory.ConversationKindGroup {
		groupID = strconv.FormatInt(msg.TargetID, 10)
	}
	messageID := strings.TrimSpace(msg.ExternalMessageID)
	if messageID == "" {
		messageID = strconv.FormatInt(msg.MessageID, 10)
	}
	for i := range msg.Files {
		file := &msg.Files[i]
		input := filesvc.RemoteFileInput{
			SessionID: sessionID,
			MessageID: messageID,
			GroupID:   groupID,
			UserID:    strconv.FormatInt(msg.UserID, 10),
			FileRef:   file.FileRef,
			Name:      file.Name,
			MimeType:  file.MimeType,
			Size:      file.Size,
			Source:    source,
		}
		if source != nil {
			input.FileID = anyString(source["file_id"])
			input.Busid = anyInt(source["busid"])
		}
		asset, handled, err := files.IngestFile(ctx, input)
		if err != nil {
			return err
		}
		if !handled {
			continue
		}
		if asset.FileRef != "" {
			file.FileRef = asset.FileRef
		}
		if file.Name == "" {
			file.Name = asset.Name
		}
		if file.MimeType == "" {
			file.MimeType = asset.MimeType
		}
		if file.Size <= 0 {
			file.Size = asset.Size
		}
	}
	return nil
}

// sendPlatformFile 通过 Platform IO 发送文件段；没有成功回执时返回真实失败原因
func sendPlatformFile(ctx context.Context, mgr *platformio.Manager, platform, conversationKind string, targetID int64, fileURL, name string) (int64, uint64, error) {
	if mgr == nil {
		return 0, 0, fmt.Errorf("Platform IO 未就绪")
	}
	chatType := platformio.ChatTypePrivate
	if conversationKind == memory.ConversationKindGroup {
		chatType = platformio.ChatTypeGroup
	}
	extra := map[string]string{}
	if strings.TrimSpace(name) != "" {
		extra["name"] = strings.TrimSpace(name)
	}
	batch := mgr.SendMessage(ctx, platformio.OutboundMessageEnvelope{
		ChatType: chatType,
		TargetID: strconv.FormatInt(targetID, 10),
		Segments: []platformio.Segment{{
			Kind:  platformio.SegmentFile,
			URL:   fileURL,
			Extra: extra,
		}},
	}, platformio.RouteKey{Platform: platform})
	sent := batch.SentReceipts()
	if len(sent) == 0 {
		reasons := make([]string, 0, len(batch.Receipts))
		for _, receipt := range batch.Receipts {
			if strings.TrimSpace(receipt.Error) != "" {
				reasons = append(reasons, receipt.Error)
			}
		}
		if len(reasons) == 0 {
			reasons = append(reasons, "没有可用的平台发送适配器")
		}
		return 0, 0, fmt.Errorf("平台文件发送失败: %s", strings.Join(reasons, "; "))
	}
	messageID, _ := strconv.ParseInt(strings.TrimSpace(sent[0].ExternalMessageID), 10, 64)
	return messageID, 0, nil
}

func anyString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func anyInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(typed))
		return parsed
	default:
		return 0
	}
}

// resolveRelayImages 解析传话附图，返回媒体引用与附件清单
//
// 对齐 Python agent/images.py resolve_images：图片来源为当前会话图片索引，
// 落盘到工作区 relay_images，附件清单随传话文本一起交给对方。
func (h *acpAgentHost) resolveRelayImages(ctx context.Context, references []string) ([]media.Ref, []map[string]string, error) {
	if len(references) == 0 {
		return nil, nil, nil
	}
	if h == nil || h.agent == nil {
		return nil, nil, fmt.Errorf("Agent 未就绪，无法解析附图")
	}
	// Agent 自身传话：available={}（对齐 Python caller == agent_id），只用 screenshot/工作区 hash
	computerWS := ""
	var requireEnabled func() error
	if h.computer != nil {
		computerWS = h.computer.Workspace()
		requireEnabled = h.computer.RequireEnabled
	}
	resolved, err := agent.ResolveRelayImages(ctx, &acpImageResolver{
		source:    func() map[string][]media.Ref { return map[string][]media.Ref{} },
		workspace: h.workspace,
		screenshot: func(shotCtx context.Context) ([]byte, error) {
			return h.captureScreenshot(shotCtx)
		},
		computerWS:     computerWS,
		requireEnabled: requireEnabled,
	}, references)
	if err != nil {
		return nil, nil, err
	}
	refs := make([]media.Ref, 0, len(resolved))
	attachments := make([]map[string]string, 0, len(resolved))
	for _, image := range resolved {
		refs = append(refs, media.Ref{Hash: image.Hash, Path: image.Path})
		attachments = append(attachments, map[string]string{"hash": image.Hash, "path": image.Path})
	}
	return refs, attachments, nil
}

// acpImageResolver 把宿主能力适配成 agent.ImageResolver
type acpImageResolver struct {
	source         func() map[string][]media.Ref
	workspace      string
	screenshot     func(context.Context) ([]byte, error)
	computerWS     string
	requireEnabled func() error
}

func (r *acpImageResolver) SourceImages() map[string][]media.Ref {
	if r == nil || r.source == nil {
		return nil
	}
	return r.source()
}

func (r *acpImageResolver) Workspace() string {
	if r == nil {
		return ""
	}
	return r.workspace
}

func (r *acpImageResolver) Screenshot(ctx context.Context) ([]byte, error) {
	if r == nil || r.screenshot == nil {
		return nil, fmt.Errorf("当前宿主不支持截图引用")
	}
	return r.screenshot(ctx)
}

func (r *acpImageResolver) AllowAgentOnlyRefs() bool { return true }
func (r *acpImageResolver) ResolveWorkspacePath(reference string) (string, error) {
	if r == nil {
		return "", nil
	}
	return agent.ResolveAgentImagePath(reference, r.workspace, r.computerWS, r.requireEnabled)
}

// captureScreenshot 对齐 Python resolve_images 的 screenshot 分支：computer.observe() → base64 image
func (h *acpAgentHost) captureScreenshot(ctx context.Context) ([]byte, error) {
	if h == nil || h.computer == nil {
		return nil, fmt.Errorf("当前宿主不支持截图引用")
	}
	frame, err := h.computer.Observe(ctx, nil)
	if err != nil {
		return nil, err
	}
	image, _ := frame["image"].(string)
	if strings.TrimSpace(image) == "" {
		return nil, fmt.Errorf("电脑没有返回截图")
	}
	data, err := base64.StdEncoding.DecodeString(image)
	if err != nil {
		return nil, fmt.Errorf("电脑截图解码失败: %w", err)
	}
	return data, nil
}

// reportACPFailure 对齐 messaging.report_failure：用 agent_acp_failure 提示词向原聊天投递内部任务
func reportACPFailure(maiAgent *maisaruntime.Agent, mem *memory.Manager, render func(string, map[string]string) (string, error), agentName, target string, taskIDs []string, errMsg string) {
	if maiAgent == nil || mem == nil || render == nil {
		return
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return
	}
	var row chatsession.Record
	if err := mem.GetDB().WithContext(context.Background()).Where("session_id = ?", target).First(&row).Error; err != nil {
		zap.L().Warn("Agent 失败通知找不到聊天流", zap.String("target", target), zap.Error(err))
		return
	}
	kind, targetID, err := chatTargetFromRecord(&row)
	if err != nil {
		zap.L().Warn("Agent 失败通知目标无效", zap.String("target", target), zap.Error(err))
		return
	}
	if strings.TrimSpace(agentName) == "" {
		agentName = "麦麦的 Agent"
	}
	text, err := render("agent_acp_failure", map[string]string{
		"agent_name": agentName,
		"task_ids":   strings.Join(taskIDs, ", "),
		"error":      errMsg,
	})
	if err != nil {
		zap.L().Warn("Agent 失败通知渲染失败", zap.Error(err))
		return
	}
	if _, err := maiAgent.EnqueueInternalTask(context.Background(), kind, targetID, text, "agent_relay", agentName+"执行失败"); err != nil {
		zap.L().Warn("Agent 失败通知未送达", zap.String("target", target), zap.Strings("tasks", taskIDs), zap.Error(err))
	}
}

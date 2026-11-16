package agentfiles

import (
	"context"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
)

// Image 是从结果中拆出的图片本体
type Image struct {
	Data     string
	MIMEType string
}

// Result 是一次工具调用的统一结果，主聊天与 ACP 共用
type Result struct {
	Content      string
	Structured   any
	Images       []Image
	Failed       bool
	ErrorMessage string
}

// Host 提供 agent_* 工具的真实宿主能力，由各运行入口按会话装配
type Host interface {
	TellAgent(ctx context.Context, text, target string, images []string) (map[string]any, error)
	AgentStatus(ctx context.Context) (map[string]any, []Image, error)
	AgentChats(ctx context.Context) ([]map[string]any, error)
	AgentRecent(ctx context.Context, target string, limit int) (map[string]any, error)
}

// Invoke 按工具名执行并返回统一结果
func Invoke(ctx context.Context, name string, args map[string]any, host Host) Result {
	if host == nil {
		return failure("工具宿主未就绪")
	}
	if args == nil {
		args = map[string]any{}
	}
	switch name {
	case "agent_talk":
		return invokeAgentTalk(ctx, args, host)
	case "agent_status":
		return invokeAgentStatus(ctx, host)
	case "agent_chats":
		return invokeAgentChats(ctx, host)
	case "agent_recent":
		return invokeAgentRecent(ctx, args, host)
	default:
		return failure(fmt.Sprintf("未知工具: %s", name))
	}
}

func invokeAgentTalk(ctx context.Context, args map[string]any, host Host) Result {
	text := stringArg(args, "text")
	target := stringArg(args, "target")
	images, err := imageRefsArg(args)
	if err != nil {
		return failure(err.Error())
	}
	payload, err := host.TellAgent(ctx, text, target, images)
	if err != nil {
		return failure(err.Error())
	}
	return success(payload, nil)
}

func invokeAgentStatus(ctx context.Context, host Host) Result {
	payload, images, err := host.AgentStatus(ctx)
	if err != nil {
		return failure(err.Error())
	}
	return success(payload, images)
}

func invokeAgentChats(ctx context.Context, host Host) Result {
	chats, err := host.AgentChats(ctx)
	if err != nil {
		return failure(err.Error())
	}
	if chats == nil {
		chats = []map[string]any{}
	}
	return success(map[string]any{"chats": chats}, nil)
}

func invokeAgentRecent(ctx context.Context, args map[string]any, host Host) Result {
	target := stringArg(args, "target")
	limit := intArg(args, "limit", 20)
	payload, err := host.AgentRecent(ctx, target, limit)
	if err != nil {
		return failure(err.Error())
	}
	return success(payload, nil)
}

func success(payload any, images []Image) Result {
	encoded, err := sonic.MarshalString(payload)
	if err != nil {
		return failure(fmt.Sprintf("工具结果序列化失败: %v", err))
	}
	return Result{Content: encoded, Structured: payload, Images: images}
}

func failure(message string) Result {
	return Result{Failed: true, ErrorMessage: strings.TrimSpace(message)}
}

func imageRefsArg(args map[string]any) ([]string, error) {
	raw, ok := args["images"]
	if !ok || raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		if typed, typedOK := raw.([]string); typedOK {
			items = make([]any, 0, len(typed))
			for _, item := range typed {
				items = append(items, item)
			}
		} else {
			return nil, fmt.Errorf("images 必须是非空图片引用组成的列表")
		}
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		value, ok := item.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("images 必须是非空图片引用组成的列表")
		}
		out = append(out, value)
	}
	return out, nil
}

// StatusPayload 把 Agent 状态整理成 Python service.status() 口径并拆出可选截图
func StatusPayload(source map[string]any) (map[string]any, []Image) {
	if source == nil {
		return map[string]any{}, nil
	}
	payload := make(map[string]any, len(source))
	for key, value := range source {
		if key == "acp_connected" {
			continue
		}
		payload[key] = value
	}
	image, _ := payload["image"].(string)
	mimeType, _ := payload["mime_type"].(string)
	if image == "" {
		return payload, nil
	}
	delete(payload, "image")
	delete(payload, "mime_type")
	return payload, []Image{{Data: image, MIMEType: mimeType}}
}

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func stringArgDefault(args map[string]any, key, fallback string) string {
	value, ok := args[key].(string)
	if !ok || value == "" {
		return fallback
	}
	return value
}

func intArg(args map[string]any, key string, fallback int) int {
	switch value := args[key].(type) {
	case float64:
		return int(value)
	case float32:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	case int32:
		return int(value)
	default:
		return fallback
	}
}

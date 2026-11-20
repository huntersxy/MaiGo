package snowluma

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// ActionService OneBot 动作封装，对齐 Python QQActionService
type ActionService struct {
	transport *Transport
	profile   *ProfileState
}

func NewActionService(transport *Transport, profile *ProfileState) *ActionService {
	return &ActionService{transport: transport, profile: profile}
}

func (s *ActionService) Call(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	if s == nil || s.transport == nil {
		return nil, ErrNotConnected
	}
	if params == nil {
		params = map[string]any{}
	}
	normalized := make(map[string]any, len(params))
	for key, value := range params {
		normalized[key] = value
	}
	if s.profile != nil && s.profile.Profile().ClientType == OneBotV11Profile.ClientType {
		for _, key := range []string{"user_id", "group_id", "message_id"} {
			raw, ok := normalized[key]
			if !ok {
				continue
			}
			n, err := coerceInt(raw)
			if err != nil {
				return nil, fmt.Errorf("%s.%s 必须是整数 ID", action, key)
			}
			normalized[key] = n
		}
	}
	response, err := s.transport.CallAction(ctx, action, normalized)
	if err != nil {
		return nil, fmt.Errorf("对端动作执行失败: action=%s error=%w", action, err)
	}
	if strings.ToLower(asString(response["status"])) != "ok" || asInt(response["retcode"], 0) != 0 {
		message := firstNonEmpty(asString(response["wording"]), asString(response["message"]), "unknown")
		return nil, fmt.Errorf("对端动作返回失败: action=%s message=%s", action, message)
	}
	if err := s.confirmGroupDelivery(ctx, action, normalized, response); err != nil {
		return nil, err
	}
	return response, nil
}

func (s *ActionService) CallData(ctx context.Context, action string, params map[string]any) (any, error) {
	response, err := s.Call(ctx, action, params)
	if err != nil {
		return nil, err
	}
	return response["data"], nil
}

func (s *ActionService) SafeCallData(ctx context.Context, action string, params map[string]any) any {
	data, err := s.CallData(ctx, action, params)
	if err != nil {
		return nil
	}
	return data
}

func (s *ActionService) confirmGroupDelivery(ctx context.Context, action string, params map[string]any, response map[string]any) error {
	if s == nil || s.profile == nil || s.profile.Profile().ClientType != SnowLumaProfile.ClientType {
		return nil
	}
	isGroupSend := action == "send_group_msg" || action == "send_group_forward_msg" ||
		((action == "send_msg" || action == "send_forward_msg") && params["group_id"] != nil && asString(params["group_id"]) != "")
	if !isGroupSend {
		return nil
	}
	data := asMap(response["data"])
	if data == nil {
		return fmt.Errorf("群消息投递未确认: action=%s 对端未返回有效消息 ID", action)
	}
	messageID := asString(data["message_id"])
	if messageID == "" || messageID == "0" {
		return fmt.Errorf("群消息投递未确认: action=%s 对端未返回有效消息 ID", action)
	}
	if n, err := strconv.ParseInt(strings.TrimPrefix(messageID, "-"), 10, 64); err != nil || n == 0 {
		return fmt.Errorf("群消息投递未确认: action=%s 对端未返回有效消息 ID", action)
	}
	detailResponse, err := s.transport.CallAction(ctx, "get_msg", map[string]any{"message_id": messageID})
	if err != nil {
		return fmt.Errorf("群消息投递未确认: action=%s message_id=%s 查询回执失败: %w", action, messageID, err)
	}
	detail := asMap(detailResponse["data"])
	sequence := asInt(detail["message_seq"], 0)
	if strings.ToLower(asString(detailResponse["status"])) != "ok" ||
		asInt(detailResponse["retcode"], 0) != 0 ||
		detail == nil ||
		asString(detail["message_type"]) != "group" ||
		asString(detail["group_id"]) != asString(params["group_id"]) ||
		asString(detail["message_id"]) != messageID ||
		sequence <= 0 {
		return fmt.Errorf("群消息投递未确认: action=%s message_id=%s 未取得对应群消息的真实序号", action, messageID)
	}
	return nil
}

func coerceInt(value any) (int64, error) {
	switch typed := value.(type) {
	case bool:
		return 0, fmt.Errorf("invalid bool")
	case int:
		return int64(typed), nil
	case int64:
		return typed, nil
	case float64:
		return int64(typed), nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, fmt.Errorf("empty")
		}
		return strconv.ParseInt(trimmed, 10, 64)
	default:
		return 0, fmt.Errorf("unsupported")
	}
}

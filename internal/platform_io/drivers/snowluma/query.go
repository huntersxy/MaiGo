package snowluma

import (
	"context"
	"errors"
	"fmt"
)

var ErrNotConnected = errors.New("对端未连接")

// QueryService 透传查询，对齐 Python QQQueryService
type QueryService struct {
	actions *ActionService
}

func NewQueryService(actions *ActionService) *QueryService {
	return &QueryService{actions: actions}
}

func (s *QueryService) require() (*ActionService, error) {
	if s == nil || s.actions == nil {
		return nil, ErrNotConnected
	}
	return s.actions, nil
}

func (s *QueryService) Call(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	actions, err := s.require()
	if err != nil {
		return nil, err
	}
	return actions.Call(ctx, action, params)
}

func (s *QueryService) CallData(ctx context.Context, action string, params map[string]any) (any, error) {
	actions, err := s.require()
	if err != nil {
		return nil, err
	}
	return actions.CallData(ctx, action, params)
}

func (s *QueryService) GetLoginInfo(ctx context.Context) (map[string]any, error) {
	actions, err := s.require()
	if err != nil {
		return nil, err
	}
	data := actions.SafeCallData(ctx, "get_login_info", map[string]any{})
	return asMap(data), nil
}

func (s *QueryService) GetImage(ctx context.Context, file string) (map[string]any, error) {
	data, err := s.CallData(ctx, "get_image", map[string]any{"file": file})
	return asMap(data), err
}

func (s *QueryService) GetStrangerInfo(ctx context.Context, userID string, noCache bool) (map[string]any, error) {
	actions, err := s.require()
	if err != nil {
		return nil, err
	}
	data := actions.SafeCallData(ctx, "get_stranger_info", map[string]any{
		"user_id":  userID,
		"no_cache": noCache,
	})
	return asMap(data), nil
}

func (s *QueryService) GetFriendList(ctx context.Context, noCache bool) ([]map[string]any, error) {
	actions, err := s.require()
	if err != nil {
		return nil, err
	}
	data := actions.SafeCallData(ctx, "get_friend_list", map[string]any{"no_cache": noCache})
	return normalizePayloadList(data), nil
}

func (s *QueryService) GetGroupInfo(ctx context.Context, groupID string, noCache bool) (map[string]any, error) {
	actions, err := s.require()
	if err != nil {
		return nil, err
	}
	data := actions.SafeCallData(ctx, "get_group_info", map[string]any{
		"group_id": groupID,
		"no_cache": noCache,
	})
	return asMap(data), nil
}

func (s *QueryService) GetGroupDetailInfo(ctx context.Context, groupID string) (map[string]any, error) {
	actions, err := s.require()
	if err != nil {
		return nil, err
	}
	data := actions.SafeCallData(ctx, "get_group_detail_info", map[string]any{"group_id": groupID})
	return asMap(data), nil
}

func (s *QueryService) GetGroupList(ctx context.Context, noCache bool) ([]map[string]any, error) {
	actions, err := s.require()
	if err != nil {
		return nil, err
	}
	data := actions.SafeCallData(ctx, "get_group_list", map[string]any{"no_cache": noCache})
	return normalizePayloadList(data), nil
}

func (s *QueryService) GetGroupAtAllRemain(ctx context.Context, groupID string) (map[string]any, error) {
	actions, err := s.require()
	if err != nil {
		return nil, err
	}
	data := actions.SafeCallData(ctx, "get_group_at_all_remain", map[string]any{"group_id": groupID})
	return asMap(data), nil
}

func (s *QueryService) GetGroupMemberInfo(ctx context.Context, groupID, userID string, noCache bool) (map[string]any, error) {
	actions, err := s.require()
	if err != nil {
		return nil, err
	}
	data := actions.SafeCallData(ctx, "get_group_member_info", map[string]any{
		"group_id": groupID,
		"user_id":  userID,
		"no_cache": noCache,
	})
	return asMap(data), nil
}

func (s *QueryService) GetGroupMemberList(ctx context.Context, groupID string, noCache bool) ([]map[string]any, error) {
	actions, err := s.require()
	if err != nil {
		return nil, err
	}
	data := actions.SafeCallData(ctx, "get_group_member_list", map[string]any{
		"group_id": groupID,
		"no_cache": noCache,
	})
	return normalizePayloadList(data), nil
}

func (s *QueryService) GetMessageDetail(ctx context.Context, messageID string) (map[string]any, error) {
	actions, err := s.require()
	if err != nil {
		return nil, err
	}
	data := actions.SafeCallData(ctx, "get_msg", map[string]any{"message_id": messageID})
	return asMap(data), nil
}

func (s *QueryService) GetForwardMessage(ctx context.Context, messageID, forwardID string) (map[string]any, error) {
	actions, err := s.require()
	if err != nil {
		return nil, err
	}
	params := map[string]any{}
	if messageID != "" {
		params["message_id"] = messageID
	}
	if forwardID != "" {
		params["id"] = forwardID
	}
	if len(params) == 0 {
		return nil, fmt.Errorf("message_id 或 id 至少提供一个")
	}
	data := actions.SafeCallData(ctx, "get_forward_msg", params)
	return normalizeForwardPayload(data), nil
}

func (s *QueryService) GetRecordDetail(ctx context.Context, fileName, fileID, outFormat string) (map[string]any, error) {
	actions, err := s.require()
	if err != nil {
		return nil, err
	}
	params := map[string]any{}
	if fileName != "" {
		params["file"] = fileName
	}
	if fileID != "" {
		params["file_id"] = fileID
	}
	if outFormat != "" {
		params["out_format"] = outFormat
	}
	if params["file"] == nil && params["file_id"] == nil {
		return nil, fmt.Errorf("file 或 file_id 至少提供一个")
	}
	data := actions.SafeCallData(ctx, "get_record", params)
	return asMap(data), nil
}

func (s *QueryService) SetGroupBan(ctx context.Context, groupID, userID int64, duration int64) (map[string]any, error) {
	return s.Call(ctx, "set_group_ban", map[string]any{
		"group_id": groupID,
		"user_id":  userID,
		"duration": duration,
	})
}

func (s *QueryService) SetGroupWholeBan(ctx context.Context, groupID int64, enable bool) (map[string]any, error) {
	return s.Call(ctx, "set_group_whole_ban", map[string]any{
		"group_id": groupID,
		"enable":   enable,
	})
}

func (s *QueryService) SetGroupKick(ctx context.Context, groupID, userID int64, rejectAddRequest bool) (map[string]any, error) {
	return s.Call(ctx, "set_group_kick", map[string]any{
		"group_id":           groupID,
		"user_id":            userID,
		"reject_add_request": rejectAddRequest,
	})
}

func (s *QueryService) SetGroupKickMembers(ctx context.Context, groupID int64, userIDs []int64, rejectAddRequest bool) (map[string]any, error) {
	return s.Call(ctx, "set_group_kick_members", map[string]any{
		"group_id":           groupID,
		"user_id":            userIDs,
		"reject_add_request": rejectAddRequest,
	})
}

func (s *QueryService) SendPoke(ctx context.Context, userID int64, groupID, targetID *int64) (map[string]any, error) {
	params := map[string]any{"user_id": userID}
	if groupID != nil {
		params["group_id"] = *groupID
	}
	if targetID != nil {
		params["target_id"] = *targetID
	}
	return s.Call(ctx, "send_poke", params)
}

func (s *QueryService) DeleteMessage(ctx context.Context, messageID int64) (map[string]any, error) {
	return s.Call(ctx, "delete_msg", map[string]any{"message_id": messageID})
}

func (s *QueryService) SendGroupAIRecord(ctx context.Context, groupID int64, character, text string) (map[string]any, error) {
	return s.Call(ctx, "send_group_ai_record", map[string]any{
		"group_id":  groupID,
		"character": character,
		"text":      text,
	})
}

func (s *QueryService) SetMessageEmojiLike(ctx context.Context, messageID, emojiID int64, setLike bool) (map[string]any, error) {
	return s.Call(ctx, "set_msg_emoji_like", map[string]any{
		"message_id": messageID,
		"emoji_id":   emojiID,
		"set":        setLike,
	})
}

func (s *QueryService) SetGroupName(ctx context.Context, groupID int64, groupName string) (map[string]any, error) {
	return s.Call(ctx, "set_group_name", map[string]any{
		"group_id":   groupID,
		"group_name": groupName,
	})
}

func (s *QueryService) SetQQProfile(ctx context.Context, nickname, personalNote, sex string) (map[string]any, error) {
	params := map[string]any{"nickname": nickname}
	if personalNote != "" {
		params["personal_note"] = personalNote
	}
	if sex != "" {
		params["sex"] = sex
	}
	return s.Call(ctx, "set_qq_profile", params)
}

func normalizePayloadList(responseData any) []map[string]any {
	switch typed := responseData.(type) {
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if m := asMap(item); m != nil {
				out = append(out, m)
			}
		}
		return out
	case []map[string]any:
		return typed
	case map[string]any:
		for _, key := range []string{"list", "items", "members", "member_list", "group_list", "friend_list", "friends", "records", "rows", "data"} {
			if candidate, ok := typed[key]; ok {
				if list := normalizePayloadList(candidate); list != nil {
					return list
				}
			}
		}
		for _, candidate := range typed {
			if list := normalizePayloadList(candidate); list != nil {
				return list
			}
		}
	}
	return nil
}

func normalizeForwardPayload(responseData any) map[string]any {
	switch typed := responseData.(type) {
	case []any:
		messages := make([]any, 0, len(typed))
		for _, item := range typed {
			if m := asMap(item); m != nil {
				messages = append(messages, m)
			}
		}
		return map[string]any{"messages": messages}
	case map[string]any:
		if messages, ok := typed["messages"].([]any); ok {
			_ = messages
			return typed
		}
		if content, ok := typed["content"].([]any); ok {
			messages := make([]any, 0, len(content))
			for _, item := range content {
				if m := asMap(item); m != nil {
					messages = append(messages, m)
				}
			}
			return map[string]any{"messages": messages}
		}
		if nested := asMap(typed["data"]); nested != nil {
			if messages, ok := nested["messages"].([]any); ok {
				out := make([]any, 0, len(messages))
				for _, item := range messages {
					if m := asMap(item); m != nil {
						out = append(out, m)
					}
				}
				return map[string]any{"messages": out}
			}
			if content, ok := nested["content"].([]any); ok {
				out := make([]any, 0, len(content))
				for _, item := range content {
					if m := asMap(item); m != nil {
						out = append(out, m)
					}
				}
				return map[string]any{"messages": out}
			}
		}
	}
	return nil
}

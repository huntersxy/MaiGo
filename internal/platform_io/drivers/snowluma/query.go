package snowluma

import (
	"context"
	"errors"
)

var ErrNotConnected = errors.New("对端未连接")

// QueryService 透传查询骨架（P0 stub：群员/好友等）
type QueryService struct {
	actions *ActionService
}

func NewQueryService(actions *ActionService) *QueryService {
	return &QueryService{actions: actions}
}

func (s *QueryService) GetGroupMemberInfo(ctx context.Context, groupID, userID string) (map[string]any, error) {
	if s == nil || s.actions == nil {
		return nil, ErrNotConnected
	}
	return s.actions.Call(ctx, "get_group_member_info", map[string]any{
		"group_id": groupID,
		"user_id":  userID,
	})
}

func (s *QueryService) GetStrangerInfo(ctx context.Context, userID string) (map[string]any, error) {
	if s == nil || s.actions == nil {
		return nil, ErrNotConnected
	}
	return s.actions.Call(ctx, "get_stranger_info", map[string]any{"user_id": userID})
}

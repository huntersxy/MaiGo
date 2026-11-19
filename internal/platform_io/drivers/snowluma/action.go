package snowluma

import "context"

// ActionService OneBot 动作封装骨架（P0 stub）
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
	return s.transport.CallAction(ctx, action, params)
}

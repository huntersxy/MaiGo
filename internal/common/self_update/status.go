package selfupdate

import (
	"context"
)

// Status 读取缓存状态，不联网
func (m *Manager) Status(ctx context.Context, opts CheckOptions) (Status, error) {
	check, err := m.Peek(ctx, opts)
	if err != nil {
		return Status{}, err
	}
	last, err := ReadLastResult(opts.Root)
	if err != nil {
		return Status{}, err
	}
	return BuildStatus(check, last), nil
}

// ForceCheck 联网检查并返回 WebUI 状态
func (m *Manager) ForceCheck(ctx context.Context, opts CheckOptions) (Status, error) {
	opts.Force = true
	check, err := m.Check(ctx, opts)
	if err != nil {
		return Status{}, err
	}
	last, err := ReadLastResult(opts.Root)
	if err != nil {
		return Status{}, err
	}
	return BuildStatus(check, last), nil
}

// CurrentStatus 使用全局缓存读取状态
func CurrentStatus(ctx context.Context, opts CheckOptions) (Status, error) {
	return DefaultManager().Status(ctx, opts)
}

// CheckStatus 使用全局缓存联网检查
func CheckStatus(ctx context.Context, opts CheckOptions) (Status, error) {
	return DefaultManager().ForceCheck(ctx, opts)
}

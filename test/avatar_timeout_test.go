package test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"MaiGo/internal/platform_io"
)

type delayedAvatarProvider struct {
	started chan bool
	release chan struct{}
	calls   atomic.Int32
}

func (p *delayedAvatarProvider) QueryAvatar(ctx context.Context, _, _, _, _, _ string) (map[string]any, error) {
	p.calls.Add(1)
	_, hasDeadline := ctx.Deadline()
	p.started <- hasDeadline
	select {
	case <-p.release:
		return map[string]any{"status": "missing"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestDashboardAvatarTimeoutKeepsSharedRefreshRunning(t *testing.T) {
	root := t.TempDir()
	service := platformio.NewAvatarService(root)
	provider := &delayedAvatarProvider{
		started: make(chan bool, 2),
		release: make(chan struct{}),
	}
	service.Provider = provider
	deps := newTestDeps(root)
	deps.Avatar = service
	router := setupTestRouter(deps)

	const path = "/api/webui/avatar?platform=qq&user_id=123"
	response := serve(router, authenticatedRequest(http.MethodGet, path, ""))
	if response.Code != http.StatusNoContent || response.Header().Get("X-Avatar-Status") != "missing" ||
		response.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("expected temporary avatar fallback, got status=%d headers=%v", response.Code, response.Header())
	}
	select {
	case hasDeadline := <-provider.started:
		if !hasDeadline {
			t.Fatal("background avatar lookup must have its own deadline")
		}
	default:
		t.Fatal("avatar provider was not called")
	}

	close(provider.release)
	result, err := service.GetAvatar(t.Context(), "qq", "123", "user", "", "", false)
	if err != nil || result.Status != platformio.AvatarStatusMissing {
		t.Fatalf("expected result from background refresh, got %+v, %v", result, err)
	}
	if got := provider.calls.Load(); got != 1 {
		t.Fatalf("expected one shared lookup, got %d", got)
	}
}

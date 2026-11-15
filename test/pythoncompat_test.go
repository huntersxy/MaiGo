package test

import (
	"context"
	"errors"
	"testing"

	"maibot-go/internal/services/pythoncompat"
)

func TestPythonCompatUnconfiguredReturnsSentinel(t *testing.T) {
	client := pythoncompat.NewClient(pythoncompat.Config{})
	if client.Configured() {
		t.Fatal("empty config should not be considered configured")
	}
	if _, err := client.CreateTypoSentences(context.Background(), []string{"测试"}, pythoncompat.TypoConfig{}, nil); !errors.Is(err, pythoncompat.ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
	if _, err := client.RunKMeans(context.Background(), pythoncompat.KMeansRequest{Rows: 1, Cols: 1, Vectors: []float32{1}, ClusterCount: 1}); !errors.Is(err, pythoncompat.ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}

func TestPythonCompatEmptySentencesNeedsNoSidecar(t *testing.T) {
	client := pythoncompat.NewClient(pythoncompat.Config{})
	results, err := client.CreateTypoSentences(context.Background(), nil, pythoncompat.TypoConfig{}, nil)
	if err != nil {
		t.Fatalf("empty input should not call the sidecar: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results, got %d", len(results))
	}
}

func TestPythonCompatTrimsTrailingSlash(t *testing.T) {
	client := pythoncompat.NewClient(pythoncompat.Config{BaseURL: "http://sidecar:8767/"})
	if got := client.BaseURL(); got != "http://sidecar:8767" {
		t.Fatalf("unexpected base url: %q", got)
	}
	if !client.Configured() {
		t.Fatal("client with base url should be configured")
	}
}

func TestPythonCompatLoadConfigFromEnv(t *testing.T) {
	t.Setenv("MAIBOT_COMPAT_URL", "http://sidecar:8767")
	t.Setenv("MAIBOT_COMPAT_TOKEN", "secret")
	t.Setenv("MAIBOT_COMPAT_TIMEOUT", "45")
	cfg := pythoncompat.LoadConfigFromEnv()
	if cfg.BaseURL != "http://sidecar:8767" || cfg.Token != "secret" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.Timeout.Seconds() != 45 {
		t.Fatalf("unexpected timeout: %v", cfg.Timeout)
	}
}

func TestPythonCompatIgnoresInvalidTimeout(t *testing.T) {
	t.Setenv("MAIBOT_COMPAT_URL", "http://sidecar:8767")
	t.Setenv("MAIBOT_COMPAT_TIMEOUT", "-5")
	cfg := pythoncompat.LoadConfigFromEnv()
	if cfg.Timeout <= 0 {
		t.Fatalf("invalid timeout should fall back to a positive default: %v", cfg.Timeout)
	}
}

package websearch

import (
	"context"
	"errors"
	"testing"
)

func TestParseRuntimeTOMLAllowsInvalidSelection(t *testing.T) {
	raw := []byte(`
[web]
preview_chars = 4000

[web.services.configured]
adapter = "tavily"
api_key = ""

[model_task_config.web_search]
model_list = ["missing"]
`)
	cfg, err := ParseRuntimeTOML(raw)
	if err != nil {
		t.Fatalf("缺少服务不应阻止启动: %v", err)
	}
	if !cfg.Enabled || cfg.SearchService != "missing" || cfg.FetchService != "missing" {
		t.Fatalf("选中服务未保留: %+v", cfg)
	}
	if _, err := NewService(cfg).Search(context.Background(), "test", 5); err == nil {
		t.Fatal("调用应返回配置错误")
	} else {
		var configErr *ConfigError
		if !errors.As(err, &configErr) || configErr.ErrorCode() != CodeWebServiceConfigInvalid {
			t.Fatalf("错误类型不正确: %v", err)
		}
	}
}

func TestParseRuntimeTOMLAllowsMissingAPIKey(t *testing.T) {
	raw := []byte(`
[web]
preview_chars = 4000

[web.services.configured]
adapter = "tavily"
api_key = ""

[model_task_config.web_search]
model_list = ["configured"]
`)
	cfg, err := ParseRuntimeTOML(raw)
	if err != nil {
		t.Fatalf("缺少 api_key 不应阻止启动: %v", err)
	}
	if !cfg.Enabled || cfg.SearchService != "configured" {
		t.Fatalf("选中服务未保留: %+v", cfg)
	}
	if _, err := NewService(cfg).Search(context.Background(), "test", 5); err == nil {
		t.Fatal("调用应返回配置错误")
	} else {
		var configErr *ConfigError
		if !errors.As(err, &configErr) || configErr.ErrorCode() != CodeWebServiceConfigInvalid {
			t.Fatalf("错误类型不正确: %v", err)
		}
	}
}

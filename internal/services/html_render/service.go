package htmlrender

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Status HTMLRenderService 运行态摘要
type Status struct {
	Implemented       bool   `json:"implemented"`
	Mode              string `json:"mode,omitempty"`
	SidecarURL        string `json:"sidecar_url,omitempty"`
	BrowserConnected  bool   `json:"browser_connected"`
	BrowsersPath      string `json:"browsers_path,omitempty"`
	PlaywrightVersion string `json:"playwright_version,omitempty"`
	LastError         string `json:"error,omitempty"`
}

// Service HTML 渲染门面：只调用 Python 原版 Playwright 侧车。
type Service struct {
	cfg Config

	mu      sync.RWMutex
	lastErr string

	sem chan struct{}
}

// NewService 创建 HTML 渲染服务
func NewService(cfg Config) *Service {
	cfg = normalizeConfig(cfg)
	return &Service{
		cfg: cfg,
		sem: make(chan struct{}, cfg.ConcurrencyLimit),
	}
}

// Status 返回当前实现状态
func (s *Service) Status() Status {
	s.mu.RLock()
	lastErr := s.lastErr
	s.mu.RUnlock()

	status := Status{
		Implemented:       true,
		Mode:              "sidecar_http",
		SidecarURL:        s.cfg.SidecarURL,
		PlaywrightVersion: "python-sidecar",
		LastError:         lastErr,
	}
	if s.cfg.Disabled {
		status.Implemented = false
		status.Mode = "disabled"
		return status
	}
	if strings.EqualFold(strings.TrimSpace(s.cfg.Mode), "browser") {
		status.Mode = "unsupported_browser"
		status.LastError = "Go CDP 浏览器后端已移除，请配置 Python 渲染侧车"
		return status
	}
	if strings.TrimSpace(s.cfg.SidecarURL) == "" {
		status.Mode = "unconfigured"
		status.LastError = "未配置 MAIBOT_HTMLRENDER_URL Python 渲染侧车"
	}
	return status
}

// RenderHTMLToPNG 对齐 render_html_to_png，统一委托 Python Playwright 侧车。
func (s *Service) RenderHTMLToPNG(ctx context.Context, req Request) (*Result, error) {
	if s.cfg.Disabled {
		err := fmt.Errorf("插件运行时浏览器渲染能力已禁用")
		s.setLastErr(err.Error())
		return nil, err
	}
	if strings.TrimSpace(req.HTML) == "" {
		err := fmt.Errorf("缺少必要参数 html")
		s.setLastErr(err.Error())
		return nil, err
	}
	if strings.EqualFold(strings.TrimSpace(s.cfg.Mode), "browser") {
		err := fmt.Errorf("Go CDP 浏览器后端已移除，请配置 Python 渲染侧车")
		s.setLastErr(err.Error())
		return nil, err
	}
	if strings.TrimSpace(s.cfg.SidecarURL) == "" {
		s.setLastErr(ErrNotConfigured.Error())
		return nil, ErrNotConfigured
	}

	normalized := s.normalizeRequest(req)
	if err := s.acquire(ctx); err != nil {
		s.setLastErr(err.Error())
		return nil, err
	}
	defer s.release()

	result, err := s.renderViaSidecar(ctx, normalized)
	if err != nil {
		s.setLastErr(err.Error())
		return nil, err
	}
	s.setLastErr("")
	return result, nil
}

// Close 释放 Go 侧资源；浏览器由 Python 侧车自己管理。
func (s *Service) Close() error {
	return nil
}

func (s *Service) normalizeRequest(req Request) Request {
	req.HTML = strings.TrimSpace(req.HTML)
	req.Selector = strings.TrimSpace(req.Selector)
	if req.Selector == "" {
		req.Selector = "body"
	}
	if req.ViewportWidth <= 0 {
		req.ViewportWidth = 900
	}
	if req.ViewportHeight <= 0 {
		req.ViewportHeight = 500
	}
	if req.DeviceScaleFactor <= 0 {
		req.DeviceScaleFactor = 2.0
	} else if req.DeviceScaleFactor < 1 {
		req.DeviceScaleFactor = 1
	}
	if req.TimeoutMs <= 0 {
		req.TimeoutMs = int(s.cfg.RenderTimeout / time.Millisecond)
	}
	if req.TimeoutMs < 1 {
		req.TimeoutMs = 1
	}
	req.WaitUntil = normalizeWaitUntil(req.WaitUntil)
	req.WaitForSelector = strings.TrimSpace(req.WaitForSelector)
	if req.WaitForTimeoutMs < 0 {
		req.WaitForTimeoutMs = 0
	}
	return req
}

func normalizeWaitUntil(waitUntil string) string {
	switch strings.ToLower(strings.TrimSpace(waitUntil)) {
	case "commit", "domcontentloaded", "load", "networkidle":
		return strings.ToLower(strings.TrimSpace(waitUntil))
	default:
		return "load"
	}
}

func (s *Service) setLastErr(msg string) {
	s.mu.Lock()
	s.lastErr = msg
	s.mu.Unlock()
}

func (s *Service) acquire(ctx context.Context) error {
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) release() {
	<-s.sem
}

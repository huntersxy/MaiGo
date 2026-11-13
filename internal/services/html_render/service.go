package htmlrender

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Status 对齐 HTMLRenderService 运行态摘要
type Status struct {
	Implemented       bool   `json:"implemented"`
	Mode              string `json:"mode,omitempty"`
	SidecarURL        string `json:"sidecar_url,omitempty"`
	BrowserConnected  bool   `json:"browser_connected"`
	BrowsersPath      string `json:"browsers_path,omitempty"`
	PlaywrightVersion string `json:"playwright_version,omitempty"`
	LastError         string `json:"error,omitempty"`
}

// Service 对齐 HTMLRenderService 门面
type Service struct {
	cfg Config

	mu      sync.RWMutex
	lastErr string

	browserMu sync.Mutex
	browser   *browserInstance
	sem       chan struct{}

	renderMu      sync.Mutex
	renderCount   int
	activeRenders int
	closed        bool
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
	s.browserMu.Lock()
	browser := s.browser
	s.browserMu.Unlock()

	st := Status{
		Implemented: true,
		Mode:        "chromium_cdp",
		LastError:   lastErr,
	}
	if s.cfg.Disabled {
		st.Implemented = false
		st.Mode = "disabled"
		return st
	}
	mode := strings.ToLower(strings.TrimSpace(s.cfg.Mode))
	if mode == "sidecar" {
		st.Mode = "sidecar_http"
		st.SidecarURL = s.cfg.SidecarURL
		if s.cfg.SidecarURL == "" && st.LastError == "" {
			st.LastError = "未配置 MAIBOT_HTMLRENDER_URL 渲染侧车"
		}
		return st
	}
	if s.cfg.SidecarURL != "" && mode != "browser" {
		st.Mode = "sidecar_http"
		st.SidecarURL = s.cfg.SidecarURL
	}
	if browser != nil && browser.alive() {
		st.BrowserConnected = true
		st.BrowsersPath = browser.execPath
		st.PlaywrightVersion = browser.version
	} else if path, err := findBrowserExecutable(s.cfg); err == nil {
		st.BrowsersPath = path
	}
	if st.Mode == "chromium_cdp" && st.BrowsersPath == "" && st.LastError == "" {
		st.LastError = "未找到可用的 Chrome/Chromium/Edge 浏览器"
	}
	return st
}

// RenderHTMLToPNG 对齐 render_html_to_png
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
	normalized := s.normalizeRequest(req)
	mode := strings.ToLower(strings.TrimSpace(s.cfg.Mode))
	if s.cfg.SidecarURL != "" && mode != "browser" {
		res, err := s.renderViaSidecar(ctx, normalized)
		if err != nil {
			s.setLastErr(err.Error())
			return nil, err
		}
		s.setLastErr("")
		return res, nil
	}
	if mode == "sidecar" {
		err := fmt.Errorf("未配置 MAIBOT_HTMLRENDER_URL 渲染侧车")
		s.setLastErr(err.Error())
		return nil, err
	}
	res, err := s.renderViaBrowser(ctx, normalized)
	if err != nil {
		s.setLastErr(err.Error())
		// 对齐 Python：浏览器渲染失败后关闭缓存实例，下一次重新启动
		_ = s.ResetBrowser(ctx, false)
		return nil, err
	}
	s.setLastErr("")
	return res, nil
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

// ResetBrowser 对齐 reset_browser；重启后下一次渲染会重新启动浏览器
func (s *Service) ResetBrowser(ctx context.Context, restartPlaywright bool) error {
	_ = ctx
	_ = restartPlaywright
	s.browserMu.Lock()
	browser := s.browser
	s.browser = nil
	s.browserMu.Unlock()
	if browser != nil {
		browser.close()
	}
	return nil
}

// Close 关闭浏览器进程并释放资源
func (s *Service) Close() error {
	s.renderMu.Lock()
	s.closed = true
	s.renderMu.Unlock()
	return s.ResetBrowser(context.Background(), false)
}

func (s *Service) setLastErr(msg string) {
	s.mu.Lock()
	s.lastErr = msg
	s.mu.Unlock()
}

func (s *Service) acquire(ctx context.Context) error {
	select {
	case s.sem <- struct{}{}:
		s.renderMu.Lock()
		if s.closed {
			s.renderMu.Unlock()
			<-s.sem
			return errors.New("htmlrender: 服务已关闭")
		}
		s.activeRenders++
		s.renderMu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) release() {
	s.renderMu.Lock()
	s.activeRenders--
	restart := s.closed || s.cfg.RestartAfterCount > 0 && s.renderCount > 0 && s.renderCount%s.cfg.RestartAfterCount == 0 && s.activeRenders == 0
	s.renderMu.Unlock()
	<-s.sem
	if restart && s.browserSupportsRestart() {
		_ = s.ResetBrowser(context.Background(), false)
	}
}

// browserSupportsRestart 只按次数重建本服务拉起的浏览器，外部 CDP 端点保持连接
func (s *Service) browserSupportsRestart() bool {
	s.browserMu.Lock()
	defer s.browserMu.Unlock()
	return s.browser != nil && s.browser.launched
}

// playwrightVersion 返回浏览器来源版本；CDP 直连模式没有 Playwright 运行时
func (s *Service) playwrightVersion() string {
	return "unknown"
}

func (s *Service) afterRender() {
	s.renderMu.Lock()
	s.renderCount++
	s.renderMu.Unlock()
}

package htmlrender

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

func (s *Service) renderViaSidecar(ctx context.Context, req Request) (*Result, error) {
	if strings.TrimSpace(s.cfg.SidecarURL) == "" {
		return nil, ErrNotConfigured
	}
	start := time.Now()

	body, err := sonic.Marshal(sidecarRequestFrom(req))
	if err != nil {
		return nil, fmt.Errorf("htmlrender: 序列化请求失败: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.SidecarURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("htmlrender: 构建侧车请求失败: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "image/png, application/json")
	if tok := strings.TrimSpace(s.cfg.SidecarToken); tok != "" {
		httpReq.Header.Set("Authorization", "Bearer "+tok)
	}

	timeout := s.cfg.SidecarTimeout
	if req.TimeoutMs > 0 {
		if d := time.Duration(req.TimeoutMs) * time.Millisecond; d > timeout {
			timeout = d
		}
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("htmlrender: 侧车请求失败: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("htmlrender: 读取侧车响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("htmlrender: 侧车返回 HTTP %d: %s", resp.StatusCode, truncateErrBody(raw))
	}

	renderMs := int(time.Since(start).Milliseconds())
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "json") || (len(raw) > 0 && raw[0] == '{') {
		return parseSidecarJSON(raw, renderMs)
	}
	return pngResultFromBytes(raw, renderMs)
}

func sidecarRequestFrom(req Request) map[string]any {
	selector := strings.TrimSpace(req.Selector)
	if selector == "" {
		selector = "body"
	}
	waitUntil := strings.TrimSpace(req.WaitUntil)
	if waitUntil == "" {
		waitUntil = "load"
	}
	vpW, vpH := req.ViewportWidth, req.ViewportHeight
	if vpW <= 0 {
		vpW = 900
	}
	if vpH <= 0 {
		vpH = 500
	}
	dsf := req.DeviceScaleFactor
	if dsf <= 0 {
		dsf = 2.0
	}
	timeoutMs := req.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = 10000
	}
	return map[string]any{
		"html":                req.HTML,
		"selector":            selector,
		"viewport_width":      vpW,
		"viewport_height":     vpH,
		"device_scale_factor": dsf,
		"full_page":           req.FullPage,
		"omit_background":     req.OmitBackground,
		"wait_until":          waitUntil,
		"wait_for_selector":   strings.TrimSpace(req.WaitForSelector),
		"wait_for_timeout_ms": req.WaitForTimeoutMs,
		"timeout_ms":          timeoutMs,
		"allow_network":       req.AllowNetwork,
	}
}

func parseSidecarJSON(raw []byte, renderMs int) (*Result, error) {
	var payload map[string]any
	if err := sonic.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("htmlrender: 侧车 JSON 无效: %w", err)
	}
	b64 := strings.TrimSpace(asString(payload["image_base64"]))
	if b64 == "" {
		return nil, fmt.Errorf("htmlrender: 侧车 JSON 缺少 image_base64")
	}
	pngBytes, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("htmlrender: image_base64 解码失败: %w", err)
	}
	res, err := pngResultFromBytes(pngBytes, renderMs)
	if err != nil {
		return nil, err
	}
	if w := int(asFloat(payload["width"])); w > 0 {
		res.Width = w
	}
	if h := int(asFloat(payload["height"])); h > 0 {
		res.Height = h
	}
	if ms := int(asFloat(payload["render_ms"])); ms > 0 {
		res.RenderMs = ms
	}
	if mt := strings.TrimSpace(asString(payload["mime_type"])); mt != "" {
		res.MimeType = mt
	}
	res.ImageBase64 = b64
	return res, nil
}

func pngResultFromBytes(pngBytes []byte, renderMs int) (*Result, error) {
	if len(pngBytes) == 0 {
		return nil, fmt.Errorf("htmlrender: 侧车返回空 PNG")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(pngBytes))
	if err != nil {
		return nil, fmt.Errorf("htmlrender: PNG 无效: %w", err)
	}
	return &Result{
		ImageBase64: base64.StdEncoding.EncodeToString(pngBytes),
		MimeType:    "image/png",
		Width:       cfg.Width,
		Height:      cfg.Height,
		RenderMs:    renderMs,
	}, nil
}

func truncateErrBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 240 {
		return s[:240] + "…"
	}
	return s
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

func asFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	default:
		return 0
	}
}

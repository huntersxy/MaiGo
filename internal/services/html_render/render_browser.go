package htmlrender

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image/png"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

func (s *Service) renderViaBrowser(ctx context.Context, req Request) (*Result, error) {
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	defer s.release()

	start := time.Now()
	browser, err := s.ensureBrowser(ctx)
	if err != nil {
		return nil, err
	}
	client := browser.client

	browserContextID := ""
	if raw, err := client.send(ctx, "", "Target.createBrowserContext", map[string]any{}); err == nil {
		var out map[string]string
		if sonicUnmarshal(raw, &out) == nil {
			browserContextID = out["browserContextId"]
		}
	}
	createParams := map[string]any{"url": "about:blank"}
	if browserContextID != "" {
		createParams["browserContextId"] = browserContextID
	}
	rawTarget, err := client.send(ctx, "", "Target.createTarget", createParams)
	if err != nil {
		closeBrowserContext(client, browserContextID)
		return nil, fmt.Errorf("创建浏览器页面失败: %w", err)
	}
	var targetOut map[string]string
	if err := sonicUnmarshal(rawTarget, &targetOut); err != nil {
		closeBrowserContext(client, browserContextID)
		return nil, fmt.Errorf("解析浏览器页面响应失败: %w", err)
	}
	targetID := targetOut["targetId"]
	if targetID == "" {
		closeBrowserContext(client, browserContextID)
		return nil, fmt.Errorf("创建浏览器页面失败: 缺少 targetId")
	}
	rawAttach, err := client.send(ctx, "", "Target.attachToTarget", map[string]any{
		"targetId": targetID,
		"flatten":  true,
	})
	if err != nil {
		closeTarget(client, targetID)
		closeBrowserContext(client, browserContextID)
		return nil, fmt.Errorf("附加浏览器页面失败: %w", err)
	}
	var attachOut map[string]string
	if err := sonicUnmarshal(rawAttach, &attachOut); err != nil {
		closeTarget(client, targetID)
		closeBrowserContext(client, browserContextID)
		return nil, fmt.Errorf("解析浏览器附加响应失败: %w", err)
	}
	sessionID := attachOut["sessionId"]
	if sessionID == "" {
		closeTarget(client, targetID)
		closeBrowserContext(client, browserContextID)
		return nil, fmt.Errorf("附加浏览器页面失败: 缺少 sessionId")
	}
	defer func() {
		client.removeSession(sessionID)
		closeTarget(client, targetID)
		closeBrowserContext(client, browserContextID)
	}()

	pageCtx, cancel := context.WithTimeout(ctx, time.Duration(req.TimeoutMs)*time.Millisecond)
	defer cancel()

	if err := client.command(pageCtx, sessionID, "Page.enable", nil, nil); err != nil {
		return nil, fmt.Errorf("启用 Page 域失败: %w", err)
	}
	if err := client.command(pageCtx, sessionID, "Runtime.enable", nil, nil); err != nil {
		return nil, fmt.Errorf("启用 Runtime 域失败: %w", err)
	}
	_ = client.command(pageCtx, sessionID, "Network.enable", nil, nil)
	if err := client.command(pageCtx, sessionID, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width":             req.ViewportWidth,
		"height":            req.ViewportHeight,
		"deviceScaleFactor": req.DeviceScaleFactor,
		"mobile":            false,
		"screenWidth":       req.ViewportWidth,
		"screenHeight":      req.ViewportHeight,
	}, nil); err != nil {
		return nil, fmt.Errorf("设置浏览器视口失败: %w", err)
	}

	tracker := newNetworkTracker()
	client.onEvent(sessionID, "Network.requestWillBeSent", func(params []byte) error {
		tracker.request(eventRequestID(params))
		return nil
	})
	client.onEvent(sessionID, "Network.loadingFinished", func(params []byte) error {
		tracker.done(eventRequestID(params))
		return nil
	})
	client.onEvent(sessionID, "Network.loadingFailed", func(params []byte) error {
		tracker.done(eventRequestID(params))
		return nil
	})
	if !req.AllowNetwork {
		if err := enableNetworkBlocking(client, sessionID, pageCtx); err != nil {
			return nil, err
		}
	}

	frameID, err := pageFrameID(pageCtx, client, sessionID)
	if err != nil {
		return nil, err
	}
	if err := client.command(pageCtx, sessionID, "Page.setDocumentContent", map[string]any{
		"frameId": frameID,
		"html":    req.HTML,
	}, nil); err != nil {
		return nil, fmt.Errorf("写入页面内容失败: %w", err)
	}
	if err := waitPageReady(pageCtx, client, sessionID, req.WaitUntil, tracker); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.WaitForSelector) != "" {
		if err := waitForSelector(pageCtx, client, sessionID, req.WaitForSelector); err != nil {
			return nil, err
		}
	}
	if req.WaitForTimeoutMs > 0 {
		timer := time.NewTimer(time.Duration(req.WaitForTimeoutMs) * time.Millisecond)
		select {
		case <-timer.C:
		case <-pageCtx.Done():
			timer.Stop()
			return nil, fmt.Errorf("等待页面稳定超时: %w", pageCtx.Err())
		}
	}
	if req.OmitBackground {
		_ = client.command(pageCtx, sessionID, "Emulation.setDefaultBackgroundColorOverride", map[string]any{
			"color": map[string]any{"r": 0, "g": 0, "b": 0, "a": 0},
		}, nil)
	}
	data, err := capturePage(pageCtx, client, sessionID, req)
	if err != nil {
		return nil, err
	}
	result, err := browserResult(data, int(time.Since(start).Milliseconds()))
	if err != nil {
		return nil, err
	}
	s.afterRender()
	return result, nil
}

func (s *Service) ensureBrowser(ctx context.Context) (*browserInstance, error) {
	s.browserMu.Lock()
	defer s.browserMu.Unlock()
	if s.browser != nil && s.browser.alive() {
		return s.browser, nil
	}
	if s.browser != nil {
		s.browser.close()
		s.browser = nil
	}
	var externalErr error
	if strings.TrimSpace(s.cfg.BrowserWS) != "" {
		wsURL, info, err := resolveBrowserWebSocket(ctx, s.cfg.BrowserWS, s.cfg.StartupTimeout)
		if err == nil {
			browser, connectErr := connectBrowser(ctx, wsURL, info)
			if connectErr == nil {
				s.browser = browser
				return browser, nil
			}
			externalErr = connectErr
		} else {
			externalErr = err
		}
	}
	browser, err := launchLocalBrowser(ctx, s.cfg)
	if err != nil {
		if externalErr != nil {
			return nil, fmt.Errorf("连接浏览器 CDP 端点失败: %v；启动本地浏览器失败: %w", externalErr, err)
		}
		return nil, err
	}
	s.browser = browser
	// 对齐 _update_managed_browser_record：本服务拉起浏览器后记录托管来源与目录
	updateManagedBrowserRecord(s.cfg.InstanceDir, resolveBrowserInstallRoot(s.cfg), managedBrowserCache, s.playwrightVersion())
	return browser, nil
}

func enableNetworkBlocking(client *cdpClient, sessionID string, ctx context.Context) error {
	client.onEvent(sessionID, "Fetch.requestPaused", func(params []byte) error {
		var event map[string]any
		if err := sonicUnmarshal(params, &event); err != nil {
			return err
		}
		requestID := stringValue(event["requestId"])
		request, _ := event["request"].(map[string]any)
		rawURL := stringValue(request["url"])
		if requestID == "" {
			return nil
		}
		if allowedRequestURL(rawURL) {
			_, err := client.send(ctx, sessionID, "Fetch.continueRequest", map[string]any{"requestId": requestID})
			return err
		}
		_, err := client.send(ctx, sessionID, "Fetch.failRequest", map[string]any{
			"requestId":   requestID,
			"errorReason": "Aborted",
		})
		return err
	})
	if err := client.command(ctx, sessionID, "Fetch.enable", map[string]any{
		"patterns": []map[string]any{{"urlPattern": "*", "requestStage": "Request"}},
	}, nil); err != nil {
		return fmt.Errorf("启用网络拦截失败: %w", err)
	}
	return nil
}

func allowedRequestURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "about", "blob", "data", "file":
		return true
	default:
		return false
	}
}

func pageFrameID(ctx context.Context, client *cdpClient, sessionID string) (string, error) {
	raw, err := client.send(ctx, sessionID, "Page.getFrameTree", nil)
	if err != nil {
		return "", fmt.Errorf("读取页面 frame 失败: %w", err)
	}
	var out map[string]any
	if err := sonicUnmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("解析页面 frame 失败: %w", err)
	}
	tree, _ := out["frameTree"].(map[string]any)
	frame, _ := tree["frame"].(map[string]any)
	frameID := stringValue(frame["id"])
	if frameID == "" {
		return "", fmt.Errorf("读取页面 frame 失败: 缺少 frameId")
	}
	return frameID, nil
}

func waitPageReady(ctx context.Context, client *cdpClient, sessionID, waitUntil string, tracker *networkTracker) error {
	// 归一语义见 service.go 的 normalizeWaitUntil，对齐 Python _normalize_wait_until
	waitUntil = normalizeWaitUntil(waitUntil)
	if waitUntil == "commit" {
		return nil
	}
	var lastErr error
	for {
		value, err := evaluateValue(ctx, client, sessionID, "document.readyState")
		if err != nil {
			lastErr = err
		} else {
			state := stringValue(value)
			ready := state == "complete"
			if waitUntil == "domcontentloaded" {
				ready = state == "interactive" || state == "complete"
			}
			if ready {
				if waitUntil == "networkidle" {
					if err := tracker.waitIdle(ctx, 500*time.Millisecond); err != nil {
						return err
					}
				}
				return nil
			}
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("等待页面加载超时: %v", lastErr)
			}
			return fmt.Errorf("等待页面加载超时: %w", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func waitForSelector(ctx context.Context, client *cdpClient, sessionID, selector string) error {
	expression := "document.querySelector(" + strconv.Quote(selector) + ") !== null"
	var lastErr error
	for {
		value, err := evaluateValue(ctx, client, sessionID, expression)
		if err != nil {
			lastErr = err
		} else if matched, _ := value.(bool); matched {
			return nil
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("等待选择器失败: %v", lastErr)
			}
			return fmt.Errorf("等待选择器超时: %s", selector)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func evaluateValue(ctx context.Context, client *cdpClient, sessionID, expression string) (any, error) {
	raw, err := client.send(ctx, sessionID, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"awaitPromise":  true,
		"returnByValue": true,
	})
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := sonicUnmarshal(raw, &out); err != nil {
		return nil, err
	}
	if details, ok := out["exceptionDetails"].(map[string]any); ok {
		text := stringValue(details["text"])
		if exception, ok := details["exception"].(map[string]any); ok {
			if description := stringValue(exception["description"]); description != "" {
				text = description
			}
		}
		if text == "" {
			text = "页面脚本执行异常"
		}
		return nil, fmt.Errorf("%s", text)
	}
	result, _ := out["result"].(map[string]any)
	return result["value"], nil
}

func capturePage(ctx context.Context, client *cdpClient, sessionID string, req Request) (string, error) {
	params := map[string]any{
		"format":                "png",
		"fromSurface":           true,
		"captureBeyondViewport": true,
	}
	if !(req.FullPage && req.Selector == "body") {
		clip, err := elementClip(ctx, client, sessionID, req.Selector)
		if err != nil {
			return "", err
		}
		params["clip"] = clip
	}
	raw, err := client.send(ctx, sessionID, "Page.captureScreenshot", params)
	if err != nil {
		return "", fmt.Errorf("浏览器截图失败: %w", err)
	}
	var out map[string]string
	if err := sonicUnmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("解析浏览器截图失败: %w", err)
	}
	data := strings.TrimSpace(out["data"])
	if data == "" {
		return "", fmt.Errorf("浏览器截图结果为空")
	}
	return data, nil
}

func elementClip(ctx context.Context, client *cdpClient, sessionID, selector string) (map[string]any, error) {
	quoted := strconv.Quote(selector)
	expression := "(() => {" +
		"const el = document.querySelector(" + quoted + ");" +
		"if (!el) return null;" +
		"el.scrollIntoView({block: 'center', inline: 'center', behavior: 'instant'});" +
		"const r = el.getBoundingClientRect();" +
		"const style = window.getComputedStyle(el);" +
		"return {x: r.left + window.scrollX, y: r.top + window.scrollY, width: r.width, height: r.height," +
		" visible: style.display !== 'none' && style.visibility !== 'hidden' && Number(style.opacity) > 0};" +
		"})()"
	var lastErr error
	for {
		value, err := evaluateValue(ctx, client, sessionID, expression)
		if err != nil {
			lastErr = fmt.Errorf("定位截图元素失败: %w", err)
		} else if rect, ok := value.(map[string]any); ok && rect != nil {
			width := floatValue(rect["width"])
			height := floatValue(rect["height"])
			visible, _ := rect["visible"].(bool)
			if visible && width > 0 && height > 0 {
				return map[string]any{
					"x":      floatValue(rect["x"]),
					"y":      floatValue(rect["y"]),
					"width":  width,
					"height": height,
					"scale":  1,
				}, nil
			}
			lastErr = fmt.Errorf("截图元素不可见: %s", selector)
		} else {
			lastErr = fmt.Errorf("未找到截图元素: %s", selector)
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, fmt.Errorf("等待截图元素超时: %s", selector)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func browserResult(base64Data string, renderMs int) (*Result, error) {
	pngBytes, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return nil, fmt.Errorf("解析浏览器 PNG 失败: %w", err)
	}
	config, err := png.DecodeConfig(bytes.NewReader(pngBytes))
	if err != nil {
		return nil, fmt.Errorf("浏览器返回 PNG 无效: %w", err)
	}
	return &Result{
		ImageBase64: base64Data,
		MimeType:    "image/png",
		Width:       config.Width,
		Height:      config.Height,
		RenderMs:    renderMs,
	}, nil
}

func closeTarget(client *cdpClient, targetID string) {
	if client == nil || targetID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = client.send(ctx, "", "Target.closeTarget", map[string]any{"targetId": targetID})
}

func closeBrowserContext(client *cdpClient, contextID string) {
	if client == nil || contextID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = client.send(ctx, "", "Target.disposeBrowserContext", map[string]any{"browserContextId": contextID})
}

type networkTracker struct {
	mu       sync.Mutex
	inflight map[string]struct{}
	last     time.Time
}

func newNetworkTracker() *networkTracker {
	return &networkTracker{inflight: make(map[string]struct{}), last: time.Now()}
}

func (n *networkTracker) request(id string) {
	if n == nil || id == "" {
		return
	}
	n.mu.Lock()
	n.inflight[id] = struct{}{}
	n.last = time.Now()
	n.mu.Unlock()
}

func (n *networkTracker) done(id string) {
	if n == nil || id == "" {
		return
	}
	n.mu.Lock()
	delete(n.inflight, id)
	n.last = time.Now()
	n.mu.Unlock()
}

func (n *networkTracker) idleFor(duration time.Duration) bool {
	if n == nil {
		return true
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.inflight) == 0 && time.Since(n.last) >= duration
}

func (n *networkTracker) waitIdle(ctx context.Context, duration time.Duration) error {
	for {
		if n.idleFor(duration) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待网络空闲超时: %w", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func eventRequestID(params []byte) string {
	var event map[string]any
	if err := sonicUnmarshal(params, &event); err != nil {
		return ""
	}
	return stringValue(event["requestId"])
}

func stringValue(value any) string {
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func floatValue(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	default:
		return 0
	}
}

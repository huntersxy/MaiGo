package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"maibot-go/internal/common/message_server"
	"maibot-go/internal/platform_io"
	"maibot-go/internal/platform_io/drivers/maim_bridge"

	"github.com/coder/websocket"
)

// apiConn 新 API Server 单条客户端连接
type apiConn struct {
	uuid      string
	userID    string
	platform  string
	accountID string
	scope     string
	apiKey    string
	ws        *websocket.Conn
	writeMu   sync.Mutex
}

func (c *apiConn) write(ctx context.Context, payload []byte) error {
	if c.ws == nil {
		return fmt.Errorf("maimmsg: API 连接 %s 未建立", c.uuid)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.Write(ctx, websocket.MessageText, payload)
}

// apiHub 维护新 API Server 的连接与平台路由
type apiHub struct {
	mu          sync.RWMutex
	conns       map[string]*apiConn
	byPlatform  map[string]map[string]*apiConn
	byRoute     map[string]map[string]*apiConn
	platformKey map[string]string
	cache       *apiMessageCache
}

func newAPIHub() *apiHub {
	return &apiHub{
		conns:       make(map[string]*apiConn),
		byPlatform:  make(map[string]map[string]*apiConn),
		byRoute:     make(map[string]map[string]*apiConn),
		platformKey: make(map[string]string),
	}
}

// apiRouteIndex 将平台/账号/作用域归一为路由索引；无法归一时退回平台级
func apiRouteIndex(platform, accountID, scope string) (platformio.RouteKey, string) {
	route := platformio.RouteKey{Platform: trimSpace(platform), AccountID: trimSpace(accountID), Scope: trimSpace(scope)}
	if normalized, err := route.Normalize(); err == nil {
		return normalized, routeIndexKey(normalized)
	}
	fallback := platformio.RouteKey{Platform: trimSpace(platform)}
	return fallback, routeIndexKey(fallback)
}

func (c *apiConn) routeKey() platformio.RouteKey {
	route, _ := apiRouteIndex(c.platform, c.accountID, c.scope)
	return route
}

func routeIndexKey(route platformio.RouteKey) string {
	return route.Platform + "\x00" + route.AccountID + "\x00" + route.Scope
}

func (h *apiHub) attach(conn *apiConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[conn.uuid] = conn
	platform := conn.platform
	if h.byPlatform[platform] == nil {
		h.byPlatform[platform] = make(map[string]*apiConn)
	}
	h.byPlatform[platform][conn.uuid] = conn
	_, index := apiRouteIndex(conn.platform, conn.accountID, conn.scope)
	if h.byRoute[index] == nil {
		h.byRoute[index] = make(map[string]*apiConn)
	}
	h.byRoute[index][conn.uuid] = conn
	if conn.apiKey != "" {
		h.platformKey[platform] = conn.apiKey
	}
}

func (h *apiHub) detach(conn *apiConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.conns, conn.uuid)
	if platformConns, ok := h.byPlatform[conn.platform]; ok {
		delete(platformConns, conn.uuid)
		if len(platformConns) == 0 {
			delete(h.byPlatform, conn.platform)
		}
	}
	_, index := apiRouteIndex(conn.platform, conn.accountID, conn.scope)
	if routeConns, ok := h.byRoute[index]; ok {
		delete(routeConns, conn.uuid)
		if len(routeConns) == 0 {
			delete(h.byRoute, index)
		}
	}
}

// rememberKey 记录平台最近一次入站使用的 API Key，用于出站信封
func (h *apiHub) rememberKey(platform, apiKey string) {
	apiKey = trimSpace(apiKey)
	if platform == "" || apiKey == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.platformKey[platform] = apiKey
}

func (h *apiHub) apiKeyForPlatform(platform string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if key := h.platformKey[platform]; key != "" {
		return key
	}
	for _, conn := range h.byPlatform[platform] {
		if conn.apiKey != "" {
			return conn.apiKey
		}
	}
	return ""
}

func (h *apiHub) hasPlatform(platform string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.byPlatform[platform]) > 0
}

// send 向平台下的全部 API 连接发送 sys_std 信封
func (h *apiHub) send(ctx context.Context, apiMsg maimmsg.APIMessageBase) (map[string]bool, error) {
	platform := trimSpace(apiMsg.GetPlatform())
	return h.sendRoute(ctx, apiMsg, platformio.RouteKey{Platform: platform})
}

// sendRoute 按路由精确发送；路由带账号/作用域时不向同平台其他身份广播
func (h *apiHub) sendRoute(ctx context.Context, apiMsg maimmsg.APIMessageBase, route platformio.RouteKey) (map[string]bool, error) {
	platform := trimSpace(apiMsg.GetPlatform())
	normalized, err := route.Normalize()
	if err != nil {
		normalized = platformio.RouteKey{Platform: platform}
	}
	if normalized.Platform == "" {
		normalized.Platform = platform
	}
	h.mu.RLock()
	var targets []*apiConn
	if normalized.AccountID != "" || normalized.Scope != "" {
		index := routeIndexKey(normalized)
		for _, conn := range h.byRoute[index] {
			targets = append(targets, conn)
		}
	} else {
		for _, conn := range h.byPlatform[normalized.Platform] {
			targets = append(targets, conn)
		}
	}
	h.mu.RUnlock()
	if len(targets) == 0 {
		if normalized.AccountID != "" || normalized.Scope != "" {
			return nil, fmt.Errorf("maimmsg: 平台 %q 账号 %q 作用域 %q 无 API 连接", normalized.Platform, normalized.AccountID, normalized.Scope)
		}
		return nil, fmt.Errorf("maimmsg: 平台 %q 无 API 连接", normalized.Platform)
	}
	results := make(map[string]bool, len(targets))
	for _, conn := range targets {
		msgID := newEnvelopeMsgID()
		payload, err := marshalAPIEnvelopeWithID(msgID, "sys_std", newServerEnvelopeMeta(conn.userID, normalized.Platform), apiMsg.ToMap())
		if err != nil {
			results[conn.uuid] = false
			continue
		}
		if h.cache != nil {
			h.cache.Add(msgID, conn.uuid, payload)
		}
		if err := conn.write(ctx, payload); err != nil {
			results[conn.uuid] = false
			continue
		}
		results[conn.uuid] = true
	}
	return results, nil
}

// connectionCounts 返回当前连接数与去重用户数
func (h *apiHub) connectionCounts() (connections, users int) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	seen := make(map[string]struct{})
	for _, conn := range h.conns {
		seen[conn.userID] = struct{}{}
	}
	return len(h.conns), len(seen)
}

// hasRoute 判断指定路由是否已有 API 连接；带账号/作用域时要求精确匹配
func (h *apiHub) hasRoute(route platformio.RouteKey) bool {
	normalized, err := route.Normalize()
	if err != nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if normalized.AccountID != "" || normalized.Scope != "" {
		return len(h.byRoute[routeIndexKey(normalized)]) > 0
	}
	return len(h.byPlatform[normalized.Platform]) > 0
}

func (h *apiHub) apiKeyForRoute(route platformio.RouteKey) string {
	normalized, err := route.Normalize()
	if err != nil {
		return ""
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if normalized.AccountID != "" || normalized.Scope != "" {
		for _, conn := range h.byRoute[routeIndexKey(normalized)] {
			if conn.apiKey != "" {
				return conn.apiKey
			}
		}
		return ""
	}
	if key := h.platformKey[normalized.Platform]; key != "" {
		return key
	}
	for _, conn := range h.byPlatform[normalized.Platform] {
		if conn.apiKey != "" {
			return conn.apiKey
		}
	}
	return ""
}

// APICustomHandler 新 API Server 自定义类型处理器，对齐 register_custom_handler
type APICustomHandler func(ctx context.Context, payload map[string]any, metadata map[string]any) error

// hubRouter 组合旧版连接和 API 连接，供 maimbridge.Adapter 使用
// 出站优先旧版链路，失败后回退新 API Server（对齐 MaiBot uni_message_sender 的回退顺序）
type hubRouter struct {
	legacy *connHub
	api    *apiHub
}

func (h *hubRouter) HasPlatform(platform string) bool {
	return h.legacy.HasPlatform(platform) || h.api.hasPlatform(platform)
}

func (h *hubRouter) SendToPlatform(ctx context.Context, platform string, payload []byte) error {
	return h.SendToRoute(ctx, platformio.RouteKey{Platform: platform}, payload)
}

// HasRoute 判断精确路由（含账号/作用域）是否可出站
func (h *hubRouter) HasRoute(route platformio.RouteKey) bool {
	if h.api.hasRoute(route) {
		return true
	}
	if route.AccountID != "" || route.Scope != "" {
		return false
	}
	return h.legacy.HasPlatform(route.Platform)
}

// SendToRoute 优先旧版平台连接，再按账号/作用域回退新 API Server
func (h *hubRouter) SendToRoute(ctx context.Context, route platformio.RouteKey, payload []byte) error {
	normalized, err := route.Normalize()
	if err != nil {
		return fmt.Errorf("maimmsg: 路由键无效: %w", err)
	}
	var legacyErr error
	if normalized.AccountID == "" && normalized.Scope == "" && h.legacy.HasPlatform(normalized.Platform) {
		legacyErr = h.legacy.SendToPlatform(ctx, normalized.Platform, payload)
		if legacyErr == nil {
			return nil
		}
	}
	if h.api.hasRoute(normalized) {
		msg, err := maimmsg.UnmarshalMessage(payload)
		if err != nil {
			return fmt.Errorf("maimmsg: 解析旧版消息失败: %w", err)
		}
		apiMsg := maimmsg.MessageConverter{}.ToAPISend(msg, h.api.apiKeyForRoute(normalized), normalized.Platform)
		results, err := h.api.sendRoute(ctx, apiMsg, normalized)
		if err != nil {
			return err
		}
		for _, ok := range results {
			if ok {
				return nil
			}
		}
		return fmt.Errorf("maimmsg: 平台 %q 账号 %q 作用域 %q API 连接写入失败", normalized.Platform, normalized.AccountID, normalized.Scope)
	}
	if legacyErr != nil {
		return legacyErr
	}
	return fmt.Errorf("maimmsg: 平台 %q 无连接", normalized.Platform)
}

var _ maimbridge.Hub = (*hubRouter)(nil)
var _ maimbridge.RouteHub = (*hubRouter)(nil)

// marshalAPIEnvelope 构造上位机/服务端信封
func marshalAPIEnvelope(msgType string, meta map[string]any, payload any) ([]byte, error) {
	return marshalAPIEnvelopeWithID(newEnvelopeMsgID(), msgType, meta, payload)
}

func marshalAPIEnvelopeWithID(msgID, msgType string, meta map[string]any, payload any) ([]byte, error) {
	envelope := map[string]any{
		"ver":     apiEnvelopeVersion,
		"msg_id":  msgID,
		"type":    msgType,
		"meta":    meta,
		"payload": payload,
	}
	return json.Marshal(envelope)
}

func newServerEnvelopeMeta(targetUser, platform string) map[string]any {
	return map[string]any{
		"sender_user": "server",
		"target_user": targetUser,
		"platform":    platform,
		"timestamp":   nowUnixFloat(),
	}
}

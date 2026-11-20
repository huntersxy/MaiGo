package snowluma

import (
	"context"
	"fmt"
	"strings"

	"MaiGo/internal/plugin_runtime/capabilities"
)

// InboundSink 入站投递面：只允许 ReceiveGatewayMessage
type InboundSink interface {
	ReceiveGatewayMessage(ctx context.Context, request capabilities.PluginGatewayInboundRequest, message map[string]any) (bool, map[string]any, error)
}

// Router 事件分派：identity 闸门 + 聊天/notice 入站
type Router struct {
	settings     Settings
	filters      *MessageFilters
	observations *IdentityObservations
	lease        *IdentityOnlyLease
	runtime      *RuntimeState
	profile      *ProfileState
	query        *QueryService
	sink         InboundSink
}

func NewRouter(settings Settings, runtime *RuntimeState, profile *ProfileState, query *QueryService, sink InboundSink) *Router {
	return &Router{
		settings:     settings,
		filters:      NewMessageFilters(settings),
		observations: NewIdentityObservations(),
		lease:        NewIdentityOnlyLease(),
		runtime:      runtime,
		profile:      profile,
		query:        query,
		sink:         sink,
	}
}

func (r *Router) IdentityObservations() *IdentityObservations {
	if r == nil {
		return NewIdentityObservations()
	}
	return r.observations
}

func (r *Router) IdentityOnlyLease() *IdentityOnlyLease {
	if r == nil {
		return NewIdentityOnlyLease()
	}
	return r.lease
}

func (r *Router) ClearIdentity() {
	if r == nil {
		return
	}
	r.observations.Clear()
	r.lease.Clear()
}

func (r *Router) HandlePayload(ctx context.Context, payload map[string]any) error {
	if r == nil || payload == nil {
		return nil
	}
	postType := strings.ToLower(asString(payload["post_type"]))
	switch postType {
	case "meta_event":
		return nil
	case "message", "message_sent":
		return r.handleMessage(ctx, payload)
	case "notice":
		return r.handleNotice(ctx, payload)
	default:
		return nil
	}
}

func (r *Router) handleMessage(ctx context.Context, payload map[string]any) error {
	accountID := r.runtime.AccountID()
	if accountID == "" {
		accountID = asString(payload["self_id"])
	}
	scope := r.runtime.Scope()
	message := NormalizeInboundMessage(payload, accountID, scope)
	if message == nil {
		return nil
	}
	userID := asString(message["user_id"])
	isSelf := userID != "" && userID == accountID
	if drop, _ := r.filters.ShouldDrop(userID, asString(message["content"]), false, isSelf); drop {
		return nil
	}
	identityOnly := r.lease.Active(accountID)
	r.observations.Observe(message)
	if identityOnly || !r.runtime.RoutingEnabled() {
		return nil
	}
	r.resolveInboundImages(ctx, message)
	return r.deliver(ctx, message)
}

func (r *Router) handleNotice(ctx context.Context, payload map[string]any) error {
	noticeType := asString(payload["notice_type"])
	subType := asString(payload["sub_type"])
	if !NoticeAllowed(r.settings, noticeType, subType) {
		return nil
	}
	accountID := r.runtime.AccountID()
	if accountID == "" {
		accountID = asString(payload["self_id"])
	}
	if r.lease.Active(accountID) || !r.runtime.RoutingEnabled() {
		return nil
	}
	message := map[string]any{
		"platform":     PlatformQQ,
		"account_id":   accountID,
		"scope":        r.runtime.Scope(),
		"message_type": "notice",
		"notice_type":  noticeType,
		"sub_type":     subType,
		"user_id":      asString(payload["user_id"]),
		"group_id":     asString(payload["group_id"]),
		"raw":          payload,
		"is_notify":    true,
	}
	return r.deliver(ctx, message)
}

func (r *Router) deliver(ctx context.Context, message map[string]any) error {
	if r.sink == nil {
		return fmt.Errorf("入站端口未接线")
	}
	request := capabilities.PluginGatewayInboundRequest{
		PluginID:          PluginID,
		GatewayName:       GatewayName,
		DriverID:          platformioDriverID(),
		Platform:          PlatformQQ,
		AccountID:         asString(message["account_id"]),
		Scope:             asString(message["scope"]),
		ExternalMessageID: asString(message["message_id"]),
		DedupeKey:         asString(message["message_id"]),
	}
	_, _, err := r.sink.ReceiveGatewayMessage(ctx, request, message)
	return err
}

func platformioDriverID() string {
	return "gateway:" + PluginID + ":" + GatewayName
}

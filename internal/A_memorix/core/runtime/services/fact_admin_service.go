package services

// 事实账本管理服务，对齐 Python core/runtime/services/fact_admin_service.py
// Python 侧在单个存储事务内完成"写入 claim + 入队画像刷新"，Go 侧存储方法自带事务，
// 这里按相同顺序调用，靠每个方法自身的原子性保证一致性

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"maibot-go/internal/A_memorix/core/storage"
)

// FactAdminDeps 事实管理服务依赖的存储能力
type FactAdminDeps struct {
	// Initialize 确保运行时已初始化，对齐 await self.initialize()
	Initialize func(ctx context.Context) error
	// GetFactClaim 读取单条 claim，不存在返回 nil
	GetFactClaim func(claimID string) (map[string]any, error)
	// ListFactClaims 按作用域与状态列出 claim
	ListFactClaims func(query storage.FactClaimQuery) ([]map[string]any, error)
	// UpsertFactClaim 写入 claim
	UpsertFactClaim func(input storage.FactClaimUpsert) (map[string]any, error)
	// UpdateFactClaimClassification 更新分类信息
	UpdateFactClaimClassification func(claimID string, input storage.FactClaimClassification) (map[string]any, error)
	// RetractFactClaim 撤回 claim
	RetractFactClaim func(claimID string, audit storage.FactClaimAudit) (map[string]any, error)
	// RestoreFactClaim 恢复 claim
	RestoreFactClaim func(claimID string, audit storage.FactClaimAudit) (map[string]any, error)
	// EnqueuePersonProfileRefresh 入队人物画像刷新
	EnqueuePersonProfileRefresh func(input storage.PersonProfileRefreshEnqueueInput) (map[string]any, error)
}

// FactAdminService 对齐 MemoryFactAdminService
type FactAdminService struct {
	*Base
	Deps FactAdminDeps
}

// NewFactAdminService 构造事实管理服务
func NewFactAdminService(base *Base, deps FactAdminDeps) *FactAdminService {
	return &FactAdminService{Base: base, Deps: deps}
}

// NewFactAdminServiceFromStore 用真实存储装配事实管理服务
func NewFactAdminServiceFromStore(base *Base, store *storage.Store) *FactAdminService {
	return NewFactAdminService(base, FactAdminDeps{
		GetFactClaim:                  store.GetFactClaim,
		ListFactClaims:                store.ListFactClaims,
		UpsertFactClaim:               store.UpsertFactClaim,
		UpdateFactClaimClassification: store.UpdateFactClaimClassification,
		RetractFactClaim:              store.RetractFactClaim,
		RestoreFactClaim:              store.RestoreFactClaim,
		EnqueuePersonProfileRefresh:   store.EnqueuePersonProfileRefresh,
	})
}

// MemoryFactAdmin 对齐 memory_fact_admin
func (s *FactAdminService) MemoryFactAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if s.Deps.Initialize != nil {
		if err := s.Deps.Initialize(ctx); err != nil {
			return nil, err
		}
	}
	act := normalizeAction(action)
	var (
		result map[string]any
		err    error
	)
	switch act {
	case "get":
		result, err = s.getFact(strings.TrimSpace(asString(payload["claim_id"])))
	case "list":
		result, err = s.listFacts(payload)
	case "create":
		result, err = s.createFact(ctx, payload)
	case "update":
		result, err = s.updateFact(ctx, strings.TrimSpace(asString(payload["claim_id"])), payload)
	case "retract", "restore":
		result, err = s.changeFactStatus(ctx, act, strings.TrimSpace(asString(payload["claim_id"])), payload)
	default:
		return map[string]any{"success": false, "error": "不支持的 fact action: " + act}, nil
	}
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}, nil
	}
	return result, nil
}

// getFact 对齐 get 分支
func (s *FactAdminService) getFact(claimID string) (map[string]any, error) {
	claim, err := s.Deps.GetFactClaim(claimID)
	if err != nil {
		return nil, err
	}
	message := ""
	if claim == nil {
		message = "事实 claim 不存在"
	}
	return map[string]any{"success": claim != nil, "claim": claim, "error": message}, nil
}

// listFacts 对齐 list 分支
func (s *FactAdminService) listFacts(payload map[string]any) (map[string]any, error) {
	limit := pyIntOr(payload["limit"], 200, 1)
	query := storage.FactClaimQuery{
		ScopeType: pyStringOr(payload["scope_type"], "person"),
		ScopeID:   pyStringOr(payload["scope_id"], ""),
		Limit:     limit,
	}
	if statuses := tokens(payload["statuses"]); len(statuses) > 0 {
		query.Statuses = statuses
	}
	claims, err := s.Deps.ListFactClaims(query)
	if err != nil {
		return nil, err
	}
	if claims == nil {
		claims = []map[string]any{}
	}
	return map[string]any{"success": true, "items": claims, "count": len(claims)}, nil
}

// createFact 对齐 _create_fact
func (s *FactAdminService) createFact(ctx context.Context, payload map[string]any) (map[string]any, error) {
	claim, err := s.upsertClaim(ctx, payload, nil, nil)
	if err != nil {
		return nil, err
	}
	refreshQueued, err := s.enqueueProfileRefresh(ctx, claim, "fact_claim_created")
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "claim": claim, "refresh_queued": refreshQueued}, nil
}

// updateFact 对齐 _update_fact
func (s *FactAdminService) updateFact(ctx context.Context, claimID string, payload map[string]any) (map[string]any, error) {
	existing, err := s.Deps.GetFactClaim(claimID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("事实 claim 不存在: %s", claimID)
	}
	requestedScopeType := asString(factClaimValue(payload, existing, "scope_type", "", false))
	requestedScopeID := asString(factClaimValue(payload, existing, "scope_id", "", false))
	if requestedScopeType != asString(existing["scope_type"]) || requestedScopeID != asString(existing["scope_id"]) {
		return nil, fmt.Errorf("事实更新不能改变 scope_type 或 scope_id")
	}
	claim := existing
	if factClaimIdentityChanged(existing, payload) {
		supersedes := []string{claimID}
		claim, err = s.upsertClaim(ctx, payload, existing, supersedes)
		if err != nil {
			if !strings.Contains(err.Error(), "不属于同一冲突组") {
				return nil, err
			}
			claim, err = s.upsertClaim(ctx, payload, existing, nil)
			if err != nil {
				return nil, err
			}
			if asString(claim["claim_id"]) != claimID {
				reason := factReason(payload, "webui_fact_revised")
				if _, err := s.Deps.RetractFactClaim(claimID, storage.FactClaimAudit{Reason: reason}); err != nil {
					return nil, err
				}
				claim, err = s.Deps.RestoreFactClaim(asString(claim["claim_id"]), storage.FactClaimAudit{Reason: reason})
				if err != nil {
					return nil, err
				}
			}
		}
	}
	confidence, ok := asFloat(factClaimValue(payload, claim, "confidence", 1.0, false))
	if !ok {
		confidence = 1.0
	}
	claim, err = s.Deps.UpdateFactClaimClassification(asString(claim["claim_id"]), storage.FactClaimClassification{
		Stability:      asString(factClaimValue(payload, claim, "stability", "stable", false)),
		ProfileSection: asString(factClaimValue(payload, claim, "profile_section", "stable_facts", false)),
		Authority:      asString(factClaimValue(payload, claim, "authority", "manual", false)),
		Confidence:     confidence,
		ValidFrom:      optionalFloat(factClaimValue(payload, claim, "valid_from", nil, true)),
		ValidTo:        optionalFloat(factClaimValue(payload, claim, "valid_to", nil, true)),
		Reason:         factReason(payload, "webui_fact_classification_updated"),
	})
	if err != nil {
		return nil, err
	}
	refreshQueued, err := s.enqueueProfileRefresh(ctx, claim, "fact_claim_updated")
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"success":           true,
		"claim":             claim,
		"previous_claim_id": claimID,
		"replaced":          asString(claim["claim_id"]) != claimID,
		"refresh_queued":    refreshQueued,
	}, nil
}

// changeFactStatus 对齐 _change_fact_status
func (s *FactAdminService) changeFactStatus(ctx context.Context, action string, claimID string, payload map[string]any) (map[string]any, error) {
	reason := factReason(payload, "webui_fact_"+action)
	var claim map[string]any
	var err error
	if action == "retract" {
		claim, err = s.Deps.RetractFactClaim(claimID, storage.FactClaimAudit{Reason: reason})
	} else {
		claim, err = s.Deps.RestoreFactClaim(claimID, storage.FactClaimAudit{Reason: reason})
	}
	if err != nil {
		return nil, err
	}
	refreshReason := "fact_claim_restored"
	if action == "retract" {
		refreshReason = "fact_claim_retracted"
	}
	refreshQueued, err := s.enqueueProfileRefresh(ctx, claim, refreshReason)
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "claim": claim, "refresh_queued": refreshQueued}, nil
}

// upsertClaim 对齐 _upsert_claim
func (s *FactAdminService) upsertClaim(ctx context.Context, payload map[string]any, existing map[string]any, supersedes []string) (map[string]any, error) {
	_ = ctx
	confidence, ok := asFloat(factClaimValue(payload, existing, "confidence", 1.0, false))
	if !ok {
		confidence = 1.0
	}
	return s.Deps.UpsertFactClaim(storage.FactClaimUpsert{
		ScopeType:          asString(factClaimValue(payload, existing, "scope_type", "person", false)),
		ScopeID:            asString(factClaimValue(payload, existing, "scope_id", "", false)),
		FactKey:            asString(factClaimValue(payload, existing, "fact_key", "", false)),
		ValueText:          asString(factClaimValue(payload, existing, "value_text", "", false)),
		Polarity:           asString(factClaimValue(payload, existing, "polarity", "positive", false)),
		Cardinality:        asString(factClaimValue(payload, existing, "cardinality", "set", false)),
		Stability:          asString(factClaimValue(payload, existing, "stability", "stable", false)),
		ProfileSection:     asString(factClaimValue(payload, existing, "profile_section", "stable_facts", false)),
		Authority:          asString(factClaimValue(payload, existing, "authority", "manual", false)),
		Confidence:         &confidence,
		ValidFrom:          optionalFloat(factClaimValue(payload, existing, "valid_from", nil, true)),
		ValidTo:            optionalFloat(factClaimValue(payload, existing, "valid_to", nil, true)),
		SupersedesClaimIDs: supersedes,
		Reason:             factReason(payload, "webui_fact_write"),
	})
}

// enqueueProfileRefresh 对齐 _enqueue_profile_refresh
func (s *FactAdminService) enqueueProfileRefresh(ctx context.Context, claim map[string]any, reason string) (bool, error) {
	_ = ctx
	if asString(claim["scope_type"]) != "person" {
		return false, nil
	}
	if !pyTruthy(s.Cfg("person_profile.enabled", true)) {
		return false, nil
	}
	request, err := s.Deps.EnqueuePersonProfileRefresh(storage.PersonProfileRefreshEnqueueInput{
		PersonID: asString(claim["scope_id"]),
		Reason:   reason,
	})
	if err != nil {
		return false, err
	}
	return request != nil, nil
}

// factClaimValue 对齐 _claim_value
func factClaimValue(payload map[string]any, existing map[string]any, key string, fallback any, allowNone bool) any {
	if raw, ok := payload[key]; ok && (allowNone || raw != nil) {
		return raw
	}
	if existing != nil {
		if value, ok := existing[key]; ok {
			return value
		}
	}
	return fallback
}

// factReason 对齐 str(kwargs.get("reason", "") or 默认值)
func factReason(payload map[string]any, fallback string) string {
	value := payload["reason"]
	if !pyTruthy(value) {
		return fallback
	}
	return asString(value)
}

// factPyStringOr 对齐 str(kwargs.get(key, "") or 默认值)
func pyStringOr(value any, fallback string) string {
	if !pyTruthy(value) {
		return fallback
	}
	return asString(value)
}

// factNormalizedIdentityValue 对齐 _normalized_identity_value：NFKC、空白折叠、casefold
func factNormalizedIdentityValue(value any) string {
	if !pyTruthy(value) && value != nil {
		return ""
	}
	text := norm.NFKC.String(asString(value))
	return cases.Fold().String(strings.Join(strings.Fields(text), " "))
}

// factClaimIdentityChanged 对齐 _claim_identity_changed
func factClaimIdentityChanged(existing map[string]any, payload map[string]any) bool {
	comparisons := map[string]string{
		"fact_key":    factNormalizedIdentityValue(existing["fact_key"]),
		"value_text":  factNormalizedIdentityValue(existing["value_normalized"]),
		"polarity":    factNormalizedIdentityValue(existing["polarity"]),
		"cardinality": factNormalizedIdentityValue(existing["cardinality"]),
	}
	for _, key := range []string{"fact_key", "value_text", "polarity", "cardinality"} {
		raw, ok := payload[key]
		if !ok {
			continue
		}
		if factNormalizedIdentityValue(raw) != comparisons[key] {
			return true
		}
	}
	return false
}

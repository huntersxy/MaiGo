package snowluma

import (
	"fmt"
	"sync"
	"time"
)

const identityOnlyTTL = 15 * time.Second

// IdentityOnlyLease 官方适配器短时身份查询租约
type IdentityOnlyLease struct {
	mu        sync.Mutex
	accountID string
	owner     string
	expiresAt time.Time
}

func NewIdentityOnlyLease() *IdentityOnlyLease {
	return &IdentityOnlyLease{}
}

func (l *IdentityOnlyLease) Clear() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.accountID = ""
	l.owner = ""
	l.expiresAt = time.Time{}
}

func (l *IdentityOnlyLease) Active(accountID string) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if time.Now().After(l.expiresAt) || time.Now().Equal(l.expiresAt) {
		l.accountID = ""
		l.owner = ""
		l.expiresAt = time.Time{}
	}
	return l.owner != "" && l.accountID == accountID
}

func (l *IdentityOnlyLease) Remaining(accountID string) time.Duration {
	if !l.Active(accountID) {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	remaining := time.Until(l.expiresAt)
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (l *IdentityOnlyLease) StatusBadges(accountID string) []map[string]any {
	if !l.Active(accountID) {
		return nil
	}
	remaining := l.Remaining(accountID)
	return []map[string]any{{
		"id":         "identity_only",
		"label":      "仅身份查询",
		"expires_at": float64(time.Now().Add(remaining).Unix()) + float64(remaining.Nanoseconds()%1e9)/1e9,
	}}
}

func (l *IdentityOnlyLease) Update(accountID, owner string, enabled bool) (map[string]any, error) {
	if !isDecimal(accountID) || owner == "" || len(owner) > 128 {
		return nil, fmt.Errorf("租约账号或持有者无效")
	}
	if l == nil {
		return map[string]any{"active": false, "ttl": identityOnlyTTL.Seconds()}, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if !l.expiresAt.IsZero() && !now.Before(l.expiresAt) {
		l.accountID = ""
		l.owner = ""
		l.expiresAt = time.Time{}
	}
	active := l.owner != "" && l.accountID == accountID
	if !enabled {
		if l.accountID == accountID && l.owner == owner {
			l.accountID = ""
			l.owner = ""
			l.expiresAt = time.Time{}
		}
		stillActive := l.owner != "" && l.accountID == accountID && now.Before(l.expiresAt)
		return map[string]any{"active": stillActive, "ttl": identityOnlyTTL.Seconds()}, nil
	}
	if l.owner != "" && (!active || l.owner != owner) {
		return nil, fmt.Errorf("身份查询模式正由另一个官方适配器实例持有，请等待租约过期")
	}
	l.accountID = accountID
	l.owner = owner
	l.expiresAt = now.Add(identityOnlyTTL)
	return map[string]any{"active": true, "ttl": identityOnlyTTL.Seconds()}, nil
}

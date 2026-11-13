package chatsession

import (
	"crypto/md5"
	"encoding/hex"
	"strings"
)

// CalculateParams 仅用于正式创建/注册聊天流，业务模块解析已有会话时不要调用
type CalculateParams struct {
	Platform  string
	UserID    string
	GroupID   string
	AccountID string
	Scope     string
}

// CalculateSessionID 对齐 MaiBot SessionUtils.calculate_session_id
// 分量不做 TrimSpace：Python 直接拼接原值，空白也进 md5
func CalculateSessionID(p CalculateParams) (string, error) {
	if p.UserID == "" && p.GroupID == "" {
		return "", ErrMissingIdentity
	}
	var route []string
	if p.AccountID != "" {
		route = append(route, "account:"+p.AccountID)
	}
	if p.Scope != "" {
		route = append(route, "scope:"+p.Scope)
	}
	var components []string
	components = append(components, p.Platform)
	components = append(components, route...)
	if p.GroupID != "" {
		components = append(components, p.GroupID)
	} else {
		components = append(components, p.UserID, "private")
	}
	sum := md5.Sum([]byte(strings.Join(components, "_")))
	return hex.EncodeToString(sum[:]), nil
}

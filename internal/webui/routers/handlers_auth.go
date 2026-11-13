package webuiapi

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
)

const webuiTokenSourceConfigured = "configured"

func (d Deps) handleAuthVerify(w http.ResponseWriter, r *http.Request) {
	if !validateRequestBodyForRoute(w, r) {
		return
	}
	// 对齐 Python Depends(check_auth_rate_limit)：封禁 / 10 次每分钟
	if d.writeAuthRateLimited(w, r) {
		return
	}
	if d.AdminAuth == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"valid":   false,
			"message": "WebUI 鉴权未初始化",
		})
		return
	}

	token, err := parseVerifyToken(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"valid":   false,
			"message": "请求格式错误",
		})
		return
	}
	if token == "" || !d.acceptedWebUIToken(token) {
		d.writeAuthVerifyFailure(w, r)
		return
	}

	if d.AuthRateLimiter != nil {
		d.AuthRateLimiter.ResetFailures(r)
	}

	// 对齐 Python set_auth_cookie：maibot_session 直接持有访问口令本身
	// （非 ASCII 走 quote 编码，与 verify_cookie_token 的解码口径配对）
	d.AdminAuth.SetSessionCookie(w, EncodeCookieToken(token))
	firstSetup, tokenSource, requiresCustom := d.webUISetupState()
	writeJSON(w, http.StatusOK, map[string]any{
		"valid":                 true,
		"message":               "Token 验证成功",
		"is_first_setup":        firstSetup,
		"token_source":          tokenSource,
		"requires_custom_token": requiresCustom,
	})
}

// writeAuthRateLimited 对齐 check_auth_rate_limit；命中时写 429 并返回 true。
func (d Deps) writeAuthRateLimited(w http.ResponseWriter, r *http.Request) bool {
	if d.AuthRateLimiter == nil {
		return false
	}
	retryAfter, detail, limited := d.AuthRateLimiter.CheckAuthRateLimit(r)
	if !limited {
		return false
	}
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	}
	writeJSON(w, http.StatusTooManyRequests, map[string]any{"detail": detail})
	return true
}

// writeAuthVerifyFailure 对齐 verify_token 失败分支：5 次/5 分钟失败 → 封禁 10 分钟。
func (d Deps) writeAuthVerifyFailure(w http.ResponseWriter, r *http.Request) {
	message := "Token 无效或已过期"
	if d.AuthRateLimiter != nil {
		blocked, remaining := d.AuthRateLimiter.RecordFailedAttempt(r, 5, 300, 600)
		if blocked {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"detail": "认证失败次数过多，您的 IP 已被临时封禁 10 分钟",
			})
			return
		}
		if remaining <= 2 {
			message += "（剩余 " + strconv.Itoa(remaining) + " 次尝试机会）"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"valid":   false,
		"message": message,
	})
}

func (d Deps) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	d.AdminAuth.ClearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "已成功登出",
	})
}

// acceptedWebUIToken 判定令牌是否为 data/webui.json 中的访问令牌
func (d Deps) acceptedWebUIToken(token string) bool {
	return d.AdminAuth.ValidateToken(token)
}

// webUISetupState 返回首次配置状态，无法读取实例目录时回退为已完成
func (d Deps) webUISetupState() (bool, string, bool) {
	manager, err := d.webUITokenManager()
	if err != nil {
		return false, webuiTokenSourceConfigured, false
	}
	source := manager.resolveTokenSource()
	return manager.isFirstSetup(), source, source == webuiTokenSourceTemporary
}

func parseVerifyToken(r *http.Request) (string, error) {
	ct := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if strings.HasPrefix(ct, "application/json") {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			return "", err
		}
		if len(body) == 0 {
			return "", nil
		}
		var payload struct {
			Token string `json:"token"`
		}
		if err := sonic.Unmarshal(body, &payload); err != nil {
			return "", err
		}
		return strings.TrimSpace(payload.Token), nil
	}

	if err := r.ParseForm(); err != nil {
		return "", err
	}
	return strings.TrimSpace(r.FormValue("token")), nil
}




package webuiapi

import (
	"net/http"
	"strconv"
	"strings"

	botservices "maibot-go/internal/services"
)

func normalizeBotAccountPlatform(platform string) string {
	return botservices.NormalizePlatform(platform)
}

func (d Deps) requireBotAccountService(w http.ResponseWriter) (*botservices.BotAccountService, bool) {
	if d.BotAccounts == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"detail": "Bot 平台账号服务未接线",
		})
		return nil, false
	}
	return d.BotAccounts, true
}

func (d Deps) serializeBotAccount(account botservices.BotPlatformAccount) map[string]any {
	platform := normalizeBotAccountPlatform(account.Platform)
	accountID := botservices.NormalizeAccountID(account.AccountID)
	online := false
	if d.BotAccountOnline != nil {
		online = d.BotAccountOnline(platform, accountID)
	}
	var disabledAt any
	if account.DisabledAt != nil {
		disabledAt = *account.DisabledAt
	}
	return map[string]any{
		"id":                account.ID,
		"platform":          platform,
		"account_id":        accountID,
		"disabled":          account.Disabled,
		"first_seen_at":     account.FirstSeenAt,
		"last_seen_at":      account.LastSeenAt,
		"disabled_at":       disabledAt,
		"last_source":       account.LastSource,
		"last_adapter_id":   account.LastAdapterID,
		"last_plugin_id":    account.LastPluginID,
		"last_gateway_name": account.LastGatewayName,
		"online":            online,
	}
}

func (d Deps) handleBotAccountList(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	service, ok := d.requireBotAccountService(w)
	if !ok {
		return
	}
	accounts, err := service.ListAccounts(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "读取 Bot 平台账号失败: "+err.Error())
		return
	}
	data := make([]map[string]any, 0, len(accounts))
	for _, account := range accounts {
		data = append(data, d.serializeBotAccount(account))
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": data})
}

func botAccountPathID(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	raw := strings.TrimSpace(chiURLParam(r, "account_id"))
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		writeValidationError(w, "account_id 必须是正整数")
		return 0, false
	}
	return id, true
}

func (d Deps) mutateBotAccount(w http.ResponseWriter, r *http.Request, disabled bool) {
	service, ok := d.requireBotAccountService(w)
	if !ok {
		return
	}
	id, ok := botAccountPathID(w, r)
	if !ok {
		return
	}
	account, err := service.SetDisabledByID(r.Context(), id, disabled)
	if err != nil {
		if strings.Contains(err.Error(), "Bot 平台账号不存在") {
			writeAPIError(w, http.StatusNotFound, err.Error())
			return
		}
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": d.serializeBotAccount(*account)})
}

func (d Deps) handleBotAccountDisable(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	d.mutateBotAccount(w, r, true)
}

func (d Deps) handleBotAccountRestore(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	d.mutateBotAccount(w, r, false)
}

func (d Deps) handleBotAccountDelete(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	service, ok := d.requireBotAccountService(w)
	if !ok {
		return
	}
	id, ok := botAccountPathID(w, r)
	if !ok {
		return
	}
	if err := service.DeleteByID(r.Context(), id); err != nil {
		if strings.Contains(err.Error(), "Bot 平台账号不存在") {
			writeAPIError(w, http.StatusNotFound, err.Error())
			return
		}
		if strings.Contains(err.Error(), "未接线") {
			writeAPIError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

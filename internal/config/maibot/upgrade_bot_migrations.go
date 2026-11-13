package maibot

import "strings"

// upgradeMigrateExpressionSelectionMode 对齐 8.14.40：vector → vector_intent
func upgradeMigrateExpressionSelectionMode(bot *BotConfigFile) []string {
	if bot.Expression.ExpressionSelectionMode != "vector" {
		return nil
	}
	bot.Expression.ExpressionSelectionMode = "vector_intent"
	return []string{"expression.expression_selection_mode"}
}

// upgradeResetExpressionDefaults 对齐 8.14.58：重置精选限制并移除旧表达模式
func upgradeResetExpressionDefaults(bot *BotConfigFile) []string {
	var reasons []string
	e := &bot.Expression
	hadSelectionMode := hasKey(bot.LoadMeta.ExpressionKeys, "expression_selection_mode") || e.ExpressionSelectionMode != ""
	if e.ExpressionCheckedOnly || !hasKey(bot.LoadMeta.ExpressionKeys, "expression_checked_only") {
		e.ExpressionCheckedOnly = false
		reasons = append(reasons, "expression.expression_checked_only")
	}
	if hadSelectionMode {
		e.ExpressionSelectionMode = ""
		reasons = append(reasons, "expression.expression_selection_mode")
	}
	// force=false：仅当原始 TOML 未显式配置该键时写入默认 false
	if !hasKey(bot.LoadMeta.ExpressionKeys, "use_vector_expression") {
		e.UseVectorExpression = false
		reasons = append(reasons, "expression.use_vector_expression")
	}
	return reasons
}

// upgradeMovePlannerMessageSteering 对齐 8.15.37：experimental → chat.reply_timing
func upgradeMovePlannerMessageSteering(bot *BotConfigFile) []string {
	legacy := bot.Experimental.PlannerMessageSteering
	if legacy == nil {
		return nil
	}
	bot.Chat.ReplyTiming.PlannerMessageSteering = *legacy
	bot.Experimental.PlannerMessageSteering = nil
	return []string{"chat.reply_timing.planner_message_steering"}
}

// upgradeMigrateWebUIHTTPBindings 对齐 8.15.62：webui.host/port → http
func upgradeMigrateWebUIHTTPBindings(bot *BotConfigFile) []string {
	w := &bot.Webui
	if w.Host == nil && w.Port == nil {
		return nil
	}
	var reasons []string
	if w.Host != nil {
		if !hasKey(bot.LoadMeta.HTTPKeys, "host") {
			host := strings.TrimSpace(*w.Host)
			if host != "" {
				bot.Http.Host = []string{host}
			}
		}
		w.Host = nil
		reasons = append(reasons, "webui.host->http.host")
	}
	if w.Port != nil {
		if !hasKey(bot.LoadMeta.HTTPKeys, "port") {
			bot.Http.Port = *w.Port
		}
		w.Port = nil
		reasons = append(reasons, "webui.port->http.port")
	}
	if len(reasons) == 0 {
		return nil
	}
	return reasons
}

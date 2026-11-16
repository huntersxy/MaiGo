package tools

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"MaiGo/internal/chat/replyer"
	"MaiGo/internal/plugin_runtime/hooks"

	"go.uber.org/zap"
)

const hookMaisakaReplyBeforePostProcess = "maisaka.reply.before_post_process"

func toolStreamID(ctx context.Context) string {
	if tc := GetToolContext(ctx); tc != nil {
		return tc.SessionID
	}
	return ""
}

// emitReplyBeforePostProcess 对齐 MaiBot maisaka/builtin_tool/reply.py:81-110：
// 在最终可见回复执行文本后处理前触发 Hook，并读回 response 与三项后处理开关。
func emitReplyBeforePostProcess(ctx context.Context, content string, replyTo int64, input *ReplyInput) (string, replyer.PostProcessOptions, error) {
	sessionID := ""
	if tc := GetToolContext(ctx); tc != nil {
		sessionID = toolStreamID(ctx)
	}
	kwargs := map[string]any{
		"response":            content,
		"session_id":          sessionID,
		"reply_message_id":    replyMessageIDText(replyTo),
		"reply_tool_args":     replyToolArgs(input),
		"skip_post_process":   false,
		"enable_splitter":     true,
		"enable_chinese_typo": true,
	}
	result := hooks.EmitGlobal(ctx, hookMaisakaReplyBeforePostProcess, kwargs)
	if len(result.Errors) > 0 {
		zap.L().Warn("回复后处理 Hook 执行失败",
			zap.String("hook", hookMaisakaReplyBeforePostProcess),
			zap.Strings("errors", result.Errors))
	}
	replyText := content
	if result.Aborted {
		return "", replyer.PostProcessOptions{}, result.RaiseIfAborted()
	}
	if raw, ok := result.Kwargs["response"]; ok {
		replyText = strings.TrimSpace(hookStringValue(raw))
	}
	skipPostProcess, err := requireReplyHookBool(result.Kwargs, "skip_post_process")
	if err != nil {
		return "", replyer.PostProcessOptions{}, err
	}
	enableSplitter, err := requireReplyHookBool(result.Kwargs, "enable_splitter")
	if err != nil {
		return "", replyer.PostProcessOptions{}, err
	}
	enableTypo, err := requireReplyHookBool(result.Kwargs, "enable_chinese_typo")
	if err != nil {
		return "", replyer.PostProcessOptions{}, err
	}
	return replyText, replyer.PostProcessOptions{
		Skip:           skipPostProcess,
		EnableSplitter: &enableSplitter,
		EnableTypo:     &enableTypo,
	}, nil
}

// requireReplyHookBool 对齐 reply.py:73-78 _require_hook_bool 的严格布尔校验
func requireReplyHookBool(kwargs map[string]any, optionName string) (bool, error) {
	raw, ok := kwargs[optionName]
	if !ok {
		return false, fmt.Errorf("Hook 返回的 %s 必须是布尔值，实际为 NoneType", optionName)
	}
	if value, ok := raw.(bool); ok {
		return value, nil
	}
	return false, fmt.Errorf("Hook 返回的 %s 必须是布尔值，实际为 %s", optionName, pythonTypeName(raw))
}

// replyMessageIDText 对齐 str(reply_message.message_id)：缺省为空字符串
func replyMessageIDText(replyTo int64) string {
	if replyTo <= 0 {
		return ""
	}
	return strconv.FormatInt(replyTo, 10)
}

// replyToolArgs 透传 reply 工具参数（跳过内部空字段）
func replyToolArgs(input *ReplyInput) map[string]any {
	args := map[string]any{}
	if input == nil {
		return args
	}
	if value := strings.TrimSpace(input.MsgID); value != "" {
		args["msg_id"] = value
	}
	if value := strings.TrimSpace(input.ReplyReference); value != "" {
		args["reply_reference"] = value
	}
	if value := strings.TrimSpace(input.ReplyStyle); value != "" {
		args["reply_style"] = value
	}
	if len(input.ExpressionIntent) > 0 {
		args["expression_intent"] = input.ExpressionIntent
	}
	if value := strings.TrimSpace(input.Content); value != "" {
		args["content"] = value
	}
	if value := strings.TrimSpace(input.Text); value != "" {
		args["text"] = value
	}
	if input.ReplyTo != 0 {
		args["reply_to"] = input.ReplyTo
	}
	return args
}

// hookStringValue 对齐 Python str(value or "").strip()
func hookStringValue(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case bool:
		if typed {
			return "True"
		}
		return "False"
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}

// pythonTypeName 对齐 Python type(value).__name__，仅用于错误提示
func pythonTypeName(value any) string {
	switch value.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case string:
		return "str"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "int"
	case float32, float64:
		return "float"
	case []any, []string, []int64:
		return "list"
	case map[string]any:
		return "dict"
	default:
		return "object"
	}
}

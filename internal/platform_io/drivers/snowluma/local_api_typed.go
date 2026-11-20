package snowluma

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

var typedNapcatAPINames = []string{
	"adapter.napcat.account.set_qq_profile",
	"adapter.napcat.account.get_stranger_info",
	"adapter.napcat.account.get_friend_list",
	"adapter.napcat.file.get_record",
	"adapter.napcat.group.set_group_ban",
	"adapter.napcat.group.set_group_whole_ban",
	"adapter.napcat.group.set_group_kick",
	"adapter.napcat.group.set_group_kick_members",
	"adapter.napcat.group.set_group_name",
	"adapter.napcat.group.get_group_info",
	"adapter.napcat.group.get_group_detail_info",
	"adapter.napcat.group.get_group_list",
	"adapter.napcat.group.get_group_at_all_remain",
	"adapter.napcat.group.get_group_member_info",
	"adapter.napcat.group.get_group_member_list",
	"adapter.napcat.message.send_poke",
	"adapter.napcat.message.delete_msg",
	"adapter.napcat.message.send_group_ai_record",
	"adapter.napcat.message.set_msg_emoji_like",
	"adapter.napcat.message.get_msg",
	"adapter.napcat.message.get_forward_msg",
	"adapter.napcat.system.get_login_info",
}

var typedNapcatHandlers = map[string]func(context.Context, string, string, map[string]any) (any, error){}

func init() {
	typedNapcatHandlers = map[string]func(context.Context, string, string, map[string]any) (any, error){
		"adapter.napcat.account.set_qq_profile":         handleSetQQProfileAPI,
		"adapter.napcat.account.get_stranger_info":      handleGetStrangerInfoAPI,
		"adapter.napcat.account.get_friend_list":        handleGetFriendListAPI,
		"adapter.napcat.file.get_record":                handleGetRecordAPI,
		"adapter.napcat.group.set_group_ban":            handleSetGroupBanAPI,
		"adapter.napcat.group.set_group_whole_ban":      handleSetGroupWholeBanAPI,
		"adapter.napcat.group.set_group_kick":           handleSetGroupKickAPI,
		"adapter.napcat.group.set_group_kick_members":   handleSetGroupKickMembersAPI,
		"adapter.napcat.group.set_group_name":           handleSetGroupNameAPI,
		"adapter.napcat.group.get_group_info":          handleGetGroupInfoAPI,
		"adapter.napcat.group.get_group_detail_info":   handleGetGroupDetailInfoAPI,
		"adapter.napcat.group.get_group_list":          handleGetGroupListAPI,
		"adapter.napcat.group.get_group_at_all_remain": handleGetGroupAtAllRemainAPI,
		"adapter.napcat.group.get_group_member_info":   handleGetGroupMemberInfoAPI,
		"adapter.napcat.group.get_group_member_list":   handleGetGroupMemberListAPI,
		"adapter.napcat.message.send_poke":             handleSendPokeAPI,
		"adapter.napcat.message.delete_msg":            handleDeleteMsgAPI,
		"adapter.napcat.message.send_group_ai_record":  handleSendGroupAIRecordAPI,
		"adapter.napcat.message.set_msg_emoji_like":    handleSetMsgEmojiLikeAPI,
		"adapter.napcat.message.get_msg":               handleGetMsgAPI,
		"adapter.napcat.message.get_forward_msg":        handleGetForwardMsgAPI,
		"adapter.napcat.system.get_login_info":         handleGetLoginInfoAPI,
	}
}

func registerTypedLocalAPIs(registerNamed func(name, description string, public bool, handler func(context.Context, string, string, map[string]any) (any, error))) {
	descriptions := map[string]string{
		"adapter.napcat.account.set_qq_profile":         "设置 QQ 账号资料",
		"adapter.napcat.account.get_stranger_info":      "获取陌生人信息",
		"adapter.napcat.account.get_friend_list":        "获取好友列表",
		"adapter.napcat.file.get_record":                "获取语音文件详情",
		"adapter.napcat.group.set_group_ban":            "设置群成员禁言",
		"adapter.napcat.group.set_group_whole_ban":      "设置群全体禁言",
		"adapter.napcat.group.set_group_kick":           "踢出单个群成员",
		"adapter.napcat.group.set_group_kick_members":   "批量踢出群成员",
		"adapter.napcat.group.set_group_name":           "设置群名称",
		"adapter.napcat.group.get_group_info":          "获取群信息",
		"adapter.napcat.group.get_group_detail_info":   "获取群详细信息",
		"adapter.napcat.group.get_group_list":          "获取群列表",
		"adapter.napcat.group.get_group_at_all_remain": "获取群 @ 全体剩余次数",
		"adapter.napcat.group.get_group_member_info":   "获取群成员信息",
		"adapter.napcat.group.get_group_member_list":   "获取群成员列表",
		"adapter.napcat.message.send_poke":             "发送戳一戳",
		"adapter.napcat.message.delete_msg":            "撤回消息",
		"adapter.napcat.message.send_group_ai_record":  "发送群 AI 语音",
		"adapter.napcat.message.set_msg_emoji_like":    "给消息贴表情",
		"adapter.napcat.message.get_msg":               "获取消息详情",
		"adapter.napcat.message.get_forward_msg":        "获取合并转发消息",
		"adapter.napcat.system.get_login_info":         "获取登录号信息",
	}
	for _, name := range typedNapcatAPINames {
		handler := typedNapcatHandlers[name]
		registerNamed(name, descriptions[name], true, handler)
	}
}

func handleSetQQProfileAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	_, query, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	nickname, err := normalizeNonEmptyString(args["nickname"], "nickname")
	if err != nil {
		return nil, err
	}
	sex := strings.ToLower(strings.TrimSpace(asString(args["sex"])))
	if sex != "" && sex != "male" && sex != "female" && sex != "unknown" {
		return nil, fmt.Errorf("sex 必须为 male、female 或 unknown")
	}
	return query.SetQQProfile(ctx, nickname, strings.TrimSpace(asString(args["personal_note"])), sex)
}

func handleGetStrangerInfoAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	_, query, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	userID, err := normalizePositiveInt(args["user_id"], "user_id")
	if err != nil {
		return nil, err
	}
	return query.GetStrangerInfo(ctx, strconv.FormatInt(userID, 10), asBool(args["no_cache"], false))
}

func handleGetFriendListAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	_, query, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	list, err := query.GetFriendList(ctx, asBool(args["no_cache"], false))
	if err != nil {
		return nil, err
	}
	return listOrNil(list), nil
}

func handleGetRecordAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	_, query, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	fileName := strings.TrimSpace(asString(args["file"]))
	fileID := strings.TrimSpace(asString(args["file_id"]))
	outFormat := strings.TrimSpace(asString(args["out_format"]))
	if outFormat == "" {
		outFormat = "wav"
	}
	if fileName == "" && fileID == "" {
		return nil, fmt.Errorf("file 或 file_id 至少提供一个")
	}
	return query.GetRecordDetail(ctx, fileName, fileID, outFormat)
}

func handleSetGroupBanAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	_, query, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	groupID, err := normalizePositiveInt(args["group_id"], "group_id")
	if err != nil {
		return nil, err
	}
	userID, err := normalizePositiveInt(args["user_id"], "user_id")
	if err != nil {
		return nil, err
	}
	duration, err := normalizeNonNegativeInt(args["duration"], "duration")
	if err != nil {
		return nil, err
	}
	if duration > 2592000 {
		return nil, fmt.Errorf("duration 不能超过 2592000 秒")
	}
	return query.SetGroupBan(ctx, groupID, userID, duration)
}

func handleSetGroupWholeBanAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	_, query, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	groupID, err := normalizePositiveInt(args["group_id"], "group_id")
	if err != nil {
		return nil, err
	}
	enable, err := normalizeBool(args["enable"], "enable")
	if err != nil {
		return nil, err
	}
	return query.SetGroupWholeBan(ctx, groupID, enable)
}

func handleSetGroupKickAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	_, query, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	groupID, err := normalizePositiveInt(args["group_id"], "group_id")
	if err != nil {
		return nil, err
	}
	userID, err := normalizePositiveInt(args["user_id"], "user_id")
	if err != nil {
		return nil, err
	}
	return query.SetGroupKick(ctx, groupID, userID, asBool(args["reject_add_request"], false))
}

func handleSetGroupKickMembersAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	_, query, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	groupID, err := normalizePositiveInt(args["group_id"], "group_id")
	if err != nil {
		return nil, err
	}
	userIDs, err := normalizeUserIDList(args["user_id"], "user_id")
	if err != nil {
		return nil, err
	}
	return query.SetGroupKickMembers(ctx, groupID, userIDs, asBool(args["reject_add_request"], false))
}

func handleSetGroupNameAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	_, query, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	groupID, err := normalizePositiveInt(args["group_id"], "group_id")
	if err != nil {
		return nil, err
	}
	groupName, err := normalizeNonEmptyString(args["group_name"], "group_name")
	if err != nil {
		return nil, err
	}
	return query.SetGroupName(ctx, groupID, groupName)
}

func handleGetGroupInfoAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	_, query, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	groupID, err := normalizePositiveInt(args["group_id"], "group_id")
	if err != nil {
		return nil, err
	}
	return query.GetGroupInfo(ctx, strconv.FormatInt(groupID, 10), asBool(args["no_cache"], false))
}

func handleGetGroupDetailInfoAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	_, query, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	groupID, err := normalizePositiveInt(args["group_id"], "group_id")
	if err != nil {
		return nil, err
	}
	return query.GetGroupDetailInfo(ctx, strconv.FormatInt(groupID, 10))
}

func handleGetGroupListAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	_, query, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	list, err := query.GetGroupList(ctx, asBool(args["no_cache"], false))
	if err != nil {
		return nil, err
	}
	return listOrNil(list), nil
}

func handleGetGroupAtAllRemainAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	_, query, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	groupID, err := normalizePositiveInt(args["group_id"], "group_id")
	if err != nil {
		return nil, err
	}
	return query.GetGroupAtAllRemain(ctx, strconv.FormatInt(groupID, 10))
}

func handleGetGroupMemberInfoAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	_, query, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	groupID, err := normalizePositiveInt(args["group_id"], "group_id")
	if err != nil {
		return nil, err
	}
	userID, err := normalizePositiveInt(args["user_id"], "user_id")
	if err != nil {
		return nil, err
	}
	return query.GetGroupMemberInfo(ctx, strconv.FormatInt(groupID, 10), strconv.FormatInt(userID, 10), asBool(args["no_cache"], true))
}

func handleGetGroupMemberListAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	_, query, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	groupID, err := normalizePositiveInt(args["group_id"], "group_id")
	if err != nil {
		return nil, err
	}
	list, err := query.GetGroupMemberList(ctx, strconv.FormatInt(groupID, 10), asBool(args["no_cache"], false))
	if err != nil {
		return nil, err
	}
	return listOrNil(list), nil
}

func handleSendPokeAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	_, query, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	userRaw := args["user_id"]
	qqRaw := args["qq_id"]
	var resolvedUserID int64
	switch {
	case userRaw != nil && asString(userRaw) != "" && qqRaw != nil && asString(qqRaw) != "":
		userID, err := normalizePositiveInt(userRaw, "user_id")
		if err != nil {
			return nil, err
		}
		qqID, err := normalizePositiveInt(qqRaw, "qq_id")
		if err != nil {
			return nil, err
		}
		if userID != qqID {
			return nil, fmt.Errorf("user_id 与 qq_id 不能同时传递不同的值")
		}
		resolvedUserID = userID
	case userRaw != nil && asString(userRaw) != "":
		resolvedUserID, err = normalizePositiveInt(userRaw, "user_id")
		if err != nil {
			return nil, err
		}
	case qqRaw != nil && asString(qqRaw) != "":
		resolvedUserID, err = normalizePositiveInt(qqRaw, "qq_id")
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("user_id 不能为空")
	}
	var groupIDPtr *int64
	if args["group_id"] != nil && strings.TrimSpace(asString(args["group_id"])) != "" {
		groupID, err := normalizePositiveInt(args["group_id"], "group_id")
		if err != nil {
			return nil, err
		}
		groupIDPtr = &groupID
	}
	var targetIDPtr *int64
	if args["target_id"] != nil && strings.TrimSpace(asString(args["target_id"])) != "" {
		targetID, err := normalizePositiveInt(args["target_id"], "target_id")
		if err != nil {
			return nil, err
		}
		targetIDPtr = &targetID
	}
	return query.SendPoke(ctx, resolvedUserID, groupIDPtr, targetIDPtr)
}

func handleDeleteMsgAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	_, query, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	messageID, err := normalizeMessageID(args["message_id"], "message_id")
	if err != nil {
		return nil, err
	}
	return query.DeleteMessage(ctx, messageID)
}

func handleSendGroupAIRecordAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	_, query, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	groupID, err := normalizePositiveInt(args["group_id"], "group_id")
	if err != nil {
		return nil, err
	}
	character, err := normalizeNonEmptyString(args["character"], "character")
	if err != nil {
		return nil, err
	}
	text, err := normalizeNonEmptyString(args["text"], "text")
	if err != nil {
		return nil, err
	}
	return query.SendGroupAIRecord(ctx, groupID, character, text)
}

func handleSetMsgEmojiLikeAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	_, query, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	messageID, err := normalizeMessageID(args["message_id"], "message_id")
	if err != nil {
		return nil, err
	}
	emojiID, err := normalizePositiveInt(args["emoji_id"], "emoji_id")
	if err != nil {
		return nil, err
	}
	return query.SetMessageEmojiLike(ctx, messageID, emojiID, asBool(args["set"], true))
}

func handleGetMsgAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	_, query, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	messageID, err := normalizeMessageID(args["message_id"], "message_id")
	if err != nil {
		return nil, err
	}
	return query.GetMessageDetail(ctx, strconv.FormatInt(messageID, 10))
}

func handleGetForwardMsgAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	_, query, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	messageID := strings.TrimSpace(asString(args["message_id"]))
	forwardID := strings.TrimSpace(asString(args["id"]))
	if messageID != "" && forwardID != "" && messageID != forwardID {
		return nil, fmt.Errorf("message_id 与 id 不能同时传递不同的值")
	}
	if messageID == "" && forwardID == "" {
		return nil, fmt.Errorf("message_id 或 id 至少提供一个")
	}
	return query.GetForwardMessage(ctx, messageID, forwardID)
}

func handleGetLoginInfoAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	_ = args
	_, query, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	return query.GetLoginInfo(ctx)
}

func listOrNil(list []map[string]any) any {
	if list == nil {
		return nil
	}
	out := make([]any, 0, len(list))
	for _, item := range list {
		out = append(out, item)
	}
	return out
}

func normalizePositiveInt(value any, field string) (int64, error) {
	n, err := coerceInt(value)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s 必须是正整数", field)
	}
	return n, nil
}

func normalizeNonNegativeInt(value any, field string) (int64, error) {
	n, err := coerceInt(value)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s 必须是非负整数", field)
	}
	return n, nil
}

func normalizeMessageID(value any, field string) (int64, error) {
	n, err := coerceInt(value)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("%s 必须是非零整数", field)
	}
	return n, nil
}

func normalizeBool(value any, field string) (bool, error) {
	typed, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%s 必须是布尔值", field)
	}
	return typed, nil
}

func normalizeNonEmptyString(value any, field string) (string, error) {
	text := strings.TrimSpace(asString(value))
	if text == "" {
		return "", fmt.Errorf("%s 不能为空", field)
	}
	return text, nil
}

func normalizeUserIDList(value any, field string) ([]int64, error) {
	raw, ok := value.([]any)
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("%s 必须是非空数组", field)
	}
	out := make([]int64, 0, len(raw))
	for _, item := range raw {
		n, err := normalizePositiveInt(item, field)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

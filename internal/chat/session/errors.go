package chatsession

import "errors"

var (
	// ErrNotFound 目标下不存在已注册或已入库的聊天流
	ErrNotFound = errors.New("未找到真实聊天流")
	// ErrAmbiguous 同一 platform+target 匹配到多条聊天流
	ErrAmbiguous = errors.New("匹配到多条聊天流，无法唯一解析")
	// ErrInvalidTarget 平台或目标 ID 无效
	ErrInvalidTarget = errors.New("平台或目标 ID 无效")
	// ErrConversationKeyNotSessionID 误把 conversation key 当作 MaiBot session_id 写入
	ErrConversationKeyNotSessionID = errors.New("conversation key 不能作为 session_id 写入数据库")
	// ErrMissingIdentity 计算 session_id 时缺少 user_id 与 group_id
	ErrMissingIdentity = errors.New("user_id 与 group_id 必须提供其一")
)

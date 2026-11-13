package internalchat

import (
	"crypto/rand"
	"encoding/hex"
	"sync/atomic"
	"time"
)

const (
	// Platform 内部聊天平台标识
	Platform = "internal"
	// OwnerUserID 内部聊天主人账号，对齐 Python INTERNAL_CHAT_USER_ID
	OwnerUserID = "owner"
	// OwnerNumericID 内部聊天主人在本项目中的数值身份，避开 QQ 号段
	OwnerNumericID int64 = 900000000000000001
	// SelfUserID 本地平台的 Bot 账号标识，对齐 Python WEBUI_BOT_USER_ID="self"
	SelfUserID = "self"
	// maxHistory 前端可回放的历史消息条数，对齐 ChatHistoryManager
	maxHistory = 200
)

var numericSeq atomic.Int64

// NewMessageID 生成内部聊天消息 ID，格式与 Python uuid4 字符串一致
func NewMessageID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format("20060102150405.000000000")))
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return hex.EncodeToString(buf[:])
}

// NextNumericMessageID 生成内部聊天落库用的单调数值 ID
func NextNumericMessageID() int64 {
	for {
		now := time.Now().UnixNano() / 1000
		last := numericSeq.Load()
		if now <= last {
			now = last + 1
		}
		if numericSeq.CompareAndSwap(last, now) {
			return now
		}
	}
}

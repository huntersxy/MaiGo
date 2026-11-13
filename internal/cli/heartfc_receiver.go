package cli

import (
	"context"
	"fmt"

	"go.uber.org/zap"
)

// HeartFCMessageStore 消息落库端口，对应 Python MessageUtils.store_message_to_db_async
type HeartFCMessageStore interface {
	StoreMessage(ctx context.Context, message *SessionMessage) error
}

// HeartFCChat Heartflow 聊天流端口，对应 BotChatSession.register_message
type HeartFCChat interface {
	RegisterMessage(ctx context.Context, message *SessionMessage) error
}

// HeartFCHeartflow 获取或创建心流聊天流，对应 heartflow_manager.get_or_create_heartflow_chat
type HeartFCHeartflow interface {
	GetOrCreateHeartflowChat(ctx context.Context, sessionID string) (HeartFCChat, error)
}

// HeartFCMemoryAutomation 记忆自动化端口，对应 memory_automation_service.on_incoming_message
type HeartFCMemoryAutomation interface {
	OnIncomingMessage(ctx context.Context, message *SessionMessage) error
}

// HeartFCPerson Person 注册端口，对应 Person.register_person
type HeartFCPerson interface {
	RegisterPerson(ctx context.Context, person PersonRef) error
}

// PersonRef Person.register_person 参数
type PersonRef struct {
	Platform      string
	UserID        string
	Nickname      string
	GroupID       string
	GroupNickname string
}

// HeartFCDependencies 心流接收链路端口；未注入的端口按 Python 的可选分支跳过
type HeartFCDependencies struct {
	Store     HeartFCMessageStore
	Heartflow HeartFCHeartflow
	Memory    HeartFCMemoryAutomation
	Person    HeartFCPerson
	Logger    *zap.Logger
}

// HeartFCMessageReceiver 负责解析消息并送入心流/存储/记忆/人物链路
type HeartFCMessageReceiver struct {
	deps HeartFCDependencies
}

// NewHeartFCMessageReceiver 创建心流接收器
func NewHeartFCMessageReceiver(deps HeartFCDependencies) *HeartFCMessageReceiver {
	return &HeartFCMessageReceiver{deps: deps}
}

// logger 返回依赖日志或全局日志
func (r *HeartFCMessageReceiver) logger() *zap.Logger {
	if r != nil && r.deps.Logger != nil {
		return r.deps.Logger
	}
	return zap.L()
}

// ProcessMessage 执行心流接收全链；错误原样返回，调用方决定是否中断输入循环
func (r *HeartFCMessageReceiver) ProcessMessage(ctx context.Context, message *SessionMessage) error {
	if err := message.Validate(); err != nil {
		return err
	}
	if err := message.EnsureSessionID(); err != nil {
		return err
	}

	var chat HeartFCChat
	if r.deps.Heartflow != nil {
		created, err := r.deps.Heartflow.GetOrCreateHeartflowChat(ctx, message.SessionID)
		if err != nil {
			r.logger().Error("获取心流聊天流失败", zap.Error(err), zap.String("session_id", message.SessionID))
		} else {
			chat = created
		}
	}

	if r.deps.Store != nil {
		if err := r.deps.Store.StoreMessage(ctx, message); err != nil {
			return fmt.Errorf("存储消息失败: %w", err)
		}
	}

	if chat != nil {
		if err := chat.RegisterMessage(ctx, message); err != nil {
			return fmt.Errorf("注册心流消息失败: %w", err)
		}
	}

	if r.deps.Memory != nil {
		if err := r.deps.Memory.OnIncomingMessage(ctx, message); err != nil {
			return fmt.Errorf("记忆自动化处理失败: %w", err)
		}
	}

	preview, full := message.PreviewText()
	r.logger().Info(fmt.Sprintf("[%s] %s: %s", message.DisplayChatName(), message.UserInfo.UserNickname, preview))
	if preview != full {
		r.logger().Debug(fmt.Sprintf("[%s] %s 完整消息: %s", message.DisplayChatName(), message.UserInfo.UserNickname, full))
	}

	if r.deps.Person != nil {
		ref := PersonRef{
			Platform: message.Platform,
			UserID:   message.UserInfo.UserID,
			Nickname: message.UserInfo.UserNickname,
		}
		if message.GroupInfo != nil {
			ref.GroupID = message.GroupInfo.GroupID
			ref.GroupNickname = message.UserInfo.UserCardname
		}
		if err := r.deps.Person.RegisterPerson(ctx, ref); err != nil {
			return fmt.Errorf("注册人物失败: %w", err)
		}
	}
	return nil
}

// ProcessMessageSafe 对齐 Python 吞掉异常只记日志的行为
func (r *HeartFCMessageReceiver) ProcessMessageSafe(ctx context.Context, message *SessionMessage) {
	if err := r.ProcessMessage(ctx, message); err != nil {
		r.logger().Error("消息处理失败", zap.Error(err))
	}
}

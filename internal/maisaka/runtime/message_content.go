package runtime

import (
	"context"

	messagereceive "maibot-go/internal/chat/message_receive"
	"maibot-go/internal/common/media"
	"maibot-go/internal/platform_io/drivers/onebot"

	"go.uber.org/zap"
)

// prepareMessageContent 群聊与私聊复用回复补全和展示处理，原文保持独立
func (a *Agent) prepareMessageContent(ctx context.Context, msg *onebot.ConversationMessage) ([]media.Ref, []messagereceive.ForwardDraft) {
	if err := a.resolveReplyInfo(ctx, msg); err != nil {
		zap.L().Debug("解析回复消息失败", zap.String("conversation_kind", msg.ConversationKind), zap.Int64("target_id", msg.TargetID), zap.Int64("message_id", msg.MessageID), zap.Error(err))
	}
	pieces, pending, forwards := messagereceive.ParseMessageContent(ctx, msg, a.resolveMentionDisplayName)
	refs := a.cacheMedia(ctx, pending)
	a.markCollectedStickers(ctx, refs)
	session := a.sessions.get(msg.ConversationKind, msg.TargetID)
	for index := range refs {
		if refs[index].Hash == "" {
			continue
		}
		refs[index].Short = session.MediaShortHash(refs[index])
	}
	msg.FinalContent = messagereceive.RenderContentPieces(pieces, refs)
	return refs, forwards
}

// cacheMedia 下载并缓存消息里的媒体，失败的位置留空以便和展示占位对齐
func (a *Agent) cacheMedia(ctx context.Context, pending []messagereceive.PendingMedia) []media.Ref {
	if len(pending) == 0 {
		return nil
	}
	refs := make([]media.Ref, len(pending))
	for index, item := range pending {
		if ctx.Err() != nil {
			break
		}
		mediaMgr := a.currentMedia()
		if mediaMgr == nil {
			continue
		}
		ref, err := mediaMgr.Ensure(ctx, media.Source{URL: item.URL, File: item.File, Sticker: item.Sticker})
		if err != nil {
			zap.L().Debug("缓存媒体失败", zap.String("url", item.URL), zap.Error(err))
			continue
		}
		refs[index] = ref
	}
	return refs
}

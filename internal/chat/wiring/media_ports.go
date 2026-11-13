package chatwiring

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"maibot-go/internal/chat/image_system"
	"maibot-go/internal/chat/message_receive"
	"maibot-go/internal/emoji_system"
	"maibot-go/internal/person_info"
	"maibot-go/internal/services"
)

// ImagePort 接图片系统的描述缓存与 VLM 构建
type ImagePort struct {
	Manager *imagesystem.Manager
}

// DescribeImage 实现 messagereceive.ImageDescriber
func (p *ImagePort) DescribeImage(ctx context.Context, ref messagereceive.ImageRef, wait bool) (string, error) {
	if p == nil || p.Manager == nil {
		return "", errors.New("图片系统未接线")
	}
	hash := strings.TrimSpace(ref.Hash)
	content := strings.TrimSpace(ref.Content)
	var data []byte
	if content != "" && !looksLikeReference(content) {
		if decoded, err := base64.StdEncoding.DecodeString(content); err == nil {
			data = decoded
		} else {
			data = []byte(content)
		}
	}
	if hash == "" && len(data) == 0 {
		return "", errors.New("图片缺少 hash 或可识别内容，无法生成描述")
	}
	description, err := p.Manager.GetImageDescription(ctx, hash, data, wait)
	if err != nil {
		return "", fmt.Errorf("生成图片描述失败: %w", err)
	}
	return description, nil
}

// EmojiDescriptionSource 表情描述查询端口，emojisystem.Registry 实现该接口
type EmojiDescriptionSource interface {
	GetEmojiDescription(ctx context.Context, content []byte, hash string, wait bool) (emojisystem.EmojiDescription, bool, error)
}

// EmojiPort 接图片缓存与表情系统的描述能力
type EmojiPort struct {
	Registry EmojiDescriptionSource
	Images   *imagesystem.Manager
}

// DescribeEmoji 实现 messagereceive.EmojiDescriber
func (p *EmojiPort) DescribeEmoji(ctx context.Context, sessionID string, ref messagereceive.EmojiRef, wait bool) (string, error) {
	if p == nil {
		return "", errors.New("表情描述后端未接线")
	}
	hash := strings.TrimSpace(ref.Hash)
	if hash != "" && p.Images != nil {
		if description := strings.TrimSpace(p.Images.GetCachedEmojiDescription(ctx, hash)); description != "" {
			return description, nil
		}
	}
	if p.Registry == nil {
		return "", errors.New("表情描述后端未接线")
	}
	if hash == "" {
		return "", errors.New("表情缺少 hash，无法查询描述")
	}
	description, _, err := p.Registry.GetEmojiDescription(ctx, nil, hash, wait)
	if err != nil {
		return "", fmt.Errorf("生成表情描述失败: %w", err)
	}
	return strings.TrimSpace(description.Description), nil
}

// PersonPort 用 personinfo 查询昵称、群名片与群名片表
type PersonPort struct {
	Registry *personinfo.Registry
}

// LookupPerson 实现 messagereceive.PersonResolver
func (p *PersonPort) LookupPerson(ctx context.Context, platform, userID, groupID string) (*messagereceive.Person, bool) {
	if p == nil || p.Registry == nil || strings.TrimSpace(userID) == "" {
		return nil, false
	}
	profile, err := p.Registry.GetProfile(ctx, personinfo.PersonID(platform, userID))
	if err != nil || profile == nil {
		return nil, false
	}
	cards := make(map[string]string, len(profile.GroupCards))
	for _, card := range profile.GroupCards {
		cards[strings.TrimSpace(card.GroupID)] = card.GroupCardname
	}
	person := &messagereceive.Person{
		UserID:     userID,
		Nickname:   profile.Nickname,
		Cardname:   cards[strings.TrimSpace(groupID)],
		GroupCards: cards,
	}
	return person, true
}

// BotPort 用 personinfo 判断机器人自身账号并暴露昵称
type BotPort struct {
	Registry *personinfo.Registry
	Nickname string
}

// IsBotSelf 实现 messagereceive.BotIdentity
func (p *BotPort) IsBotSelf(platform, userID string) bool {
	if p == nil || p.Registry == nil {
		return false
	}
	return p.Registry.IsBotSelf(platform, userID)
}

// BotNickname 返回机器人昵称
func (p *BotPort) BotNickname() string {
	if p == nil {
		return ""
	}
	return p.Nickname
}

// ReplyPort 用消息服务查询引用消息
type ReplyPort struct {
	Messages *services.MessageService
}

// LookupReplyMessage 实现 messagereceive.ReplyLookup
func (p *ReplyPort) LookupReplyMessage(ctx context.Context, platform, sessionID, messageID string) (messagereceive.ReplyMessage, int, error) {
	if p == nil || p.Messages == nil {
		return messagereceive.ReplyMessage{}, 0, errors.New("消息服务未接线")
	}
	rows, err := p.Messages.ListMessagesByID(ctx, platform, sessionID, messageID)
	if err != nil {
		return messagereceive.ReplyMessage{}, 0, fmt.Errorf("查询引用消息失败: %w", err)
	}
	if len(rows) == 0 {
		return messagereceive.ReplyMessage{}, 0, nil
	}
	if len(rows) > 1 {
		return messagereceive.ReplyMessage{}, len(rows), nil
	}
	log := rows[0]
	content := strings.TrimSpace(log.ProcessedPlainText)
	if content == "" {
		content = strings.TrimSpace(log.ProcessedPlainText)
	}
	senderID := strings.TrimSpace(log.ExternalUserID)
	if senderID == "" && log.UserID > 0 {
		senderID = strconv.FormatInt(log.UserID, 10)
	}
	return messagereceive.ReplyMessage{
		Content: content,
		Sender: messagereceive.UserInfo{
			UserID:       senderID,
			UserNickname: strings.TrimSpace(log.UserNickname),
		},
	}, len(rows), nil
}

func looksLikeReference(value string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(trimmed, "http://") ||
		strings.HasPrefix(trimmed, "https://") ||
		strings.HasPrefix(trimmed, "base64://") ||
		strings.HasPrefix(trimmed, "file://")
}

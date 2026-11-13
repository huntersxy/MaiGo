package bootstrap

import (
	"context"
	"errors"
	"strings"

	"maibot-go/internal/chat/wiring"
	maibotcfg "maibot-go/internal/config/maibot"
)

// chatVoiceResolver 把语音媒体引用解析成语音字节
// 适配器下发的 data URI/base64/URL/本地文件由 chatwiring 默认解析器处理
type chatVoiceResolver struct {
	fallback *chatwiring.VoiceContentResolver
}

// newChatVoiceResolver 构造主链语音解析器
func newChatVoiceResolver() *chatVoiceResolver {
	return &chatVoiceResolver{fallback: chatwiring.NewVoiceContentResolver(0)}
}

// ResolveVoice 实现 chatwiring.VoiceResolver
func (r *chatVoiceResolver) ResolveVoice(ctx context.Context, hash, content string) (chatwiring.VoiceMedia, error) {
	if source := strings.TrimSpace(content); source != "" {
		return r.ResolveVoiceSource(ctx, source)
	}
	if r == nil || r.fallback == nil {
		return chatwiring.VoiceMedia{}, errors.New("语音解析器未配置")
	}
	return r.fallback.ResolveVoice(ctx, hash, content)
}

// ResolveVoiceSource 实现 chatwiring.VoiceSourceResolver
func (r *chatVoiceResolver) ResolveVoiceSource(ctx context.Context, source string) (chatwiring.VoiceMedia, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return chatwiring.VoiceMedia{}, errors.New("语音媒体引用为空")
	}
	if r == nil || r.fallback == nil {
		return chatwiring.VoiceMedia{}, errors.New("语音解析器未配置")
	}
	return r.fallback.ResolveVoiceSource(ctx, source)
}

// chatVoiceASREnabled 读取语音转写开关
func chatVoiceASREnabled(holder *maibotcfg.InstanceHolder) bool {
	if holder == nil {
		return false
	}
	cfg := holder.Config()
	return cfg != nil && cfg.Bot.Voice.EnableAsr
}

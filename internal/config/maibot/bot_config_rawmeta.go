package maibot

import (
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

func parseBotConfigLoadMeta(raw []byte) BotConfigLoadMeta {
	var doc map[string]any
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return BotConfigLoadMeta{}
	}
	chat := asMap(doc["chat"])
	return BotConfigLoadMeta{
		RootKeys:             tableKeys(doc),
		EmojiKeys:            tableKeys(asMap(doc["emoji"])),
		FilesKeys:            tableKeys(asMap(doc["files"])),
		ChatRootKeys:         tableKeys(chat),
		ChatReplyTimingKeys:  tableKeys(asMap(chat["reply_timing"])),
		ChatReplyStyleKeys:   tableKeys(asMap(chat["reply_style"])),
		ExpressionKeys:       tableKeys(asMap(doc["expression"])),
		JargonKeys:           tableKeys(asMap(doc["jargon"])),
		PersonalityKeys:      tableKeys(asMap(doc["personality"])),
		HTTPKeys:             tableKeys(asMap(doc["http"])),
		PostProcessKeys:      tableKeys(asMap(doc["response_post_process"])),
		ChineseTypoKeys:      tableKeys(asMap(doc["chinese_typo"])),
		ResponseSplitterKeys: tableKeys(asMap(doc["response_splitter"])),
	}
}

func captureJargonGroupsLegacyRaw(raw []byte, bot *BotConfigFile) {
	var doc map[string]any
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return
	}
	jargon := asMap(doc["jargon"])
	if jargon == nil {
		return
	}
	rawGroups, ok := jargon["jargon_groups"]
	if !ok {
		return
	}
	if groups, err := decodeChatStreamGroups(rawGroups); err == nil && len(groups) > 0 {
		bot.Jargon.JargonGroups = groups
		return
	}
	bot.Jargon.JargonGroupsLegacyRaw = rawGroups
}

// FileTransferBaseURL 返回 files.transfer_base_url，空值表示沿用默认本机地址
func (b BotConfigFile) FileTransferBaseURL() string {
	return strings.TrimSpace(b.Files.TransferBaseUrl)
}

// HasRootKey 判断原始 TOML 根表是否存在该键，用于区分「显式配置」与「未配置的零值」
func (m BotConfigLoadMeta) HasRootKey(key string) bool {
	return hasKey(m.RootKeys, key)
}

// TelemetryEnabled 对齐 MaiBot telemetry.enable 默认值：原始 TOML 缺 [telemetry] 段时视为启用
func (b BotConfigFile) TelemetryEnabled() bool {
	if !b.LoadMeta.HasRootKey("telemetry") {
		return true
	}
	return b.Telemetry.Enable
}

func tableKeys(table map[string]any) map[string]struct{} {
	if len(table) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(table))
	for k := range table {
		out[k] = struct{}{}
	}
	return out
}

func asMap(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return m
}

func hasKey(keys map[string]struct{}, key string) bool {
	if keys == nil {
		return false
	}
	_, ok := keys[key]
	return ok
}

// HasEmojiKey 判断 [emoji] 表是否显式包含某个键
func (m BotConfigLoadMeta) HasEmojiKey(key string) bool {
	return hasKey(m.EmojiKeys, key)
}

// HasFilesKey 判断 [files] 表是否显式包含某个键
func (m BotConfigLoadMeta) HasFilesKey(key string) bool {
	return hasKey(m.FilesKeys, key)
}

// EmojiStealEnabled 对齐 MaiBot emoji.steal_emoji 默认值：缺省启用
func (b BotConfigFile) EmojiStealEnabled() bool {
	if !b.LoadMeta.HasEmojiKey("steal_emoji") {
		return true
	}
	return b.Emoji.StealEmoji
}

// EmojiCheckInterval 维护检查间隔，缺省或非正数回退 MaiBot 默认 10 分钟
func (b BotConfigFile) EmojiCheckInterval() time.Duration {
	if !b.LoadMeta.HasEmojiKey("check_interval") || b.Emoji.CheckInterval <= 0 {
		return 10 * time.Minute
	}
	return time.Duration(b.Emoji.CheckInterval) * time.Minute
}

// EmojiMaxRegNum 可用表情上限，缺省或非正数回退 MaiBot 默认 64
func (b BotConfigFile) EmojiMaxRegNum() int {
	if !b.LoadMeta.HasEmojiKey("max_reg_num") || b.Emoji.MaxRegNum <= 0 {
		return 64
	}
	return b.Emoji.MaxRegNum
}

// EmojiDoReplace 满额后是否替换，缺省回退 MaiBot 默认 true
func (b BotConfigFile) EmojiDoReplace() bool {
	if !b.LoadMeta.HasEmojiKey("do_replace") {
		return true
	}
	return b.Emoji.DoReplace
}

// EmojiContentFiltration 注册前是否启用 VLM 内容审核，缺省关闭
func (b BotConfigFile) EmojiContentFiltration() bool {
	if !b.LoadMeta.HasEmojiKey("content_filtration") {
		return false
	}
	return b.Emoji.ContentFiltration
}

// EmojiMaxSizeBytes 收集表情大小上限；缺省 5 MiB，显式 0 表示不限
func (b BotConfigFile) EmojiMaxSizeBytes() int64 {
	if !b.LoadMeta.HasEmojiKey("max_emoji_size_mb") {
		return 5 * 1024 * 1024
	}
	if b.Emoji.MaxEmojiSizeMb <= 0 {
		return 0
	}
	return int64(b.Emoji.MaxEmojiSizeMb * 1024 * 1024)
}

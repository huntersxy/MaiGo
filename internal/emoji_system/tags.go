package emojisystem

import (
	"regexp"
	"strings"
)

var emojiTagSplit = regexp.MustCompile(`[,，、;；\r\n\t]+`)

// NormalizeEmotionTags 对齐 MaiBot _normalize_emoji_tag_text，描述字段兼作情绪标签
func NormalizeEmotionTags(raw any) []string {
	switch v := raw.(type) {
	case string:
		return dedupeTags(splitTagString(v))
	case []string:
		var out []string
		for _, item := range v {
			out = append(out, NormalizeEmotionTags(item)...)
		}
		return dedupeTags(out)
	case []any:
		// JSON 解码与插件载荷给出的都是 []any，必须逐元素递归，
		// 否则会被 default 分支用 fmt.Sprintf 拍平成单个标签
		var out []string
		for _, item := range v {
			out = append(out, NormalizeEmotionTags(item)...)
		}
		return dedupeTags(out)
	case nil:
		return nil
	default:
		// 对齐 Python：非 str/list 返回空
		return nil
	}
}

// EmojiTagsFromDescription 对齐 MaiEmoji.from_db_instance 的情绪标签口径
//
// Python 只把 ，。、 三个字符换成逗号，再按**原值**去重（不转小写），
// 与 _normalize_emoji_tag_text 的小写归一不同；这条路径只用于构造展示对象。
func EmojiTagsFromDescription(description string) []string {
	text := strings.NewReplacer("，", ",", "。", ",", "、", ",").Replace(description)
	parts := strings.Split(text, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		duplicated := false
		for _, existing := range out {
			if existing == trimmed {
				duplicated = true
				break
			}
		}
		if !duplicated {
			out = append(out, trimmed)
		}
	}
	return out
}

func splitTagString(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := emojiTagSplit.Split(s, -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func dedupeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		t := strings.TrimSpace(tag)
		if t == "" {
			continue
		}
		key := strings.ToLower(t)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, t)
	}
	return out
}

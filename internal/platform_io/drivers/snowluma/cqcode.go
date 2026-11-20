package snowluma

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	cqPattern      = regexp.MustCompile(`\[CQ:([A-Za-z0-9_]+)(,[^\[\]]*)?\]`)
	cqTextEscapes  = strings.NewReplacer("&#91;", "[", "&#93;", "]", "&amp;", "&")
	cqParamEscapes = strings.NewReplacer("&#91;", "[", "&#93;", "]", "&#44;", ",", "&amp;", "&")
)

// parseCQMessage 保留 OneBot v11 文本与消息段顺序，按 CQ 规则解码参数。
func parseCQMessage(message string) ([]any, error) {
	matches := cqPattern.FindAllStringSubmatchIndex(message, -1)
	segments := make([]any, 0, len(matches)*2+1)
	offset := 0
	for _, match := range matches {
		if offset < match[0] {
			segments = append(segments, buildTextSegment(cqTextEscapes.Replace(message[offset:match[0]])))
		}
		kind := message[match[2]:match[3]]
		params := map[string]any{}
		if match[4] >= 0 {
			for entry := range strings.SplitSeq(message[match[4]+1:match[5]], ",") {
				key, value, found := strings.Cut(entry, "=")
				if !found || key == "" || params[key] != nil {
					return nil, fmt.Errorf("无效的 CQ %s 消息段参数", kind)
				}
				params[key] = cqParamEscapes.Replace(value)
			}
		}
		segments = append(segments, map[string]any{"type": kind, "data": params})
		offset = match[1]
	}
	if offset < len(message) {
		segments = append(segments, buildTextSegment(cqTextEscapes.Replace(message[offset:])))
	}
	return segments, nil
}

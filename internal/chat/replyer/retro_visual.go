package replyer

import (
	"fmt"
	"strings"
)

const (
	retroRecentImageCount    = 3
	retroRecentImageMaxSide  = 640
	retroHistoryImageMaxSide = 128
	retroImageMarkerPrefix   = "\x00retro-image:"
	retroImageMarkerSuffix   = "\x00"
)

// retroVisualCandidate 一条历史消息里的图片/表情包出现位置
type retroVisualCandidate struct {
	messageIndex int
	partIndex    int
	emoji        bool
	image        MessageImage
}

type retroVisualImage struct {
	marker       string
	image        RequestImage
	messageIndex int
}

// buildRetroVisualRequest 对齐 _prepare_retro_visual_history：
// 只保留最后 maxImages 张；最近 3 张按 640（再受 image_quality 限制），其余 128
func (b *PromptBuilder) buildRetroVisualRequest(history []ChatMessage, maxImages int, quality string) (string, []retroVisualImage, map[int]map[int]bool) {
	if maxImages < 0 {
		maxImages = 0
	}
	candidates := collectRetroVisualCandidates(history)
	retained := candidates
	if len(retained) > maxImages {
		retained = retained[len(retained)-maxImages:]
	}
	sent := make(map[int]map[int]bool)
	retroImages := make([]retroVisualImage, 0, len(retained))
	recentStart := len(retained) - retroRecentImageCount
	if recentStart < 0 {
		recentStart = 0
	}
	qualityMaxSide := imageMaxSideForQuality(quality)
	for index, candidate := range retained {
		maxSide := retroHistoryImageMaxSide
		if index >= recentStart {
			maxSide = retroRecentImageMaxSide
		}
		if !candidate.emoji && qualityMaxSide < maxSide {
			maxSide = qualityMaxSide
		}
		encoded, ok := transcodeImage(candidate.image.Base64, maxSide)
		if !ok {
			continue
		}
		marker := retroImageMarker(candidate.messageIndex, candidate.partIndex)
		if sent[candidate.messageIndex] == nil {
			sent[candidate.messageIndex] = make(map[int]bool)
		}
		sent[candidate.messageIndex][candidate.partIndex] = true
		retroImages = append(retroImages, retroVisualImage{marker: marker, image: encoded, messageIndex: candidate.messageIndex})
	}
	return b.buildRetroDialogueWithVisual(history, sent), retroImages, sent
}

func collectRetroVisualCandidates(history []ChatMessage) []retroVisualCandidate {
	candidates := make([]retroVisualCandidate, 0)
	for messageIndex, message := range history {
		// 对齐 _prepare_retro_visual_history：所有 ContentContextMessage 都可携带图片
		if len(message.Parts) > 0 {
			for partIndex, part := range message.Parts {
				if part.Kind != PartImage && part.Kind != PartEmoji {
					continue
				}
				image := MessageImage{Format: part.ImageFormat, Base64: part.ImageBase64}
				if image.Base64 == "" {
					continue
				}
				candidates = append(candidates, retroVisualCandidate{
					messageIndex: messageIndex, partIndex: partIndex,
					emoji: part.Kind == PartEmoji, image: image,
				})
			}
			continue
		}
		for imageIndex, image := range message.Images {
			if image.Base64 == "" {
				continue
			}
			candidates = append(candidates, retroVisualCandidate{
				messageIndex: messageIndex, partIndex: imageIndex,
				image: image,
			})
		}
	}
	return candidates
}

// buildRetroDialogueWithVisual 在已发送图片位置写入占位标记，未发送图片保留文字描述
func (b *PromptBuilder) buildRetroDialogueWithVisual(history []ChatMessage, sent map[int]map[int]bool) string {
	lines := make([]string, 0, len(history))
	historyByID := make(map[string]ChatMessage, len(history))
	for messageIndex, message := range history {
		if message.MessageID != "" {
			if len(sent[messageIndex]) != 0 {
				message.Content, _ = retroMessageContentWithVisual(messageIndex, message, sent[messageIndex])
			}
			historyByID[message.MessageID] = message
		}
	}
	for messageIndex, message := range history {
		if message.IsNotify {
			continue
		}
		content, markers := retroMessageContentWithVisual(messageIndex, message, sent[messageIndex])
		rendered := ChatMessage{
			Role: message.Role, MessageID: message.MessageID, SenderName: message.SenderName,
			SenderID: message.SenderID, Platform: message.Platform, Timestamp: message.Timestamp,
			Content: content, Quotes: message.Quotes, IsNotify: message.IsNotify,
			SourceKind: message.SourceKind, VisibleReply: message.VisibleReply,
		}
		line := b.RenderRetroHistoryLine(rendered, historyByID)
		if line == "" && len(markers) == 0 {
			continue
		}
		line += strings.Join(markers, "")
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func retroMessageContentWithVisual(messageIndex int, message ChatMessage, sent map[int]bool) (string, []string) {
	if len(message.Parts) == 0 {
		markers := make([]string, 0, len(message.Images))
		for imageIndex := range message.Images {
			if sent != nil && sent[imageIndex] {
				markers = append(markers, retroImageMarker(messageIndex, imageIndex))
			}
		}
		return message.Content, markers
	}
	var text strings.Builder
	markers := make([]string, 0)
	for partIndex, part := range message.Parts {
		switch part.Kind {
		case PartText:
			text.WriteString(part.Text)
		case PartAt:
			text.WriteString(renderTargetAt(part))
		case PartReply:
			continue
		case PartImage:
			if sent != nil && sent[partIndex] {
				markers = append(markers, retroImageMarker(messageIndex, partIndex))
				text.WriteString("[图片]")
			} else {
				text.WriteString(firstNonEmpty(strings.TrimSpace(part.Description), "[图片，识别中.....]"))
			}
		case PartEmoji:
			if sent != nil && sent[partIndex] {
				markers = append(markers, retroImageMarker(messageIndex, partIndex))
				text.WriteString("[表情包]")
			} else {
				text.WriteString(firstNonEmpty(strings.TrimSpace(part.Description), "[表情包]"))
			}
		case PartVoice:
			text.WriteString(firstNonEmpty(strings.TrimSpace(part.Description), "[语音消息]"))
		}
	}
	return text.String(), markers
}

func retroImageMarker(messageIndex, partIndex int) string {
	return fmt.Sprintf("%s%d:%d%s", retroImageMarkerPrefix, messageIndex, partIndex, retroImageMarkerSuffix)
}

// splitRetroVisualParts 把渲染后的模板按图片标记切成文本/图片内容块
func splitRetroVisualParts(text string, images []retroVisualImage) []RequestPart {
	if len(images) == 0 {
		return []RequestPart{{Kind: "text", Text: text}}
	}
	byMarker := make(map[string]RequestImage, len(images))
	for _, image := range images {
		byMarker[image.marker] = image.image
	}
	parts := make([]RequestPart, 0, len(images)*2+1)
	remaining := text
	for {
		index := strings.Index(remaining, retroImageMarkerPrefix)
		if index < 0 {
			if strings.TrimSpace(remaining) != "" {
				parts = append(parts, RequestPart{Kind: "text", Text: remaining})
			}
			break
		}
		if textBefore := remaining[:index]; strings.TrimSpace(textBefore) != "" {
			parts = append(parts, RequestPart{Kind: "text", Text: textBefore})
		}
		rest := remaining[index:]
		// 前缀本身以 NUL 开头，后缀必须从前缀之后开始找
		end := strings.Index(rest[len(retroImageMarkerPrefix):], retroImageMarkerSuffix)
		if end < 0 {
			parts = append(parts, RequestPart{Kind: "text", Text: remaining})
			break
		}
		markerEnd := len(retroImageMarkerPrefix) + end + len(retroImageMarkerSuffix)
		marker := rest[:markerEnd]
		if image, ok := byMarker[marker]; ok {
			copied := image
			parts = append(parts, RequestPart{Kind: "image", Image: &copied})
		}
		remaining = rest[markerEnd:]
	}
	return parts
}

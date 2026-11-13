package llm

// 失败切换层的图片压缩：eino 消息是 chat/gemini/plugin 客户端的共同入口，
// 在这里压缩可以覆盖未在客户端内置 413 重试的 Gemini 与插件实现。

import (
	"strings"

	"github.com/cloudwego/eino/schema"
)

// compressAgenticMessages 压缩 eino 消息中的 base64 图片并返回新消息切片。
// 只复制被修改的消息与内容块，未修改的输入保持原指针。
func compressAgenticMessages(messages []*schema.AgenticMessage, targetSize int) ([]*schema.AgenticMessage, bool) {
	if len(messages) == 0 {
		return messages, false
	}
	if targetSize <= 0 {
		targetSize = defaultCompressedImageTargetSize
	}
	output := make([]*schema.AgenticMessage, len(messages))
	copy(output, messages)
	changed := false
	for messageIndex, message := range output {
		if message == nil {
			continue
		}
		var blocks []*schema.ContentBlock
		for blockIndex, block := range message.ContentBlocks {
			if block == nil {
				continue
			}
			switch {
			case block.UserInputImage != nil:
				compressed, format, ok := compressAgenticImageBase64(block.UserInputImage.Base64Data, targetSize)
				if !ok {
					continue
				}
				if blocks == nil {
					blocks = append([]*schema.ContentBlock(nil), message.ContentBlocks...)
				}
				copied := *block.UserInputImage
				copied.Base64Data = compressed
				if format != "" {
					copied.MIMEType = contextImageMIME(format)
				}
				blocks[blockIndex] = schema.NewContentBlock(&copied)
				changed = true
			case block.AssistantGenImage != nil:
				compressed, format, ok := compressAgenticImageBase64(block.AssistantGenImage.Base64Data, targetSize)
				if !ok {
					continue
				}
				if blocks == nil {
					blocks = append([]*schema.ContentBlock(nil), message.ContentBlocks...)
				}
				copied := *block.AssistantGenImage
				copied.Base64Data = compressed
				if format != "" {
					copied.MIMEType = contextImageMIME(format)
				}
				blocks[blockIndex] = schema.NewContentBlock(&copied)
				changed = true
			}
		}
		if blocks != nil {
			copied := *message
			copied.ContentBlocks = blocks
			output[messageIndex] = &copied
		}
	}
	return output, changed
}

// compressAgenticImageBase64 压缩单张图片并识别压缩后的实际格式。
func compressAgenticImageBase64(encoded string, targetSize int) (string, string, bool) {
	if strings.TrimSpace(encoded) == "" {
		return "", "", false
	}
	compressed := compressBase64Image(encoded, targetSize)
	if compressed == encoded {
		return "", "", false
	}
	format, err := chatImageFormatFromBase64(compressed)
	if err != nil {
		return compressed, "", true
	}
	return compressed, format, true
}

func chatImageFormatFromBase64(encoded string) (string, error) {
	raw, err := decodeSnapshotBase64(encoded)
	if err != nil {
		return "", err
	}
	return chatImageFormat(raw)
}

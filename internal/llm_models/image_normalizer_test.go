package llm

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func testContextImage(t *testing.T, width, height int, fill color.Color) ContextContentPart {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: fill}, image.Point{}, draw.Src)
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("编码测试图片失败: %v", err)
	}
	return ContextContentPart{
		Type:        ContextPartImage,
		ImageFormat: "png",
		ImageBase64: base64.StdEncoding.EncodeToString(buffer.Bytes()),
	}
}

func decodeTestContextImage(t *testing.T, part ContextContentPart) image.Image {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(part.ImageBase64)
	if err != nil {
		t.Fatalf("解码图片 base64 失败: %v", err)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("解码测试图片失败: %v", err)
	}
	return img
}

func TestNormalizeContextImagesKeepsRecentImages(t *testing.T) {
	older := testContextImage(t, 10, 10, color.RGBA{R: 255, A: 255})
	newer := testContextImage(t, 10, 10, color.RGBA{B: 255, A: 255})
	first := NewUserMessageItem(older)
	last := NewAssistantMessageItem("", ContextContentPart{Type: ContextPartText, Text: "描述"}, newer)

	result := normalizeContextImagesForModel([]ContextItem{first, last}, 1, 0, 0)

	if got := result[0].Parts; len(got) != 1 || got[0].Type != ContextPartText || got[0].Text != modelImageOmittedNotice {
		t.Fatalf("较早图片应替换为省略占位，实际 %#v", got)
	}
	if len(result[1].Parts) != 2 || result[1].Parts[1].ImageBase64 != newer.ImageBase64 {
		t.Fatalf("最近图片应原样保留，实际 %#v", result[1].Parts)
	}
	if first.Parts[0].Type != ContextPartImage {
		t.Fatalf("归一化不能修改原始历史 Item")
	}
}

func TestNormalizeContextImagePartObeysMinMaxSides(t *testing.T) {
	limits := imageLimits{maxSide: 100, minSide: 32}
	sizes := [][2]int{{10, 20}, {600, 400}, {20, 2000}, {2000, 20}, {1, 2000}}
	for _, size := range sizes {
		parts := normalizeContextImagePart(testContextImage(t, size[0], size[1], color.White), limits)
		if len(parts) == 0 || len(parts) > modelImageMaxSegments {
			t.Fatalf("%dx%d 分段数量越界: %d", size[0], size[1], len(parts))
		}
		for _, part := range parts {
			bounds := decodeTestContextImage(t, part).Bounds()
			if short := min(bounds.Dx(), bounds.Dy()); short < limits.minSide {
				t.Fatalf("%dx%d 片段短边 %d 小于下限 %d", size[0], size[1], short, limits.minSide)
			}
			if long := max(bounds.Dx(), bounds.Dy()); long > limits.maxSide {
				t.Fatalf("%dx%d 片段长边 %d 超过上限 %d", size[0], size[1], long, limits.maxSide)
			}
		}
	}
}

func TestNormalizeContextImagePartPreservesCompliantImage(t *testing.T) {
	part := testContextImage(t, 60, 80, color.White)
	parts := normalizeContextImagePart(part, imageLimits{maxSide: 100, minSide: 32})
	if len(parts) != 1 || parts[0].ImageBase64 != part.ImageBase64 {
		t.Fatalf("尺寸合规图片应保持原始字节")
	}
}

func TestNormalizeContextImagePartSegmentsLongImage(t *testing.T) {
	parts := normalizeContextImagePart(testContextImage(t, 200, 2000, color.White), imageLimits{maxSide: 100, minSide: 1})
	if len(parts) <= 1 || len(parts) > modelImageMaxSegments {
		t.Fatalf("长图应分段且不超过 %d 段，实际 %d", modelImageMaxSegments, len(parts))
	}
	for _, part := range parts {
		bounds := decodeTestContextImage(t, part).Bounds()
		if max(bounds.Dx(), bounds.Dy()) > 100 {
			t.Fatalf("长图片段长边 %d 超过上限 100", max(bounds.Dx(), bounds.Dy()))
		}
	}
}

func TestNormalizeContextImagesCountsGeneratedSegments(t *testing.T) {
	item := NewUserMessageItem(testContextImage(t, 20, 2000, color.White))
	result := normalizeContextImagesForModel([]ContextItem{item}, 2, 100, 1)

	imageCount := 0
	placeholderCount := 0
	for _, part := range result[0].Parts {
		switch {
		case part.Type == ContextPartImage:
			imageCount++
		case part.Type == ContextPartText && part.Text == modelImageOmittedNotice:
			placeholderCount++
		}
	}
	if imageCount != 2 {
		t.Fatalf("数量上限应按切片计数并保留最近 2 张，实际 %d", imageCount)
	}
	if placeholderCount == 0 {
		t.Fatalf("超出上限的切片应替换为省略占位")
	}
	if item.Parts[0].Type != ContextPartImage {
		t.Fatalf("归一化不能修改原始历史 Item")
	}
}

func TestNormalizeContextImagesInvalidatesAssistantReplay(t *testing.T) {
	replay, err := NewContextProviderReplayFragment(ContextProviderScope{
		SchemaVersion:       1,
		ClientType:          "openai",
		ProviderName:        "provider",
		EndpointFingerprint: "endpoint",
		ModelIdentifier:     "model",
	}, map[string]any{"content": "original"})
	if err != nil {
		t.Fatalf("构造回放片段失败: %v", err)
	}
	original := NewAssistantMessageItem("", testContextImage(t, 200, 200, color.White))
	original.Replay = &replay

	result := normalizeContextImagesForModel([]ContextItem{original}, 0, 100, 1)
	if result[0].Replay != nil {
		t.Fatalf("图片被改写后应清除助手回放片段")
	}
	if original.Replay == nil {
		t.Fatalf("归一化不能修改原始历史 Item")
	}
}

func TestNormalizeAgenticMessageImagesAppliesModelLimits(t *testing.T) {
	imagePart := testContextImage(t, 200, 200, color.White)
	first := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.UserInputImage{Base64Data: imagePart.ImageBase64, MIMEType: "image/png"}),
		},
	}
	second := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.UserInputImage{Base64Data: imagePart.ImageBase64, MIMEType: "image/png"}),
		},
	}
	result := normalizeAgenticMessageImages([]*schema.AgenticMessage{first, second}, 1, 100, 1)

	if result[0].ContentBlocks[0].UserInputImage != nil || result[0].ContentBlocks[0].UserInputText == nil {
		t.Fatalf("较早消息图片应替换为文本占位")
	}
	kept := result[1].ContentBlocks[0].UserInputImage
	if kept == nil {
		t.Fatalf("最近消息图片应保留")
	}
	bounds := decodeTestContextImage(t, ContextContentPart{Type: ContextPartImage, ImageFormat: "png", ImageBase64: kept.Base64Data}).Bounds()
	if max(bounds.Dx(), bounds.Dy()) > 100 {
		t.Fatalf("消息图片长边 %d 超过上限 100", max(bounds.Dx(), bounds.Dy()))
	}
	if first.ContentBlocks[0].UserInputImage == nil {
		t.Fatalf("归一化不能修改原始消息")
	}
}

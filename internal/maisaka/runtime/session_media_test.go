package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"MaiGo/internal/common/media"
)

func TestResolveDeferredMediaBlocksReadsOnlyKeptImages(t *testing.T) {
	dir := t.TempDir()
	pngPath := filepath.Join(dir, "a.png")
	// 最小 PNG 头足以走通读盘分支
	if err := os.WriteFile(pngPath, []byte("\x89PNG\r\n\x1a\n"), 0o600); err != nil {
		t.Fatalf("写入测试图片失败: %v", err)
	}
	marker := deferredMediaMarkerPrefix + pngPath
	message := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.UserInputText{Text: "hi"}),
			schema.NewContentBlock(&schema.UserInputImage{Base64Data: marker, MIMEType: "image/png"}),
		},
	}

	resolved := resolveDeferredMediaBlocks([]*schema.AgenticMessage{message})
	if len(resolved) != 1 || resolved[0] == message {
		t.Fatal("含惰性块的消息应返回副本")
	}
	image := resolved[0].ContentBlocks[1].UserInputImage
	if strings.HasPrefix(image.Base64Data, deferredMediaMarkerPrefix) {
		t.Fatal("惰性标记应被替换为真实 base64")
	}
	if image.Base64Data == "" {
		t.Fatal("解析结果不应为空")
	}
	// 原消息不能被改写，否则 base64 会沉淀到会话工作集
	original := message.ContentBlocks[1].UserInputImage.Base64Data
	if original != marker {
		t.Fatalf("原消息不应被修改: %q", original)
	}
}

func TestResolveDeferredMediaBlocksDropsMissingFile(t *testing.T) {
	marker := deferredMediaMarkerPrefix + filepath.Join(t.TempDir(), "missing.png")
	message := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.UserInputImage{Base64Data: marker}),
		},
	}
	resolved := resolveDeferredMediaBlocks([]*schema.AgenticMessage{message})
	if len(resolved[0].ContentBlocks) != 0 {
		t.Fatalf("缺失文件应丢弃该图片块，实际剩余 %d 块", len(resolved[0].ContentBlocks))
	}
}

func TestAttachDeferredMediaBlocksKeepsMarker(t *testing.T) {
	ref := media.Ref{Path: filepath.Join(t.TempDir(), "b.png"), Mime: "image/png"}
	message := &schema.AgenticMessage{
		Role:          schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.UserInputText{Text: "hi"})},
	}
	got := attachDeferredMediaBlocks(message, []media.Ref{ref})
	if len(got.ContentBlocks) != 2 {
		t.Fatalf("应补一个惰性图片块，实际 %d", len(got.ContentBlocks))
	}
	image := got.ContentBlocks[1].UserInputImage
	if image == nil || !strings.HasPrefix(image.Base64Data, deferredMediaMarkerPrefix) {
		t.Fatalf("应为惰性标记: %#v", image)
	}
	if len(message.ContentBlocks) != 1 {
		t.Fatal("原消息不应被修改")
	}
}

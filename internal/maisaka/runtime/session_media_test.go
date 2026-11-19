package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"MaiGo/internal/common/media"
	"MaiGo/internal/maisaka"
)

func TestResolveDeferredMediaBlocksReadsOnlyKeptImages(t *testing.T) {
	dir := t.TempDir()
	pngPath := filepath.Join(dir, "a.png")
	// 最小 PNG 头足以走通读盘分支
	if err := os.WriteFile(pngPath, []byte("\x89PNG\r\n\x1a\n"), 0o600); err != nil {
		t.Fatalf("写入测试图片失败: %v", err)
	}
	marker := maisaka.DeferredMediaMarkerPrefix + pngPath
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
	if strings.HasPrefix(image.Base64Data, maisaka.DeferredMediaMarkerPrefix) {
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
	marker := maisaka.DeferredMediaMarkerPrefix + filepath.Join(t.TempDir(), "missing.png")
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
	if image == nil || !strings.HasPrefix(image.Base64Data, maisaka.DeferredMediaMarkerPrefix) {
		t.Fatalf("应为惰性标记: %#v", image)
	}
	if len(message.ContentBlocks) != 1 {
		t.Fatal("原消息不应被修改")
	}
}

func TestUserSchemaMessageUsesDeferredMarker(t *testing.T) {
	ref := media.Ref{Path: filepath.Join(t.TempDir(), "live.png"), Mime: "image/png"}
	message := userSchemaMessage("hello", []media.Ref{ref})
	if message == nil || len(message.ContentBlocks) != 2 {
		t.Fatalf("应含文本与图片块，实际 %#v", message)
	}
	image := message.ContentBlocks[1].UserInputImage
	if image == nil || image.Base64Data != maisaka.DeferredMediaMarkerPrefix+ref.Path {
		t.Fatalf("在线写入应为 deferred 占位: %#v", image)
	}
}

func TestInternalTaskMessageKeepsDeferredMarker(t *testing.T) {
	ref := media.Ref{Path: filepath.Join(t.TempDir(), "task.png"), Mime: "image/jpeg", Hash: "abc"}
	message, err := internalTaskMessage("task", "notify", "reason", []media.Ref{ref}, time.Unix(1, 0))
	if err != nil {
		t.Fatalf("组装内部任务失败: %v", err)
	}
	if len(message.ContentBlocks) != 2 {
		t.Fatalf("应含文本与图片块，实际 %d", len(message.ContentBlocks))
	}
	image := message.ContentBlocks[1].UserInputImage
	if image == nil || image.Base64Data != maisaka.DeferredMediaMarkerPrefix+ref.Path {
		t.Fatalf("内部任务应保留 deferred 占位: %#v", image)
	}
	if source, _ := message.Extra["source"].(string); source != "internal_task" {
		t.Fatalf("source 应为 internal_task: %#v", message.Extra)
	}
}

func TestAppendMediaImageBlocksUsesDeferredMarker(t *testing.T) {
	ref := media.Ref{Path: filepath.Join(t.TempDir(), "proactive.png"), Mime: "image/png"}
	payload := &schema.AgenticMessage{
		Role:          schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.UserInputText{Text: "hi"})},
	}
	appendMediaImageBlocks(payload, []media.Ref{ref})
	if len(payload.ContentBlocks) != 2 {
		t.Fatalf("应补一个图片块，实际 %d", len(payload.ContentBlocks))
	}
	image := payload.ContentBlocks[1].UserInputImage
	if image == nil || !strings.HasPrefix(image.Base64Data, maisaka.DeferredMediaMarkerPrefix) {
		t.Fatalf("主动上下文应为 deferred 占位: %#v", image)
	}
}

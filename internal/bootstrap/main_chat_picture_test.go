package bootstrap

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"MaiGo/internal/chat/debounce"
	"MaiGo/internal/chat/image_system"
	"MaiGo/internal/common/database"
	"MaiGo/internal/common/database/migrations"
	"MaiGo/internal/platform_io/drivers/onebot"
	"MaiGo/internal/platform_io/drivers/snowluma"
	"MaiGo/internal/plugin_runtime/hostports"
)

type waitingImageDescriber struct {
	started chan struct{}
	release chan struct{}
}

func (*waitingImageDescriber) Available() bool { return true }

func (d *waitingImageDescriber) Describe(ctx context.Context, _ []byte, _ string) (string, error) {
	close(d.started)
	select {
	case <-d.release:
		return "测试图片", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestSnowLumaImageOnlyDispatchDoesNotWaitForVLM(t *testing.T) {
	root := t.TempDir()
	db, err := memory.OpenDB(filepath.Join(root, "MaiBot.db"))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := migration.RunMigrations(db); err != nil {
		t.Fatal(err)
	}
	describer := &waitingImageDescriber{started: make(chan struct{}), release: make(chan struct{})}
	images, err := imagesystem.NewManager(imagesystem.ManagerOptions{
		Root: root, Store: imagesystem.NewGORMStore(db), Describer: describer,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		close(describer.release)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := images.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})

	dispatched := make(chan *onebot.ConversationMessage, 1)
	pipeline := &chatRuntimePipeline{
		images:  newChatImagePreprocessor(images),
		handler: func(msg *onebot.ConversationMessage) { dispatched <- msg },
	}
	gateway := hostports.NewGateway(nil, nil)
	gateway.SetInboundHandler(pipeline.HandlePluginMessage)
	const gif = "R0lGODlhAQABAIAAAAUEBAAAACwAAAAAAQABAAACAkQBADs="
	event := snowluma.NormalizeInboundMessage(map[string]any{
		"message_type": "private", "message_id": "1", "user_id": "100",
		"message": []any{
			map[string]any{"type": "image", "data": map[string]any{"sub_type": 0, "file": "base64://" + gif}},
		},
	}, "200", "")

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- gateway.ReceiveExternalMessage(ctx, event) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("image-only message was blocked by visual recognition")
	}
	select {
	case msg := <-dispatched:
		if len(msg.Images) != 1 || msg.Images[0].URL != "data:image/gif;base64,"+gif {
			t.Fatalf("expected one inlined image, got %+v", msg.Images)
		}
		const originalHash = "90252ef0aa9e3d36c861bbeaa1bd57b7f855333edf2957ab9473838a52e2dd7f"
		if got := msg.Images[0].RecognitionHash; got != originalHash {
			t.Fatalf("recognition hash = %q, want %q", got, originalHash)
		}
		if bare, emoji := debounce.ShapeFromMessage(msg); !bare || emoji {
			t.Fatalf("expected bare image despite adapter placeholder, got bare=%t emoji=%t", bare, emoji)
		}
	default:
		t.Fatal("image-only message was not delivered to the Agent")
	}
	select {
	case <-describer.started:
	case <-ctx.Done():
		t.Fatal("visual recognition was not scheduled")
	}
}

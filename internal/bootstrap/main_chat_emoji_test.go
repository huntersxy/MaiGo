package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"MaiGo/internal/chat/image_system"
	"MaiGo/internal/common/database"
	"MaiGo/internal/common/database/migrations"
	"MaiGo/internal/emoji_system"
	"MaiGo/internal/platform_io/drivers/onebot"
	"MaiGo/internal/platform_io/drivers/snowluma"
	"MaiGo/internal/plugin_runtime/hostports"
)

func TestSnowLumaStickerReachesLearningList(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MAIBOT_INSTANCE_DIR", root)
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
	mem, err := memory.NewManager(db, filepath.Join(root, "media"))
	if err != nil {
		t.Fatal(err)
	}
	registry := emojisystem.DefaultRegistry()
	registry.SetMemory(mem)
	registry.SetDescribeDeps(filepath.Join(root, "stickers"), nil, nil)
	t.Cleanup(func() { registry.SetManager(nil) })

	images, err := imagesystem.NewManager(imagesystem.ManagerOptions{Root: root, Store: imagesystem.NewGORMStore(db)})
	if err != nil {
		t.Fatal(err)
	}
	var dispatched *onebot.ConversationMessage
	pipeline := &chatRuntimePipeline{
		images:  newChatImagePreprocessor(images),
		handler: func(_ context.Context, msg *onebot.ConversationMessage) { dispatched = msg },
	}
	gateway := hostports.NewGateway(nil, nil)
	gateway.SetInboundHandler(pipeline.HandlePluginMessage)
	const gif = "R0lGODlhAQABAIAAAAUEBAAAACwAAAAAAQABAAACAkQBADs="
	event := snowluma.NormalizeInboundMessage(map[string]any{
		"message_type": "private", "message_id": "1", "user_id": "100",
		"message": []any{
			map[string]any{"type": "image", "data": map[string]any{"subType": 1, "url": "data:image/gif;base64," + gif}},
		},
	}, "200", "")
	if err := gateway.ReceiveExternalMessage(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if dispatched == nil || len(dispatched.Images) != 1 || dispatched.Images[0].SubType != 1 {
		t.Fatalf("sticker was not delivered to the Agent: %+v", dispatched)
	}
	page, err := registry.WebUI().List(t.Context(), emojisystem.WebUIEmojiQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Status != "unknown" {
		t.Fatalf("expected one new sticker in the learning list, got %+v", page)
	}
}

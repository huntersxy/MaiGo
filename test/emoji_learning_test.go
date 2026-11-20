package test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"MaiGo/internal/common/database"
	"MaiGo/internal/common/database/migrations"
	"MaiGo/internal/emoji_system"
	"MaiGo/internal/platform_io/drivers/snowluma"
)

func TestFirstSeenEmojiAppearsInLearningList(t *testing.T) {
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
	manager, err := memory.NewManager(db, filepath.Join(root, "media"))
	if err != nil {
		t.Fatal(err)
	}
	registry := &emojisystem.Registry{}
	registry.SetMemory(manager)
	registry.SetDescribeDeps(filepath.Join(root, "stickers"), nil, nil)
	image, err := base64.StdEncoding.DecodeString("R0lGODlhAQABAIAAAAUEBAAAACwAAAAAAQABAAACAkQBADs=")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, _, err := registry.GetEmojiDescriptionForSession(t.Context(), image, "", false, "chat"); err != nil {
			t.Fatal(err)
		}
	}
	service := registry.WebUI()
	page, err := service.List(t.Context(), emojisystem.WebUIEmojiQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Status != "unknown" {
		t.Fatalf("expected one unreviewed emoji, got %+v", page)
	}
	item, err := registry.Manager().GetByHash(t.Context(), page.Items[0].Hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(registry.Manager().EmojiFilePath(item)); err != nil {
		t.Fatal(err)
	}
}

func TestSnowLumaKeepsStickerImageSubtype(t *testing.T) {
	message := snowluma.NormalizeInboundMessage(map[string]any{
		"message_type": "private", "message_id": "1", "user_id": "100",
		"message": []any{
			map[string]any{"type": "image", "data": map[string]any{"subType": float64(1), "url": "https://example.com/sticker.gif"}},
			map[string]any{"type": "image", "data": map[string]any{"sub_type": "0", "url": "https://example.com/photo.png"}},
		},
	}, "200", "")
	if message["is_emoji"] != true || message["is_picture"] != true {
		t.Fatalf("incorrect media flags: %+v", message)
	}
	segments := message["raw_message"].([]any)
	if segments[0].(map[string]any)["type"] != "emoji" || segments[1].(map[string]any)["type"] != "image" {
		t.Fatalf("incorrect media segment types: %+v", segments)
	}
}

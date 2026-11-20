package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"MaiGo/internal/chat/image_system"
	"MaiGo/internal/common/database"
	"MaiGo/internal/common/media"
	"MaiGo/internal/maisaka"
	"MaiGo/internal/maisaka/visual"
	"MaiGo/internal/plugin_runtime/hooks"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type recognitionHashLookup struct {
	originalHash     string
	description      string
	emojiDescription string
	emojiPending     bool
}

type plannerMediaCapture struct {
	model.AgenticModel
	messages []*schema.AgenticMessage
}

func (m *plannerMediaCapture) Generate(_ context.Context, messages []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
	m.messages = messages
	return &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.AssistantGenText{Text: "收到"})},
	}, nil
}

type plannerEditHook struct {
	t         *testing.T
	mediaData string
}

func (h plannerEditHook) EmitHook(_ context.Context, name string, kwargs map[string]any) hooks.DispatchResult {
	if name != "maisaka.planner.before_request" {
		return hooks.DispatchResult{Kwargs: kwargs}
	}
	wire, err := json.Marshal(kwargs["items"])
	if err != nil || bytes.Contains(wire, []byte(h.mediaData)) {
		h.t.Fatalf("invalid Hook payload or leaked image bytes")
	}
	var items []any
	if err := json.Unmarshal(wire, &items); err != nil {
		h.t.Fatal(err)
	}
	items[1], items[2] = items[2], items[1]
	items[1].(map[string]any)["text"] = "Hook 修改过的表情文字"
	kwargs["items"] = items
	return hooks.DispatchResult{Kwargs: kwargs}
}

func TestPlannerHookKeepsMediaInTheModelRequest(t *testing.T) {
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	picture := base64.StdEncoding.EncodeToString(pngData.Bytes())
	const gif = "R0lGODlhAQABAIAAAAUEBAAAACwAAAAAAQABAAACAkQBADs="
	history := []maisaka.ContextMessage{
		{Role: maisaka.RoleUser, Content: "普通图片", Images: []maisaka.ImageComponent{{MediaRef: picture}}, CountInContext: true},
		{Role: maisaka.RoleUser, Content: "表情消息", Emojis: []maisaka.ImageComponent{{MediaRef: gif}}, CountInContext: true},
	}
	previous := hooks.GlobalEmitter()
	t.Cleanup(func() { hooks.SetGlobalEmitter(previous) })
	for _, tc := range []struct {
		name    string
		edit    bool
		sticker string
	}{
		{name: "no subscriber", sticker: "表情消息"},
		{name: "Hook edits and reorders items", edit: true, sticker: "Hook 修改过的表情文字"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var emitter hooks.Emitter
			if tc.edit {
				emitter = plannerEditHook{t: t, mediaData: gif}
			}
			hooks.SetGlobalEmitter(emitter)
			capture := &plannerMediaCapture{}
			loop := maisaka.NewChatLoopService("private-session", false, nil, &maisakaChatClient{model: capture})
			if _, err := loop.ChatLoopStep(t.Context(), history, "planner", 20, nil, "测试人格", "", nil, nil); err != nil {
				t.Fatal(err)
			}
			mediaByText := map[string]string{}
			for _, message := range capture.messages {
				var text string
				for _, block := range message.ContentBlocks {
					if block.UserInputText != nil {
						text = block.UserInputText.Text
					}
					if block.UserInputImage != nil {
						mediaByText[strings.SplitN(text, "\n", 2)[0]] = block.UserInputImage.Base64Data
					}
				}
			}
			if mediaByText["普通图片"] != picture || mediaByText[tc.sticker] == "" || mediaByText[tc.sticker] == picture {
				t.Fatal("the model request lost or misplaced its image or sticker")
			}
		})
	}
}

func (l recognitionHashLookup) IsVLMTaskConfigured() bool { return true }

func (l recognitionHashLookup) LookupCachedImageDescription(hash string) string {
	if hash == l.originalHash {
		return l.description
	}
	return ""
}

func (l recognitionHashLookup) LookupCachedEmojiDescription(ref string) string {
	if ref == l.originalHash {
		return l.emojiDescription
	}
	return ""
}

func (l recognitionHashLookup) IsImageDescriptionPending(hash string) bool {
	return hash == l.originalHash && l.description == ""
}

func (l recognitionHashLookup) IsEmojiDescriptionPending(ref string) bool {
	return ref == l.originalHash && l.emojiPending
}

func TestTranscodedImageDescriptionReachesTextPlanner(t *testing.T) {
	transcoded := []byte("transcoded image bytes")
	path := filepath.Join(t.TempDir(), "picture.webp")
	if err := os.WriteFile(path, transcoded, 0o600); err != nil {
		t.Fatal(err)
	}
	originalHash := fmt.Sprintf("%x", sha256.Sum256([]byte("original image bytes")))
	refs := []media.Ref{{Path: path, Mime: "image/webp", RecognitionHash: originalHash}}
	const messageText = "用户消息 [图片:abc123]"
	messages := []*schema.AgenticMessage{
		userSchemaMessage(messageText, refs),
		attachDeferredMediaBlocks(schema.UserAgenticMessage(messageText), refs),
	}
	for _, message := range messages {
		history, ok := agenticMessageToMaisaka(message, 1)
		if !ok || len(history) != 1 || len(history[0].Images) != 1 {
			t.Fatalf("missing image: %+v", history)
		}
		lookup := recognitionHashLookup{originalHash: originalHash}
		refresher := visual.Refresher{Lookup: lookup}
		if !refresher.HasPendingImageRecognition(history) {
			t.Fatal("planner should wait on the original image recognition")
		}
		lookup.description = "一张蓝色海面的照片"
		refresher.Lookup = lookup
		if got := refresher.RefreshChatHistoryVisualPlaceholders(history); got != 1 {
			t.Fatalf("refreshed %d history messages, want 1", got)
		}
		text := maisaka.BuildContextItemsFromHistoryEntry(history[0], false)[0]
		if !strings.Contains(text.Text, lookup.description) || len(text.Images) != 0 {
			t.Fatalf("text planner input is missing the description: %+v", text)
		}
		multimodal := maisaka.BuildContextItemsFromHistoryEntry(history[0], true)[0]
		if len(multimodal.Images) != 1 || multimodal.Images[0].MediaRef != base64.StdEncoding.EncodeToString(transcoded) {
			t.Fatalf("visual planner input lost the cached image: %+v", multimodal)
		}
	}
}

func TestInboundStickerReachesPlannerAndReplyer(t *testing.T) {
	const gif = "R0lGODlhAQABAIAAAAUEBAAAACwAAAAAAQABAAACAkQBADs="
	sticker, err := base64.StdEncoding.DecodeString(gif)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sticker.gif")
	if err := os.WriteFile(path, sticker, 0o600); err != nil {
		t.Fatal(err)
	}
	refs := []media.Ref{{Path: path, Mime: "image/gif", Sticker: true}}
	const messageText = "[表情包:653b6efe]再试试"
	for _, message := range []*schema.AgenticMessage{
		userSchemaMessage(messageText, refs),
		attachDeferredMediaBlocks(schema.UserAgenticMessage(messageText), refs),
	} {
		history, ok := agenticMessageToMaisaka(message, 1)
		if !ok || len(history) != 1 || len(history[0].Emojis) != 1 || len(history[0].Images) != 0 {
			t.Fatal("the sticker lost its media type")
		}
		lookup := recognitionHashLookup{originalHash: gif, emojiPending: true}
		refresher := visual.Refresher{Lookup: lookup}
		if !refresher.HasPendingImageRecognition(history) {
			t.Fatal("the text planner should wait for the pending sticker description")
		}
		lookup.emojiDescription = "惊讶的小猫"
		lookup.emojiPending = false
		refresher.Lookup = lookup
		if got := refresher.RefreshChatHistoryVisualPlaceholders(history); got != 1 || refresher.HasPendingImageRecognition(history) {
			t.Fatalf("sticker description did not replace its pending state, refreshed %d messages", got)
		}
		text := maisaka.BuildContextItemsFromHistoryEntry(history[0], false)[0]
		if !strings.Contains(text.Text, messageText) || !strings.Contains(text.Text, lookup.emojiDescription) {
			t.Fatalf("the text planner lost the sticker description or handle: %s", text.Text)
		}
		multimodal := maisaka.BuildContextItemsFromHistoryEntry(history[0], true)[0]
		if blocks := maisakaItemMediaBlocks(multimodal, false, 0); len(blocks) != 1 || blocks[0].UserInputImage == nil {
			t.Fatal("the visual planner did not receive an image block")
		}
		replies := replyerHistoryMessages(history)
		if len(replies) != 1 || len(replies[0].Parts) != 2 || !strings.Contains(replies[0].Parts[1].Description, lookup.emojiDescription) {
			t.Fatal("the replyer did not receive the sticker's description")
		}
	}
}

type stickerCaptionDescriber struct {
	release <-chan struct{}
}

func (stickerCaptionDescriber) Available() bool { return true }

func (d stickerCaptionDescriber) Describe(_ context.Context, data []byte, format string) (string, error) {
	<-d.release
	if _, err := png.DecodeConfig(bytes.NewReader(data)); err != nil || format != "png" {
		return "", fmt.Errorf("caption model did not receive the cached sticker PNG")
	}
	return "黄色小狗戴着蓝帽子", nil
}

func TestTextPlannerPrefersStickerCaptionToLearningTags(t *testing.T) {
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
	store := imagesystem.NewGORMStore(db)
	if err := store.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	var input bytes.Buffer
	if err := png.Encode(&input, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	const cachedPath = "sticker.png"
	path := filepath.Join(root, cachedPath)
	if err := os.WriteFile(path, input.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(input.Bytes()))
	const tags = "playful,cute"
	if err := store.Create(t.Context(), &imagesystem.ImageRecord{
		FullPath: cachedPath, ImageHash: hash, ImageType: imagesystem.ImageTypeEmoji, Description: tags, RecordTime: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	defer close(release)
	manager, err := imagesystem.NewManager(imagesystem.ManagerOptions{
		Root: root, Store: store, Describer: stickerCaptionDescriber{release: release},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	bridge := &agentVisualBridge{manager: manager}
	if got := bridge.LookupCachedEmojiDescription(hash); got != tags {
		t.Fatalf("got %q for an emoji with only tags", got)
	}
	if got := bridge.scheduleStickerCaption(t.Context(), path); got != hash || !bridge.IsEmojiDescriptionPending(hash) {
		t.Fatal("sticker caption was not scheduled by the image manager")
	}
	if got := bridge.LookupCachedEmojiDescription(hash); got != "" {
		t.Fatalf("returned %q before the pending caption completed", got)
	}
	release <- struct{}{}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	history := []maisaka.ContextMessage{{
		Role: maisaka.RoleUser, Content: "[表情包:03f4f00a]", Emojis: []maisaka.ImageComponent{{RecognitionHash: hash}},
	}}
	refresher := visual.Refresher{Lookup: bridge}
	refresher.RefreshChatHistoryVisualPlaceholders(history)
	request := maisaka.BuildContextItemsFromHistoryEntry(history[0], false)[0]
	if !strings.Contains(request.Text, "黄色小狗戴着蓝帽子") || strings.Contains(request.Text, tags) {
		t.Fatalf("unexpected text planner context %q", request.Text)
	}
	if got := manager.GetCachedEmojiDescription(t.Context(), hash); got != tags {
		t.Fatalf("the emoji learning tags changed to %q", got)
	}
}

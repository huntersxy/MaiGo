//go:build rlottie && cgo

package test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"image/gif"
	"net/http"
	"net/http/httptest"
	"path"
	"sync/atomic"
	"testing"
	"time"

	"MaiGo/internal/platform_io/drivers/telegram"
)

func TestNativeAdapterTelegramTGS(t *testing.T) {
	const model = `{"v":"5.7.4","fr":30,"ip":0,"op":5,"w":16,"h":16,"nm":"test","ddd":0,"assets":[],"layers":[{"ddd":0,"ind":1,"ty":1,"nm":"Solid","sr":1,"ks":{"o":{"a":0,"k":100},"r":{"a":0,"k":0},"p":{"a":0,"k":[8,8,0]},"a":{"a":0,"k":[8,8,0]},"s":{"a":0,"k":[100,100,100]}},"ao":0,"sw":16,"sh":16,"sc":"#ff0000","ip":0,"op":5,"st":0,"bm":0}]}`
	var compressed bytes.Buffer
	zip := gzip.NewWriter(&compressed)
	if _, err := zip.Write([]byte(model)); err != nil {
		t.Fatal(err)
	}
	if err := zip.Close(); err != nil {
		t.Fatal(err)
	}
	var updates atomic.Int32
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch path.Base(r.URL.Path) {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":42,"username":"test"}}`)
		case "getWebhookInfo":
			fmt.Fprint(w, `{"ok":true,"result":{"url":""}}`)
		case "getUpdates":
			if updates.Add(1) != 1 {
				<-stop
				return
			}
			fmt.Fprint(w, `{"ok":true,"result":[{"update_id":1,"message":{"message_id":2,"date":3,"chat":{"id":55,"type":"private"},"from":{"id":55,"first_name":"test"},"sticker":{"file_id":"tgs","is_animated":true,"is_video":false,"emoji":"👍"}}}]}`)
		case "getFile":
			fmt.Fprintf(w, `{"ok":true,"result":{"file_path":"stickers/fixture.tgs","file_size":%d}}`, compressed.Len())
		case "fixture.tgs":
			_, _ = w.Write(compressed.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer close(stop)
	dir := t.TempDir()
	settings := telegram.DefaultSettings()
	settings.Plugin.Enabled = true
	settings.Telegram.BotToken, settings.Telegram.APIBaseURL = "1:local_test", server.URL
	inbound := make(chan map[string]any, 1)
	adapter := telegram.NewAdapter(settings, &telegram.HostHooks{
		DataDir: func() string { return dir },
		Inbound: nativeAdapterSink(func(message map[string]any) error {
			inbound <- message
			return nil
		}),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := adapter.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer adapter.Stop(ctx)
	select {
	case message := <-inbound:
		parts := message["raw_message"].([]map[string]any)
		if len(parts) != 1 || parts[0]["type"] != "emoji" {
			t.Fatalf("TGS did not enter the native gateway as an emoji: %#v", parts)
		}
		binary, err := base64.StdEncoding.DecodeString(parts[0]["binary_data_base64"].(string))
		if err != nil {
			t.Fatal(err)
		}
		animation, err := gif.DecodeAll(bytes.NewReader(binary))
		if err != nil || len(animation.Image) < 2 || animation.Config.Width != 16 || animation.Config.Height != 16 || animation.Delay[0] != 6 {
			t.Fatalf("wrong GIF produced by rlottie: %#v, %v", animation, err)
		}
		red, green, blue, alpha := animation.Image[0].At(8, 8).RGBA()
		if red <= green || red <= blue || alpha == 0 {
			t.Fatalf("TGS frame was not rendered: %v", animation.Image[0].At(8, 8))
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

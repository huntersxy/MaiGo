package test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	htmlrender "MaiGo/internal/services/html_render"
)

func TestHTMLRenderDelegatesToSidecar(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/render" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode sidecar request: %v", err)
		}
		imageBytes := image.NewRGBA(image.Rect(0, 0, 10, 20))
		imageBytes.Set(0, 0, color.White)
		var pngBuffer bytes.Buffer
		if err := png.Encode(&pngBuffer, imageBytes); err != nil {
			t.Errorf("encode png: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"image_base64": base64.StdEncoding.EncodeToString(pngBuffer.Bytes()),
			"mime_type":    "image/png",
			"width":        10,
			"height":       20,
			"render_ms":    3,
		})
	}))
	defer server.Close()

	service := htmlrender.NewService(htmlrender.Config{
		SidecarURL:       server.URL + "/render",
		Mode:             "sidecar",
		ConcurrencyLimit: 1,
		RenderTimeout:    time.Second,
	})
	result, err := service.RenderHTMLToPNG(context.Background(), htmlrender.Request{
		HTML:      "<html><body>ok</body></html>",
		Selector:  "body",
		TimeoutMs: 1000,
	})
	if err != nil {
		t.Fatalf("RenderHTMLToPNG failed: %v", err)
	}
	if result.Width != 10 || result.Height != 20 || result.MimeType != "image/png" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if received["html"] != "<html><body>ok</body></html>" {
		t.Fatalf("sidecar received unexpected html: %v", received["html"])
	}
}

func TestHTMLRenderWithoutSidecarFails(t *testing.T) {
	service := htmlrender.NewService(htmlrender.DefaultConfig())
	_, err := service.RenderHTMLToPNG(context.Background(), htmlrender.Request{HTML: "<html></html>"})
	if !errors.Is(err, htmlrender.ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}

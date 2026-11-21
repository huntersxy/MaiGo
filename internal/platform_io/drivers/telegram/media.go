package telegram

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/disintegration/imaging"
	"github.com/gen2brain/webp"
)

const maxConvertedBytes = 50 << 20

func (a *Adapter) prepareInboundMedia(ctx context.Context, payload map[string]any, account string) ([]map[string]any, error) {
	a.mu.Lock()
	client, store := a.client, a.store
	a.mu.Unlock()
	media := make([]map[string]any, 0, 3)
	appendMedia := func(kind string, source map[string]any, transform func([]byte) ([]byte, error)) error {
		if client == nil {
			return fmt.Errorf("Telegram 客户端未连接")
		}
		id, _ := source["file_id"].(string)
		if id == "" {
			return fmt.Errorf("Telegram 媒体缺少 file_id")
		}
		binary, err := client.Download(ctx, id)
		if err != nil {
			return err
		}
		if transform != nil {
			binary, err = transform(binary)
			if err != nil {
				return err
			}
		}
		segment, err := BinarySegment(kind, binary, asString(source["emoji"]))
		if err != nil {
			return err
		}
		if kind == "emoji" && source["is_animated"] != nil && store != nil {
			if err := store.SaveSticker(account, asString(segment["hash"]), id); err != nil {
				return err
			}
		}
		media = append(media, segment)
		return nil
	}
	if photos := entityList(payload["photo"]); len(photos) > 0 {
		var largest map[string]any
		var area int64
		for _, photo := range photos {
			w, wOK := asInt64(photo["width"])
			h, hOK := asInt64(photo["height"])
			if !wOK || !hOK || w <= 0 || h <= 0 || w > 1<<24 || h > 1<<24 {
				return nil, fmt.Errorf("Telegram 图片尺寸无效")
			}
			if largest == nil || w*h > area {
				largest, area = photo, w*h
			}
		}
		if err := appendMedia("image", largest, nil); err != nil {
			return nil, err
		}
	}
	timeout := time.Duration(a.settings.Telegram.MediaTimeoutSec) * time.Second
	video := func(binary []byte) ([]byte, error) {
		return convertMedia(ctx, timeout, binary, ".gif",
			"-an", "-filter_complex_threads", "1", "-filter_complex",
			"fps=15,scale=w='min(512,iw)':h='min(512,ih)':force_original_aspect_ratio=decrease,split[a][b];[a]palettegen=stats_mode=diff[p];[b][p]paletteuse=dither=bayer")
	}
	if sticker, _ := asMap(payload["sticker"]); len(sticker) > 0 {
		transform := func(binary []byte) ([]byte, error) {
			if asBool(sticker["is_video"]) {
				return video(binary)
			}
			if asBool(sticker["is_animated"]) {
				return nil, fmt.Errorf("Telegram TGS 贴纸需要 Lottie 渲染")
			}
			img, err := decodeImage(binary)
			if err != nil {
				return nil, err
			}
			var out bytes.Buffer
			imageRGBA := image.NewNRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
			draw.Draw(imageRGBA, imageRGBA.Bounds(), img, img.Bounds().Min, draw.Src)
			err = png.Encode(&out, imageRGBA)
			return out.Bytes(), err
		}
		if err := appendMedia("emoji", sticker, transform); err != nil {
			return nil, err
		}
	}
	voice, _ := asMap(payload["voice"])
	if len(voice) == 0 {
		voice, _ = asMap(payload["audio"])
	}
	wav := func(binary []byte) ([]byte, error) {
		return convertMedia(ctx, timeout, binary, ".wav", "-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le")
	}
	if len(voice) > 0 {
		if err := appendMedia("voice", voice, wav); err != nil {
			return nil, err
		}
	}
	if animation, _ := asMap(payload["animation"]); len(animation) > 0 {
		var transform func([]byte) ([]byte, error)
		if animation["mime_type"] != "image/gif" {
			transform = video
		}
		if err := appendMedia("emoji", animation, transform); err != nil {
			return nil, err
		}
	} else if document, _ := asMap(payload["document"]); len(document) > 0 {
		mime := asString(document["mime_type"])
		if strings.HasPrefix(mime, "image/") && mime != "image/svg+xml" {
			if err := appendMedia("image", document, nil); err != nil {
				return nil, err
			}
		} else if strings.HasPrefix(mime, "audio/") {
			if err := appendMedia("voice", document, wav); err != nil {
				return nil, err
			}
		}
	}
	return media, nil
}

func decodeImage(binary []byte) (image.Image, error) {
	if len(binary) >= 12 && string(binary[:4]) == "RIFF" && string(binary[8:12]) == "WEBP" {
		cfg, err := webp.DecodeConfig(bytes.NewReader(binary))
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 64<<20 {
			return nil, fmt.Errorf("Telegram 图片尺寸无效")
		}
		return webp.Decode(bytes.NewReader(binary))
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(binary))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 64<<20 {
		return nil, fmt.Errorf("Telegram 图片格式或尺寸无效")
	}
	return imaging.Decode(bytes.NewReader(binary), imaging.AutoOrientation(true))
}

func convertMedia(ctx context.Context, timeout time.Duration, binary []byte, extension string, args ...string) ([]byte, error) {
	if timeout <= 0 {
		timeout = DefaultMediaTimeoutSec * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "tg-media-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	input, output := filepath.Join(dir, "input"), filepath.Join(dir, "output"+extension)
	if err := os.WriteFile(input, binary, 0o600); err != nil {
		return nil, err
	}
	flags := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-threads", "1", "-filter_threads", "1", "-i", input}
	flags = append(flags, args...)
	flags = append(flags, "-fs", strconv.Itoa(maxConvertedBytes), "-y", output)
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		projectRoot := strings.TrimSpace(os.Getenv("MAIBOT_PLUGIN_PROJECT_ROOT"))
		if projectRoot != "" {
			matches, _ := filepath.Glob(filepath.Join(projectRoot, "imageio_ffmpeg", "binaries", "ffmpeg-*"))
			if len(matches) == 1 {
				ffmpeg = matches[0]
			}
		}
		if ffmpeg == "" {
			return nil, fmt.Errorf("Telegram 媒体转换需要 FFmpeg")
		}
	}
	if err := exec.CommandContext(ctx, ffmpeg, flags...).Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("Telegram 媒体转换超时: %w", ctx.Err())
		}
		return nil, fmt.Errorf("Telegram FFmpeg 转换失败: %w", err)
	}
	info, err := os.Stat(output)
	if err != nil {
		return nil, err
	}
	if info.Size() >= maxConvertedBytes {
		return nil, fmt.Errorf("Telegram 媒体转换结果超过 50 MB")
	}
	return os.ReadFile(output)
}

func prepareImage(binary []byte, sticker bool) (string, UploadField, error) {
	if sticker && bytes.HasPrefix(binary, []byte{0x1f, 0x8b}) {
		return "sendSticker", UploadField{"sticker", "sticker.tgs", "application/x-tgsticker", binary}, nil
	}
	if sticker && bytes.HasPrefix(binary, []byte{0x1a, 0x45, 0xdf, 0xa3}) {
		return "sendSticker", UploadField{"sticker", "sticker.webm", "video/webm", binary}, nil
	}
	if bytes.HasPrefix(binary, []byte("GIF8")) {
		return "sendAnimation", UploadField{"animation", "animation.gif", "image/gif", binary}, nil
	}
	img, err := decodeImage(binary)
	if err != nil {
		return "", UploadField{}, err
	}
	var out bytes.Buffer
	if sticker {
		fitted := img
		if img.Bounds().Dx() > 512 || img.Bounds().Dy() > 512 {
			fitted = imaging.Fit(img, 512, 512, imaging.Lanczos)
		}
		canvas := imaging.PasteCenter(image.NewNRGBA(image.Rect(0, 0, 512, 512)), fitted)
		if err := webp.Encode(&out, canvas, webp.Options{Quality: 90}); err != nil {
			return "", UploadField{}, err
		}
		if out.Len() > 512<<10 {
			return "", UploadField{}, fmt.Errorf("静态贴纸超过 512 KB")
		}
		return "sendSticker", UploadField{"sticker", "sticker.webp", "image/webp", out.Bytes()}, nil
	}
	if img.Bounds().Dx() > 2560 || img.Bounds().Dy() > 2560 {
		img = imaging.Fit(img, 2560, 2560, imaging.Lanczos)
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	canvas := image.NewRGBA(image.Rect(0, 0, max(w, (h+19)/20), max(h, (w+19)/20)))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	origin := image.Pt((canvas.Bounds().Dx()-w)/2, (canvas.Bounds().Dy()-h)/2)
	draw.Draw(canvas, image.Rectangle{Min: origin, Max: origin.Add(image.Pt(w, h))}, img, img.Bounds().Min, draw.Over)
	if err := jpeg.Encode(&out, canvas, &jpeg.Options{Quality: 92}); err != nil {
		return "", UploadField{}, err
	}
	if out.Len() > 10<<20 {
		return "", UploadField{}, fmt.Errorf("图片超过 Telegram 的 10 MB 上限")
	}
	return "sendPhoto", UploadField{"photo", "photo.jpg", "image/jpeg", out.Bytes()}, nil
}

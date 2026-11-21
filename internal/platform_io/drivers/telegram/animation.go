package telegram

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/png"

	"github.com/gen2brain/webp"
)

const maxAnimationFrames = 200

func prepareAnimatedImage(data []byte) ([]byte, error) {
	var layout image.Config
	var count int
	var err error
	var apng *apngImage
	isPNG := bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n"))
	switch {
	case isPNG:
		apng, err = parseAPNG(data)
		if apng != nil {
			count = len(apng.frames)
			layout.Width, layout.Height = int(binary.BigEndian.Uint32(apng.header)), int(binary.BigEndian.Uint32(apng.header[4:]))
		}
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		count, err = webpFrameCount(data)
		if err == nil && count > 1 {
			layout, err = webp.DecodeConfig(bytes.NewReader(data))
		}
	}
	if err != nil || count <= 1 {
		return nil, err
	}
	if count > maxAnimationFrames {
		return nil, fmt.Errorf("动画帧数超过 200")
	}
	if layout.Width <= 0 || layout.Height <= 0 || uint64(layout.Width)*uint64(layout.Height) > (64<<20)/uint64(count) {
		return nil, fmt.Errorf("Telegram 动图解码尺寸无效或过大")
	}
	canvas := image.Rect(0, 0, layout.Width, layout.Height)
	animation := &gif.GIF{LoopCount: 0}
	if isPNG {
		err = apng.appendGIFFrames(animation, canvas)
	} else {
		var decoded *webp.WEBP
		decoded, err = webp.DecodeAll(bytes.NewReader(data))
		if err == nil {
			if len(decoded.Image) != count || len(decoded.Delay) != count {
				return nil, fmt.Errorf("Telegram WebP 动图帧数与头部不一致")
			}
			for i, frame := range decoded.Image {
				appendGIFFrame(animation, frame, decoded.Delay[i]/10)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := gif.EncodeAll(&out, animation); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

type apngImage struct {
	header []byte
	colors [][]byte
	frames []apngFrame
}

type apngFrame struct {
	width, height uint32
	x, y          int
	delay         int
	dispose       byte
	blend         byte
	poster        bool
	pixels        [][]byte
}

func parseAPNG(data []byte) (*apngImage, error) {
	animation := &apngImage{}
	var declared, sequence uint32
	var width, height uint32
	var found uint32
	seenIDAT := false
	for rest := data[8:]; len(rest) >= 12; {
		size := uint64(binary.BigEndian.Uint32(rest))
		if size+12 > uint64(len(rest)) {
			return nil, fmt.Errorf("Telegram APNG 块长度无效")
		}
		chunk := rest[8 : 8+size]
		if crc32.ChecksumIEEE(rest[4:8+size]) != binary.BigEndian.Uint32(rest[8+size:]) {
			return nil, fmt.Errorf("Telegram PNG 块校验失败")
		}
		switch string(rest[4:8]) {
		case "IHDR":
			if size != 13 || animation.header != nil {
				return nil, fmt.Errorf("Telegram PNG 头部长度无效")
			}
			animation.header = chunk
			width, height = binary.BigEndian.Uint32(chunk), binary.BigEndian.Uint32(chunk[4:])
		case "PLTE", "tRNS":
			animation.colors = append(animation.colors, rest[:12+size])
		case "acTL":
			if size != 8 || declared != 0 {
				return nil, fmt.Errorf("Telegram APNG 动画头部无效")
			}
			declared = binary.BigEndian.Uint32(chunk)
			if declared == 0 || declared > maxAnimationFrames {
				return nil, fmt.Errorf("Telegram APNG 动画帧数无效")
			}
		case "fcTL":
			if size != 26 || declared == 0 || found >= declared || binary.BigEndian.Uint32(chunk) != sequence {
				return nil, fmt.Errorf("Telegram APNG 帧头无效")
			}
			w, h := binary.BigEndian.Uint32(chunk[4:]), binary.BigEndian.Uint32(chunk[8:])
			x, y := binary.BigEndian.Uint32(chunk[12:]), binary.BigEndian.Uint32(chunk[16:])
			if w == 0 || h == 0 || uint64(x)+uint64(w) > uint64(width) || uint64(y)+uint64(h) > uint64(height) || chunk[24] > 2 || chunk[25] > 1 {
				return nil, fmt.Errorf("Telegram APNG 帧尺寸、偏移或混合模式无效")
			}
			if !seenIDAT && (found != 0 || w != width || h != height) {
				return nil, fmt.Errorf("Telegram APNG 首帧尺寸与画布不一致")
			}
			denominator := int(binary.BigEndian.Uint16(chunk[22:]))
			if denominator == 0 {
				denominator = 100
			}
			delay := int(binary.BigEndian.Uint16(chunk[20:])) * 100 / denominator
			animation.frames = append(animation.frames, apngFrame{width: w, height: h, x: int(x), y: int(y), delay: delay, dispose: chunk[24], blend: chunk[25]})
			found++
			sequence++
		case "IDAT":
			if declared == 0 {
				return nil, nil
			}
			if len(animation.frames) == 0 {
				animation.frames = append(animation.frames, apngFrame{width: width, height: height, poster: true, delay: 10})
			}
			animation.frames[0].pixels = append(animation.frames[0].pixels, chunk)
			seenIDAT = true
		case "fdAT":
			if size < 4 || found == 0 || !seenIDAT || binary.BigEndian.Uint32(chunk) != sequence {
				return nil, fmt.Errorf("Telegram APNG 帧数据无效")
			}
			frame := &animation.frames[len(animation.frames)-1]
			frame.pixels = append(frame.pixels, chunk[4:])
			sequence++
		case "IEND":
			if found != declared || !seenIDAT {
				return nil, fmt.Errorf("Telegram APNG 动画帧数与头部不一致")
			}
			return animation, nil
		}
		rest = rest[12+size:]
	}
	return nil, fmt.Errorf("Telegram PNG 数据不完整")
}

func webpFrameCount(data []byte) (int, error) {
	var count int
	for rest := data[12:]; len(rest) >= 8; {
		size := uint64(binary.LittleEndian.Uint32(rest[4:8]))
		end := 8 + size + size%2
		if end > uint64(len(rest)) {
			return 0, fmt.Errorf("Telegram WebP 块长度无效")
		}
		if string(rest[:4]) == "ANMF" {
			count++
			if count > maxAnimationFrames {
				return 0, fmt.Errorf("动画帧数超过 200")
			}
		}
		rest = rest[end:]
	}
	return count, nil
}

func (source *apngImage) appendGIFFrames(animation *gif.GIF, bounds image.Rectangle) error {
	canvas := image.NewNRGBA(bounds)
	for _, frame := range source.frames {
		imageData, err := source.decode(frame)
		if err != nil {
			return err
		}
		if frame.poster {
			appendGIFFrame(animation, imageData, frame.delay)
			continue
		}
		rect := imageData.Bounds().Add(image.Pt(frame.x, frame.y))
		var previous *image.NRGBA
		if frame.dispose == 2 {
			previous = image.NewNRGBA(rect)
			draw.Draw(previous, rect, canvas, rect.Min, draw.Src)
		}
		op := draw.Src
		if frame.blend == 1 {
			op = draw.Over
		}
		draw.Draw(canvas, rect, imageData, imageData.Bounds().Min, op)
		appendGIFFrame(animation, canvas, frame.delay)
		switch frame.dispose {
		case 1:
			draw.Draw(canvas, rect, image.Transparent, image.Point{}, draw.Src)
		case 2:
			draw.Draw(canvas, rect, previous, rect.Min, draw.Src)
		}
	}
	return nil
}

func (source *apngImage) decode(frame apngFrame) (image.Image, error) {
	var content bytes.Buffer
	content.WriteString("\x89PNG\r\n\x1a\n")
	header := bytes.Clone(source.header)
	binary.BigEndian.PutUint32(header, frame.width)
	binary.BigEndian.PutUint32(header[4:], frame.height)
	appendPNGChunk(&content, "IHDR", header)
	for _, chunk := range source.colors {
		content.Write(chunk)
	}
	for _, chunk := range frame.pixels {
		appendPNGChunk(&content, "IDAT", chunk)
	}
	appendPNGChunk(&content, "IEND", nil)
	return png.Decode(&content)
}

func appendPNGChunk(out *bytes.Buffer, name string, content []byte) {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(content)))
	out.Write(size[:])
	start := out.Len()
	out.WriteString(name)
	out.Write(content)
	binary.BigEndian.PutUint32(size[:], crc32.ChecksumIEEE(out.Bytes()[start:]))
	out.Write(size[:])
}

func appendGIFFrame(animation *gif.GIF, source image.Image, centiseconds int) {
	colors := append(color.Palette{color.Transparent}, palette.Plan9[:255]...)
	frame := image.NewPaletted(source.Bounds(), colors)
	draw.FloydSteinberg.Draw(frame, frame.Bounds(), source, source.Bounds().Min)
	animation.Image = append(animation.Image, frame)
	animation.Delay = append(animation.Delay, min(max(centiseconds, 0), 65535))
	animation.Disposal = append(animation.Disposal, gif.DisposalBackground)
}

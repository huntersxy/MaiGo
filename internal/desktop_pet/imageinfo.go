package desktoppet

// 皮肤与聊天图片按文件头取尺寸格式，再做一次完整解码校验完整性

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"os"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"
)

// imageInfo 图片头部解析结果
type imageInfo struct {
	Width    int
	Height   int
	Format   string
	MIME     string
	Animated bool
}

func readImageInfo(path string) (imageInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return imageInfo{}, err
	}
	return parseImageInfo(data)
}

func parseImageInfo(data []byte) (imageInfo, error) {
	var (
		info imageInfo
		err  error
	)
	switch {
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		info, err = parsePNGInfo(data)
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		info, err = parseWebPInfo(data)
	case len(data) >= 7 && string(data[:4]) == "GIF8":
		info, err = parseGIFInfo(data)
	case len(data) >= 4 && data[0] == 0xFF && data[1] == 0xD8:
		info, err = parseJPEGInfo(data)
	default:
		return imageInfo{}, fmt.Errorf("图片格式或尺寸不支持")
	}
	if err != nil {
		return imageInfo{}, err
	}
	// 对齐 PIL Image.verify：头部合法之外还要完整解码，截断或损坏的图片不能进入皮肤与聊天
	if err := verifyImageDecode(data); err != nil {
		return imageInfo{}, err
	}
	return info, nil
}

// verifyImageDecode 完整解码图片，确保文件没有截断或结构损坏
func verifyImageDecode(data []byte) error {
	decoded, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("图片格式或尺寸不支持")
	}
	switch format {
	case "png", "jpeg", "gif", "webp":
	default:
		return fmt.Errorf("图片格式或尺寸不支持")
	}
	bounds := decoded.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return fmt.Errorf("图片格式或尺寸不支持")
	}
	return nil
}

// parseGIFInfo 读取 GIF 逻辑屏幕宽高（小端序），仅用于尺寸校验
func parseGIFInfo(data []byte) (imageInfo, error) {
	info := imageInfo{Format: "GIF", MIME: "image/gif"}
	if len(data) < 10 {
		return imageInfo{}, fmt.Errorf("图片格式或尺寸不支持")
	}
	info.Width = int(binary.LittleEndian.Uint16(data[6:8]))
	info.Height = int(binary.LittleEndian.Uint16(data[8:10]))
	if info.Width == 0 || info.Height == 0 {
		return imageInfo{}, fmt.Errorf("图片格式或尺寸不支持")
	}
	return info, nil
}

// parseJPEGInfo 扫描 SOFn 段读取 JPEG 宽高
func parseJPEGInfo(data []byte) (imageInfo, error) {
	info := imageInfo{Format: "JPEG", MIME: "image/jpeg"}
	offset := 2
	for offset+4 <= len(data) {
		if data[offset] != 0xFF {
			offset++
			continue
		}
		marker := data[offset+1]
		offset += 2
		switch {
		case marker == 0xD8 || marker == 0xD9 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01:
			continue
		case marker == 0xFF:
			offset--
			continue
		}
		if offset+2 > len(data) {
			break
		}
		length := int(binary.BigEndian.Uint16(data[offset : offset+2]))
		if length < 2 || offset+length > len(data) {
			break
		}
		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			if length < 7 {
				break
			}
			info.Height = int(binary.BigEndian.Uint16(data[offset+3 : offset+5]))
			info.Width = int(binary.BigEndian.Uint16(data[offset+5 : offset+7]))
			if info.Width == 0 || info.Height == 0 {
				return imageInfo{}, fmt.Errorf("图片格式或尺寸不支持")
			}
			return info, nil
		}
		offset += length
	}
	return imageInfo{}, fmt.Errorf("图片格式或尺寸不支持")
}

func parsePNGInfo(data []byte) (imageInfo, error) {
	info := imageInfo{Format: "PNG", MIME: "image/png"}
	offset := 8
	for offset+8 <= len(data) {
		length := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		kind := string(data[offset+4 : offset+8])
		body := offset + 8
		if length < 0 || body+length > len(data) {
			break
		}
		switch kind {
		case "IHDR":
			if length < 8 {
				return imageInfo{}, fmt.Errorf("图片格式或尺寸不支持")
			}
			info.Width = int(binary.BigEndian.Uint32(data[body : body+4]))
			info.Height = int(binary.BigEndian.Uint32(data[body+4 : body+8]))
		case "acTL":
			// 动画 PNG 的播放控制块出现在 IDAT 之前
			info.Animated = true
		}
		offset = body + length + 4
		if kind == "IEND" {
			break
		}
	}
	if info.Width <= 0 || info.Height <= 0 {
		return imageInfo{}, fmt.Errorf("图片格式或尺寸不支持")
	}
	return info, nil
}

func parseWebPInfo(data []byte) (imageInfo, error) {
	info := imageInfo{Format: "WEBP", MIME: "image/webp"}
	offset := 12
	for offset+8 <= len(data) {
		kind := string(data[offset : offset+4])
		length := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		body := offset + 8
		end := body + length
		if length < 0 || end > len(data) {
			end = len(data)
		}
		chunk := data[body:end]
		switch kind {
		case "VP8X":
			if len(chunk) < 10 {
				return imageInfo{}, fmt.Errorf("图片格式或尺寸不支持")
			}
			// VP8X 标志位第 1 位表示带 ANIM/ANMF 的动图
			if chunk[0]&0x02 != 0 {
				info.Animated = true
			}
			info.Width = 1 + int(uint32(chunk[4])|uint32(chunk[5])<<8|uint32(chunk[6])<<16)
			info.Height = 1 + int(uint32(chunk[7])|uint32(chunk[8])<<8|uint32(chunk[9])<<16)
		case "VP8 ":
			if len(chunk) < 10 {
				return imageInfo{}, fmt.Errorf("图片格式或尺寸不支持")
			}
			if info.Width == 0 {
				info.Width = int(binary.LittleEndian.Uint16(chunk[6:8]) & 0x3fff)
				info.Height = int(binary.LittleEndian.Uint16(chunk[8:10]) & 0x3fff)
			}
		case "VP8L":
			if len(chunk) < 5 || chunk[0] != 0x2f {
				return imageInfo{}, fmt.Errorf("图片格式或尺寸不支持")
			}
			if info.Width == 0 {
				bits := uint32(chunk[1]) | uint32(chunk[2])<<8 | uint32(chunk[3])<<16 | uint32(chunk[4])<<24
				info.Width = 1 + int(bits&0x3fff)
				info.Height = 1 + int((bits>>14)&0x3fff)
			}
		case "ANIM", "ANMF":
			info.Animated = true
		}
		offset = body + length + (length & 1)
		if offset <= body {
			break
		}
	}
	if info.Width <= 0 || info.Height <= 0 {
		return imageInfo{}, fmt.Errorf("图片格式或尺寸不支持")
	}
	return info, nil
}

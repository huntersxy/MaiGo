package imagesystem

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"math"

	"github.com/disintegration/imaging"
	"go.uber.org/zap"
	_ "golang.org/x/image/bmp"
	"golang.org/x/image/webp"
)

// CompressImageToSize 将图片压缩到目标大小以内，失败时保持原图数据
// 对齐 Python ImageUtils.compress_image_to_size 的 16 轮质量/尺寸回退
func CompressImageToSize(imageBytes []byte, targetSize int) ([]byte, error) {
	if len(imageBytes) == 0 {
		return nil, fmt.Errorf("输入的图片字节数据无效")
	}
	if targetSize <= 0 || len(imageBytes) <= targetSize {
		return imageBytes, nil
	}
	img, err := decodeImageBytes(imageBytes)
	if err != nil {
		zap.L().Warn("接收图片压缩失败，无法识别图片格式", zap.Error(err))
		return imageBytes, nil
	}
	working := flattenToRGB(img)
	compressed := compressStaticImageToSize(working, targetSize)
	if len(compressed) < len(imageBytes) {
		return compressed, nil
	}
	return imageBytes, nil
}

func decodeImageBytes(data []byte) (image.Image, error) {
	reader := bytes.NewReader(data)
	if img, err := imaging.Decode(reader, imaging.AutoOrientation(true)); err == nil {
		return img, nil
	}
	if _, err := reader.Seek(0, 0); err != nil {
		return nil, err
	}
	return webp.Decode(reader)
}

// flattenToRGB 将带透明通道的图片铺白底，对齐 Python _prepare_image_for_receive_compression
func flattenToRGB(img image.Image) image.Image {
	bounds := img.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return img
	}
	canvas := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(canvas, canvas.Bounds(), img, bounds.Min, draw.Over)
	return canvas
}

// compressStaticImageToSize 通过降低质量与缩放尺寸压缩静态图片
func compressStaticImageToSize(input image.Image, targetSize int) []byte {
	working := input
	quality := 85
	var lastOutput []byte
	for round := 0; round < 16; round++ {
		var buffer bytes.Buffer
		if err := jpeg.Encode(&buffer, working, &jpeg.Options{Quality: quality}); err != nil {
			return lastOutput
		}
		output := buffer.Bytes()
		lastOutput = append(lastOutput[:0], output...)
		if len(output) <= targetSize {
			return lastOutput
		}
		if quality > 55 {
			quality = max(55, quality-10)
			continue
		}
		scale := math.Max(0.1, math.Min(0.95, math.Sqrt(float64(targetSize)/float64(len(output)))*0.95))
		newWidth := max(1, int(float64(working.Bounds().Dx())*scale))
		newHeight := max(1, int(float64(working.Bounds().Dy())*scale))
		if newWidth == working.Bounds().Dx() && newHeight == working.Bounds().Dy() {
			break
		}
		working = imaging.Resize(working, newWidth, newHeight, imaging.Lanczos)
	}
	return lastOutput
}

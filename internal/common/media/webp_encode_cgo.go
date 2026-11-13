//go:build cgo && !race

package media

import (
	"image"
	"io"

	"github.com/kolesa-team/go-webp/encoder"
)

// encodeWebP 用 cgo 版 libwebp 编码，CI 与生产镜像走这条路径
func encodeWebP(w io.Writer, img image.Image, quality float32) error {
	opts, err := encoder.NewLossyEncoderOptions(encoder.PresetDefault, quality)
	if err != nil {
		return err
	}
	enc, err := encoder.NewEncoder(img, opts)
	if err != nil {
		return err
	}
	return enc.Encode(w)
}

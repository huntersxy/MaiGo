//go:build !cgo

package media

import (
	"image"
	"io"
	"math"

	"github.com/gen2brain/webp"
)

// encodeWebP 用 WASM 移植版 libwebp 编码，供无 C 工具链的本机构建使用
//
// gen2brain/webp 把 libwebp 编译成 WASM 再转译为纯 Go，
// 因此没有 cgo 也能得到 libwebp 的编码结果，
// 质量参数与 cgo 路径保持一致，便于两种构建在同一实例目录下混用
func encodeWebP(w io.Writer, img image.Image, quality float32) error {
	return webp.Encode(w, img, webp.Options{Quality: int(math.Round(float64(quality)))})
}

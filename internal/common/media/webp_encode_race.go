//go:build cgo && race

package media

import (
	"image"
	"io"
	"math"

	"github.com/gen2brain/webp"
)

// encodeWebP 在 race 构建下改用 WASM 移植版 libwebp
//
// 常规 cgo 路径依赖系统 libwebp 头文件；未安装 libwebp-dev 的环境无法用 -race
// 跑传递依赖 internal/media 的包——race 构建启用该 shim 即可脱离 C 依赖
// gen2brain/webp 把 libwebp 编译成 WASM 再转译为纯 Go，
// 编码结果同为 libwebp 语义，质量参数取整逻辑与 nocgo 版一致
func encodeWebP(w io.Writer, img image.Image, quality float32) error {
	return webp.Encode(w, img, webp.Options{Quality: int(math.Round(float64(quality)))})
}

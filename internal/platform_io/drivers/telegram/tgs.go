//go:build rlottie && cgo

package telegram

/*
#cgo LDFLAGS: -lrlottie -lstdc++ -lm
#include <rlottie_capi.h>
#include <stdlib.h>
*/
import "C"

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/gif"
	"io"
	"math"
	"unsafe"
)

func renderTGS(ctx context.Context, compressed []byte) ([]byte, error) {
	stream, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("Telegram TGS 压缩格式无效: %w", err)
	}
	defer stream.Close()
	const maxTGS = 1 << 20
	data, err := io.ReadAll(io.LimitReader(stream, maxTGS+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxTGS {
		return nil, fmt.Errorf("TGS 解压结果超过 1 MB")
	}
	var model struct {
		Width  int `json:"w"`
		Height int `json:"h"`
		Assets []struct {
			ImagePath string `json:"p"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(data, &model); err != nil || model.Width < 1 || model.Height < 1 || model.Width > 512 || model.Height > 512 {
		return nil, fmt.Errorf("TGS 画布尺寸无效")
	}
	for _, asset := range model.Assets {
		if asset.ImagePath != "" {
			return nil, fmt.Errorf("TGS 贴纸必须由矢量图层组成")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source := C.CString(string(data))
	defer C.free(unsafe.Pointer(source))
	empty := C.CString("")
	defer C.free(unsafe.Pointer(empty))
	animation := C.lottie_animation_from_data(source, empty, empty)
	if animation == nil {
		return nil, fmt.Errorf("TGS 贴纸模型无效")
	}
	defer C.lottie_animation_destroy(animation)
	fps := float64(C.lottie_animation_get_framerate(animation))
	frames := uint64(C.lottie_animation_get_totalframe(animation))
	if fps <= 0 || math.IsNaN(fps) || math.IsInf(fps, 0) || frames == 0 || frames > 200 || float64(frames)/fps > 3.1 {
		return nil, fmt.Errorf("TGS 帧数或时长不符合贴纸规范")
	}
	var width, height C.size_t
	C.lottie_animation_get_size(animation, &width, &height)
	if width == 0 || height == 0 || width > 512 || height > 512 {
		return nil, fmt.Errorf("TGS 画布尺寸无效")
	}
	surface := make([]uint32, int(width)*int(height))
	frame := image.NewNRGBA(image.Rect(0, 0, int(width), int(height)))
	result := &gif.GIF{LoopCount: 0}
	step := int(math.RoundToEven(min(fps/15, float64(frames))))
	for i := 0; i < int(frames); i += max(1, step) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		clear(surface)
		C.lottie_animation_render(animation, C.size_t(i), (*C.uint32_t)(unsafe.Pointer(&surface[0])), width, height, width*4)
		for px, argb := range surface {
			offset := px * 4
			frame.Pix[offset], frame.Pix[offset+1], frame.Pix[offset+2], frame.Pix[offset+3] = byte(argb>>16), byte(argb>>8), byte(argb), byte(argb>>24)
		}
		appendGIFFrame(result, frame, 6)
	}
	var encoded bytes.Buffer
	if err := gif.EncodeAll(&encoded, result); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

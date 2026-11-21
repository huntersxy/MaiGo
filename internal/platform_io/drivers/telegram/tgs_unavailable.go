//go:build !rlottie || !cgo

package telegram

import (
	"context"
	"fmt"
)

func renderTGS(context.Context, []byte) ([]byte, error) {
	return nil, fmt.Errorf("Telegram TGS 贴纸需要链接 rlottie 的构建")
}

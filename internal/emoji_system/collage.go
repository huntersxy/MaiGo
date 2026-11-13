package emojisystem

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strconv"
	"sync"

	"github.com/disintegration/imaging"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	_ "golang.org/x/image/webp"
)

// 拼图常量对齐 MaiBot send_emoji.py:36-42
const (
	// CollageTileSize 单块边长，对齐 _EMOJI_CANDIDATE_TILE_SIZE
	CollageTileSize = 256
	// CollageBadgeSize 序号角标高度，对齐 _EMOJI_INDEX_BADGE_SIZE
	CollageBadgeSize = 64
	// CollageIndexFontSize 序号字号，对齐 _EMOJI_INDEX_FONT_SIZE
	CollageIndexFontSize = 32
	// CollageMaxCandidateCount 候选上限，对齐 _EMOJI_MAX_CANDIDATE_COUNT
	CollageMaxCandidateCount = 64

	collageGap          = 12
	collageBadgeMargin  = 14
	collageBadgeRadius  = 8
	collageMinFontSize  = 8
	collageBadgePadding = 12
)

var (
	collageTypefaceOnce sync.Once
	collageTypeface     *opentype.Font
	collageTypefaceErr  error

	collageFaceMu    sync.Mutex
	collageFaceCache = map[int]font.Face{}
)

// CalculateGridShape 按候选数量计算尽量接近矩形的行列数
// 对齐 send_emoji.py:82-102 _calculate_grid_shape
func CalculateGridShape(candidateCount int) (rows int, columns int) {
	if candidateCount <= 0 {
		return 1, 1
	}
	bestColumns := candidateCount
	bestRows := 1
	bestAspectGap := 0
	bestEmptySlots := 0
	hasBest := false
	for cols := 1; cols <= candidateCount; cols++ {
		r := (candidateCount + cols - 1) / cols
		emptySlots := r*cols - candidateCount
		aspectGap := cols - r
		if aspectGap < 0 {
			aspectGap = -aspectGap
		}
		if !hasBest || aspectGap < bestAspectGap || (aspectGap == bestAspectGap && emptySlots < bestEmptySlots) {
			hasBest = true
			bestAspectGap = aspectGap
			bestEmptySlots = emptySlots
			bestColumns = cols
			bestRows = r
		}
	}
	return bestRows, bestColumns
}

// BuildPlaceholderTile 构建图片读取失败时的占位图
// 对齐 send_emoji.py:105-120 _build_placeholder_tile
func BuildPlaceholderTile(label string, tileSize int) image.Image {
	if tileSize <= 0 {
		tileSize = CollageTileSize
	}
	tile := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))
	draw.Draw(tile, tile.Bounds(), image.NewUniform(color.RGBA{R: 245, G: 245, B: 245, A: 255}), image.Point{}, draw.Src)
	face, err := collageFace(collageMinFontSize)
	if err != nil {
		return tile
	}
	drawCenteredText(tile, face, label, float64(tileSize)/2, float64(tileSize)/2, color.RGBA{R: 80, G: 80, B: 80, A: 255})
	return tile
}

// BuildLabeledTile 构建带序号角标的候选图片块
// 对齐 send_emoji.py:123-172 _build_labeled_tile
func BuildLabeledTile(imageBytes []byte, index int, tileSize int) image.Image {
	if tileSize <= 0 {
		tileSize = CollageTileSize
	}
	source, err := decodeCollageImage(imageBytes)
	if err != nil {
		return BuildPlaceholderTile(strconv.Itoa(index), tileSize)
	}
	scaled := shrinkToFit(source, tileSize)
	tile := image.NewRGBA(image.Rect(0, 0, tileSize, tileSize))
	draw.Draw(tile, tile.Bounds(), image.NewUniform(color.RGBA{R: 255, G: 255, B: 255, A: 255}), image.Point{}, draw.Src)
	offsetX := (tileSize - scaled.Bounds().Dx()) / 2
	offsetY := (tileSize - scaled.Bounds().Dy()) / 2
	draw.Draw(tile,
		image.Rect(offsetX, offsetY, offsetX+scaled.Bounds().Dx(), offsetY+scaled.Bounds().Dy()),
		scaled, scaled.Bounds().Min, draw.Over)

	label := strconv.Itoa(index)
	fontSize := CollageIndexFontSize
	face, faceErr := collageFace(fontSize)
	if faceErr != nil {
		return tile
	}
	textWidth, _ := collageTextSize(face, label)
	limit := tileSize - 2*collageBadgeMargin - collageBadgePadding
	for textWidth > float64(limit) && fontSize > collageMinFontSize {
		fontSize--
		next, err := collageFace(fontSize)
		if err != nil {
			break
		}
		face = next
		textWidth, _ = collageTextSize(face, label)
	}
	badgeWidth := CollageBadgeSize
	if int(math.Ceil(textWidth))+collageBadgePadding > badgeWidth {
		badgeWidth = int(math.Ceil(textWidth)) + collageBadgePadding
	}
	fillRoundedRect(tile,
		image.Rect(collageBadgeMargin, collageBadgeMargin, collageBadgeMargin+badgeWidth, collageBadgeMargin+CollageBadgeSize),
		collageBadgeRadius, color.RGBA{A: 180})
	centerX := float64(collageBadgeMargin) + float64(badgeWidth)/2
	centerY := float64(collageBadgeMargin) + float64(CollageBadgeSize)/2 - 1
	drawCenteredText(tile, face, label, centerX, centerY, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	return tile
}

// MergeEmojiTiles 把候选表情图拼成一张尽量接近矩形的网格 PNG
// 对齐 send_emoji.py:175-199 _merge_emoji_tiles
func MergeEmojiTiles(imageBytesList [][]byte, startIndex int) ([]byte, error) {
	if startIndex <= 0 {
		startIndex = 1
	}
	rows, columns := CalculateGridShape(len(imageBytesList))
	canvasWidth := CollageTileSize*columns + collageGap*(columns-1)
	canvasHeight := CollageTileSize*rows + collageGap*(rows-1)
	canvas := image.NewRGBA(image.Rect(0, 0, canvasWidth, canvasHeight))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.RGBA{R: 255, G: 255, B: 255, A: 255}), image.Point{}, draw.Src)
	for index, raw := range imageBytesList {
		tile := BuildLabeledTile(raw, startIndex+index, CollageTileSize)
		offsetX := (index % columns) * (CollageTileSize + collageGap)
		offsetY := (index / columns) * (CollageTileSize + collageGap)
		draw.Draw(canvas,
			image.Rect(offsetX, offsetY, offsetX+CollageTileSize, offsetY+CollageTileSize),
			tile, image.Point{}, draw.Over)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return nil, fmt.Errorf("emojisystem: 编码表情包拼图失败: %w", err)
	}
	return buf.Bytes(), nil
}

// decodeCollageImage 解码候选表情字节，失败由调用方回退占位图
func decodeCollageImage(raw []byte) (image.Image, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("emojisystem: 表情图片为空")
	}
	return imaging.Decode(bytes.NewReader(raw))
}

// shrinkToFit 复刻 PIL Image.thumbnail 的两段等比缩放与取整顺序
func shrinkToFit(source image.Image, tileSize int) image.Image {
	width := source.Bounds().Dx()
	height := source.Bounds().Dy()
	if width <= 0 || height <= 0 {
		return source
	}
	x, y := width, height
	if x > tileSize {
		y = int(math.Round(float64(y) * float64(tileSize) / float64(x)))
		x = tileSize
	}
	if y > tileSize {
		x = int(math.Round(float64(x) * float64(tileSize) / float64(y)))
		y = tileSize
	}
	if x < 1 {
		x = 1
	}
	if y < 1 {
		y = 1
	}
	if x == width && y == height {
		return source
	}
	return imaging.Resize(source, x, y, imaging.Lanczos)
}

// collageFace 按字号取字体，缓存避免每块重复解析
func collageFace(size int) (font.Face, error) {
	if size <= 0 {
		size = collageMinFontSize
	}
	collageFaceMu.Lock()
	defer collageFaceMu.Unlock()
	if face, ok := collageFaceCache[size]; ok {
		return face, nil
	}
	collageTypefaceOnce.Do(func() {
		collageTypeface, collageTypefaceErr = opentype.Parse(goregular.TTF)
	})
	if collageTypefaceErr != nil {
		return nil, fmt.Errorf("emojisystem: 解析拼图字体失败: %w", collageTypefaceErr)
	}
	face, err := opentype.NewFace(collageTypeface, &opentype.FaceOptions{
		Size:    float64(size),
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return nil, fmt.Errorf("emojisystem: 创建拼图字体失败: %w", err)
	}
	collageFaceCache[size] = face
	return face, nil
}

// collageTextSize 返回文字墨迹框的宽高（像素）
func collageTextSize(face font.Face, label string) (width float64, height float64) {
	bounds, _ := font.BoundString(face, label)
	return float64(bounds.Max.X-bounds.Min.X) / 64, float64(bounds.Max.Y-bounds.Min.Y) / 64
}

// drawCenteredText 让文字墨迹框中心落在给定坐标
func drawCenteredText(dst draw.Image, face font.Face, label string, centerX, centerY float64, fill color.Color) {
	bounds, _ := font.BoundString(face, label)
	dotX := centerX - float64(bounds.Min.X+bounds.Max.X)/128
	dotY := centerY - float64(bounds.Min.Y+bounds.Max.Y)/128
	drawer := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(fill),
		Face: face,
		Dot:  fixed.Point26_6{X: fixed.Int26_6(math.Round(dotX * 64)), Y: fixed.Int26_6(math.Round(dotY * 64))},
	}
	drawer.DrawString(label)
}

// fillRoundedRect 用 src-over 填充圆角矩形，对齐 PIL rounded_rectangle
func fillRoundedRect(dst *image.RGBA, rect image.Rectangle, radius int, fill color.RGBA) {
	if radius < 0 {
		radius = 0
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			if !insideRoundedRect(x, y, rect, radius) {
				continue
			}
			blendPixel(dst, x, y, fill)
		}
	}
}

func insideRoundedRect(x, y int, rect image.Rectangle, radius int) bool {
	if x < rect.Min.X || x >= rect.Max.X || y < rect.Min.Y || y >= rect.Max.Y {
		return false
	}
	if radius <= 0 {
		return true
	}
	left := rect.Min.X + radius
	right := rect.Max.X - radius
	top := rect.Min.Y + radius
	bottom := rect.Max.Y - radius
	centerX := left
	if x >= right {
		centerX = right
	}
	centerY := top
	if y >= bottom {
		centerY = bottom
	}
	if x >= left && x < right {
		return true
	}
	if y >= top && y < bottom {
		return true
	}
	dx := float64(x - centerX)
	dy := float64(y - centerY)
	return dx*dx+dy*dy <= float64(radius*radius)
}

// blendPixel 按 alpha 做 src-over 合成
func blendPixel(dst *image.RGBA, x, y int, src color.RGBA) {
	if !(image.Point{X: x, Y: y}).In(dst.Bounds()) {
		return
	}
	if src.A == 255 {
		dst.SetRGBA(x, y, src)
		return
	}
	if src.A == 0 {
		return
	}
	sa := float64(src.A) / 255
	offset := dst.PixOffset(x, y)
	da := float64(dst.Pix[offset+3]) / 255
	outA := sa + da*(1-sa)
	if outA <= 0 {
		dst.SetRGBA(x, y, color.RGBA{})
		return
	}
	blend := func(srcChannel, dstChannel uint8) uint8 {
		value := (float64(srcChannel)*sa + float64(dstChannel)*da*(1-sa)) / outA
		return uint8(math.Round(math.Min(255, math.Max(0, value))))
	}
	dst.Pix[offset] = blend(src.R, dst.Pix[offset])
	dst.Pix[offset+1] = blend(src.G, dst.Pix[offset+1])
	dst.Pix[offset+2] = blend(src.B, dst.Pix[offset+2])
	dst.Pix[offset+3] = uint8(math.Round(outA * 255))
}

package imagememory

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/image/bmp"
	"golang.org/x/image/webp"
)

// imageFormatToMIME 对齐 Python _FORMAT_TO_MIME
var imageFormatToMIME = map[string]string{
	"BMP":  "image/bmp",
	"JPEG": "image/jpeg",
	"PNG":  "image/png",
	"WEBP": "image/webp",
}

var pngSignature = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

// errUnidentifiedImage 表示无法识别的图片内容
var errUnidentifiedImage = &ValidationError{Message: "无法解析图片内容"}

// InspectedImage 对齐 Python InspectedImage 的只读字段
type InspectedImage struct {
	ByteSize   int
	Extension  string
	Height     int
	MIMEType   string
	Width      int
	FrameCount int
}

// ImageAssetStore 对齐 Python ImageAssetStore
type ImageAssetStore struct {
	root      string
	maxBytes  int
	maxPixels int
}

// NewImageAssetStore 创建内容寻址图片资产存储
func NewImageAssetStore(assetsRoot string, maxBytes int, maxPixels int) (*ImageAssetStore, error) {
	if maxBytes < 1 {
		maxBytes = 1
	}
	if maxPixels < 1 {
		maxPixels = 1
	}
	root, err := filepath.Abs(assetsRoot)
	if err != nil {
		return nil, err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolved
	}
	if err := os.MkdirAll(root, 0o777); err != nil {
		return nil, err
	}
	return &ImageAssetStore{root: root, maxBytes: maxBytes, maxPixels: maxPixels}, nil
}

// Root 返回资产目录绝对路径
func (s *ImageAssetStore) Root() string { return s.root }

// MaxBytes 返回单图大小上限
func (s *ImageAssetStore) MaxBytes() int { return s.maxBytes }

// MaxPixels 返回单图像素上限
func (s *ImageAssetStore) MaxPixels() int { return s.maxPixels }

// ContentHash 计算原始字节的 SHA-256 十六进制值
func ContentHash(imageBytes []byte) string {
	sum := sha256.Sum256(imageBytes)
	return hex.EncodeToString(sum[:])
}

// Inspect 校验图片格式、尺寸与帧数，对齐 Python inspect
func (s *ImageAssetStore) Inspect(imageBytes []byte) (InspectedImage, error) {
	if len(imageBytes) == 0 {
		return InspectedImage{}, validationErrorf("图片内容为空")
	}
	if len(imageBytes) > s.maxBytes {
		return InspectedImage{}, validationErrorf("图片超过大小限制: %d > %d", len(imageBytes), s.maxBytes)
	}
	format, width, height, frameCount, err := inspectImagePayload(imageBytes)
	if err != nil {
		return InspectedImage{}, validationErrorf("无法解析图片内容")
	}
	mimeType, supported := imageFormatToMIME[format]
	if !supported {
		label := format
		if label == "" {
			label = "unknown"
		}
		return InspectedImage{}, validationErrorf("不支持的图片格式: %s", label)
	}
	if width <= 0 || height <= 0 || int64(width)*int64(height) > int64(s.maxPixels) {
		return InspectedImage{}, validationErrorf("图片像素超过限制: %dx%d", width, height)
	}
	extension := strings.ToLower(format)
	if format == "JPEG" {
		extension = "jpg"
	}
	return InspectedImage{
		ByteSize:   len(imageBytes),
		Extension:  extension,
		Height:     height,
		MIMEType:   mimeType,
		Width:      width,
		FrameCount: frameCount,
	}, nil
}

// publish 对齐 Python publish，返回内容哈希与存储键
func (s *ImageAssetStore) Publish(imageBytes []byte, inspected InspectedImage) (string, string, error) {
	digest := ContentHash(imageBytes)
	storageKey := digest + "." + inspected.Extension
	target, err := s.resolve(storageKey)
	if err != nil {
		return "", "", err
	}
	info, statErr := os.Stat(target)
	if statErr == nil {
		// Python 对已存在的任意路径都先做大小与内容比对，目录同样按冲突处理
		if !info.Mode().IsRegular() || info.Size() != int64(len(imageBytes)) {
			return "", "", runtimeErrorf("内容寻址图片资产发生冲突: %s", storageKey)
		}
		existing, hashErr := hashFileSHA256(target)
		if hashErr != nil {
			return "", "", hashErr
		}
		if existing != digest {
			return "", "", runtimeErrorf("内容寻址图片资产发生冲突: %s", storageKey)
		}
		return digest, storageKey, nil
	}
	temporary, err := os.CreateTemp(s.root, "."+digest+".*.tmp")
	if err != nil {
		return "", "", err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if _, err := temporary.Write(imageBytes); err != nil {
		_ = temporary.Close()
		return "", "", err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", "", err
	}
	if err := temporary.Close(); err != nil {
		return "", "", err
	}
	if err := os.Rename(temporaryName, target); err != nil {
		if info, statErr := os.Stat(target); statErr == nil && info.Mode().IsRegular() && info.Size() == int64(len(imageBytes)) {
			if existing, hashErr := hashFileSHA256(target); hashErr == nil && existing == digest {
				return digest, storageKey, nil
			}
			return "", "", runtimeErrorf("内容寻址图片资产发生冲突: %s", storageKey)
		}
		return "", "", err
	}
	return digest, storageKey, nil
}

// ReadBytes 读取资产内容
func (s *ImageAssetStore) ReadBytes(storageKey string) ([]byte, error) {
	path, err := s.resolve(storageKey)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, notFoundErrorf("图片资产不存在: %s", storageKey)
		}
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, notFoundErrorf("图片资产不存在: %s", storageKey)
	}
	return os.ReadFile(path)
}

// Open 打开资产文件
func (s *ImageAssetStore) Open(storageKey string) (*os.File, error) {
	path, err := s.resolve(storageKey)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, notFoundErrorf("图片资产不存在: %s", storageKey)
		}
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, notFoundErrorf("图片资产不存在: %s", storageKey)
	}
	return os.Open(path)
}

// PathForRead 返回资产文件路径
func (s *ImageAssetStore) PathForRead(storageKey string) (string, error) {
	path, err := s.resolve(storageKey)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", notFoundErrorf("图片资产不存在: %s", storageKey)
		}
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", notFoundErrorf("图片资产不存在: %s", storageKey)
	}
	return path, nil
}

// Delete 删除资产文件，不存在返回 false
func (s *ImageAssetStore) Delete(storageKey string) (bool, error) {
	path, err := s.resolve(storageKey)
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if info.IsDir() {
		// Python Path.unlink 对目录抛错，Go os.Remove 会删除空目录，这里显式拒绝
		return false, fmt.Errorf("图片资产不是普通文件: %s", storageKey)
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}

// resolve 解析并校验存储键，只允许资产目录的直接子文件
func (s *ImageAssetStore) resolve(storageKey string) (string, error) {
	key := strings.ReplaceAll(pythonStrip(storageKey), "\\", "/")
	if key == "" || isAbsoluteAssetKey(key) || hasParentSegment(key) {
		return "", validationErrorf("图片资产键非法")
	}
	target := filepath.Join(s.root, filepath.FromSlash(key))
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		target = resolved
	}
	target = filepath.Clean(target)
	if !samePath(filepath.Dir(target), s.root) {
		return "", validationErrorf("图片资产键越出存储目录")
	}
	return target, nil
}

// isAbsoluteAssetKey 判断存储键是否为绝对路径
func isAbsoluteAssetKey(key string) bool {
	if filepath.VolumeName(key) != "" {
		return true
	}
	return strings.HasPrefix(key, "/")
}

// hasParentSegment 判断存储键是否含 .. 段
func hasParentSegment(key string) bool {
	for _, segment := range strings.Split(key, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

// samePath 按平台语义比较路径
func samePath(left string, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

// hashFileSHA256 计算文件 SHA-256
func hashFileSHA256(path string) (string, error) {
	handle, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer handle.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, handle); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// inspectImagePayload 只解析图片头部，等价 Pillow open/verify 的格式、尺寸与帧数
func inspectImagePayload(payload []byte) (string, int, int, int, error) {
	if len(payload) >= len(pngSignature) && bytes.Equal(payload[:len(pngSignature)], pngSignature) {
		return inspectPNG(payload)
	}
	if config, err := jpeg.DecodeConfig(bytes.NewReader(payload)); err == nil {
		return "JPEG", config.Width, config.Height, 1, nil
	}
	if config, err := gif.DecodeConfig(bytes.NewReader(payload)); err == nil {
		frames := 1
		if decoded, decodeErr := gif.DecodeAll(bytes.NewReader(payload)); decodeErr == nil && len(decoded.Image) > 0 {
			frames = len(decoded.Image)
		}
		return "GIF", config.Width, config.Height, frames, nil
	}
	if format, width, height, frames, ok := inspectWebP(payload); ok {
		return format, width, height, frames, nil
	}
	if config, err := bmp.DecodeConfig(bytes.NewReader(payload)); err == nil {
		return "BMP", config.Width, config.Height, 1, nil
	}
	if isTIFF(payload) {
		return "TIFF", 0, 0, 1, nil
	}
	if len(payload) >= 4 && payload[0] == 0 && payload[1] == 0 && payload[2] == 1 && payload[3] == 0 {
		return "ICO", 0, 0, 1, nil
	}
	if len(payload) >= 4 && string(payload[:4]) == "8BPS" {
		return "PSD", 0, 0, 1, nil
	}
	return "", 0, 0, 0, errUnidentifiedImage
}

// isTIFF 识别 TIFF 魔数
func isTIFF(payload []byte) bool {
	if len(payload) < 4 {
		return false
	}
	if payload[0] == 'I' && payload[1] == 'I' && payload[2] == 0x2a && payload[3] == 0 {
		return true
	}
	return payload[0] == 'M' && payload[1] == 'M' && payload[2] == 0 && payload[3] == 0x2a
}

// inspectPNG 校验 PNG 全部分块 CRC 与 IEND，并统计 APNG 帧数
func inspectPNG(payload []byte) (string, int, int, int, error) {
	config, err := png.DecodeConfig(bytes.NewReader(payload))
	if err != nil {
		return "", 0, 0, 0, err
	}
	frames, err := verifyPNGChunks(payload)
	if err != nil {
		return "", 0, 0, 0, err
	}
	if frames < 1 {
		frames = 1
	}
	return "PNG", config.Width, config.Height, frames, nil
}

// verifyPNGChunks 对齐 Pillow PngImagePlugin.verify 的逐块 CRC 校验
func verifyPNGChunks(payload []byte) (int, error) {
	offset := int64(len(pngSignature))
	total := int64(len(payload))
	frames := 1
	for {
		if offset+8 > total {
			return 0, errUnidentifiedImage
		}
		length := int64(binary.BigEndian.Uint32(payload[offset : offset+4]))
		chunkType := payload[offset+4 : offset+8]
		end := offset + 8 + length
		if length < 0 || end+4 > total {
			return 0, errUnidentifiedImage
		}
		data := payload[offset+8 : end]
		expected := binary.BigEndian.Uint32(payload[end : end+4])
		hasher := crc32.NewIEEE()
		_, _ = hasher.Write(chunkType)
		_, _ = hasher.Write(data)
		if hasher.Sum32() != expected {
			return 0, errUnidentifiedImage
		}
		if string(chunkType) == "acTL" && len(data) >= 4 {
			frames = int(binary.BigEndian.Uint32(data[:4]))
			if frames < 1 {
				frames = 1
			}
		}
		if string(chunkType) == "IEND" {
			return frames, nil
		}
		offset = end + 4
	}
}

// inspectWebP 解析 RIFF 结构，静态图用 x/image/webp 头部，动画用 VP8X/ANMF
func inspectWebP(payload []byte) (string, int, int, int, bool) {
	if len(payload) < 16 || string(payload[0:4]) != "RIFF" || string(payload[8:12]) != "WEBP" {
		return "", 0, 0, 0, false
	}
	riffEnd := int64(binary.LittleEndian.Uint32(payload[4:8])) + 8
	if riffEnd > int64(len(payload)) {
		riffEnd = int64(len(payload))
	}
	width, height := 0, 0
	frames := 0
	animated := false
	offset := int64(12)
	for offset+8 <= riffEnd {
		fourCC := string(payload[offset : offset+4])
		size := int64(binary.LittleEndian.Uint32(payload[offset+4 : offset+8]))
		dataStart := offset + 8
		dataEnd := dataStart + size
		if dataEnd > riffEnd {
			return "", 0, 0, 0, false
		}
		data := payload[dataStart:dataEnd]
		switch fourCC {
		case "VP8X":
			if len(data) >= 10 {
				animated = data[0]&0x02 != 0
				width = 1 + int(data[4]) + (int(data[5]) << 8) + (int(data[6]) << 16)
				height = 1 + int(data[7]) + (int(data[8]) << 8) + (int(data[9]) << 16)
			}
		case "ANMF":
			frames++
		case "ANIM":
			animated = true
		}
		offset = dataEnd
		if size%2 == 1 {
			offset++
		}
	}
	if animated {
		if frames < 1 || width <= 0 || height <= 0 {
			return "", 0, 0, 0, false
		}
		return "WEBP", width, height, frames, true
	}
	config, err := webp.DecodeConfig(bytes.NewReader(payload))
	if err != nil {
		return "", 0, 0, 0, false
	}
	return "WEBP", config.Width, config.Height, 1, true
}

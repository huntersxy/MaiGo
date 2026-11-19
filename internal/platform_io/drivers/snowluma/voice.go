package snowluma

import "bytes"

// silk 缺依赖/转码失败时降级为文本占位 [voice]（optionalDivergence）。

var silkMagicPrefixes = [][]byte{
	[]byte("#!SILK_V3"),
	{0x02, '#', '!', 'S', 'I', 'L', 'K', '_', 'V', '3'},
}

// IsSilkVoiceBinary 按 magic 头判断 silk
func IsSilkVoiceBinary(data []byte) bool {
	for _, prefix := range silkMagicPrefixes {
		if bytes.HasPrefix(data, prefix) {
			return true
		}
	}
	return false
}

// TranscodeSilkVoiceBinary P0：未引入 silk/ffmpeg 依赖，一律返回 nil 触发占位文本。
func TranscodeSilkVoiceBinary(data []byte) []byte {
	_ = data
	return nil
}

// VoicePlaceholder 对齐 Python 依赖缺失时的降级文本
func VoicePlaceholder() string {
	return "[voice]"
}

package utils

// 元数据值归一化，对齐 A_memorix core/utils/metadata.py

// CoerceMetadataDict 返回字典副本，非字典输入返回空字典
func CoerceMetadataDict(value any) map[string]any {
	switch typed := value.(type) {
	case nil:
		return map[string]any{}
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = item
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = item
		}
		return out
	default:
		return map[string]any{}
	}
}

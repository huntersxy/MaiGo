package embedding

// 向量辅助函数，供管理器缓存与适配器校验复用

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
)

// cloneVector 复制向量，对齐 Python 中 embedding.copy() 的防御性拷贝语义
func cloneVector(vector []float32) []float32 {
	if vector == nil {
		return nil
	}
	cloned := make([]float32, len(vector))
	copy(cloned, vector)
	return cloned
}

// vectorIsFinite 判断向量是否全部为有限值
func vectorIsFinite(vector []float32) bool {
	for _, value := range vector {
		converted := float64(value)
		if math.IsNaN(converted) || math.IsInf(converted, 0) {
			return false
		}
	}
	return true
}

// sha256Hex 计算字节串的 SHA256 十六进制摘要，对齐 hashlib.sha256(...).hexdigest()
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// validateEmbeddingVector 校验嵌入向量，对齐 _validate_embedding_vector 的可表达部分
// Go 切片天然一维，Python 的 ndim 校验无对应场景，仅在报告中标注
func validateEmbeddingVector(vector []float32, source string) error {
	if len(vector) == 0 {
		return fmt.Errorf("%s 返回了空 embedding", source)
	}
	if !vectorIsFinite(vector) {
		return fmt.Errorf("%s 返回了非有限 embedding 值", source)
	}
	return nil
}

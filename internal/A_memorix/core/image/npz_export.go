package imagememory

// 通用向量 npz 编解码，供知识包文本与图谱向量复用图片向量的 numpy 兼容实现

// VectorNPZ 表示记忆包中的向量数组
type VectorNPZ struct {
	IDs            []string
	Vectors        [][]float32
	Rows           int
	Cols           int
	TwoDimensional bool
}

// EncodeVectorNPZ 生成与 np.savez_compressed(ids=..., vectors=...) 兼容的压缩包
func EncodeVectorNPZ(ids []string, vectors [][]float32) ([]byte, error) {
	return encodeImageVectorsNPZ(ids, vectors)
}

// DecodeVectorNPZ 解析记忆包中的向量数组
func DecodeVectorNPZ(payload []byte) (*VectorNPZ, error) {
	decoded, err := decodeImageVectorsNPZ(payload)
	if err != nil {
		return nil, err
	}
	return &VectorNPZ{
		IDs:            decoded.IDs,
		Vectors:        decoded.Vectors,
		Rows:           decoded.Rows,
		Cols:           decoded.Cols,
		TwoDimensional: decoded.TwoDimensional,
	}, nil
}

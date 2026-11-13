package utils

// A_memorix 向量量化工具，对齐 Python core/utils/quantization.py
// Python 侧全程用 float32 ndarray 运算，这里也逐步用 float32 计算并保持相同的运算顺序，
// 因此 scale、round、clip 的舍入结果与 numpy 一致
// 注意 Python 把 (min, max) 存进 _scalar_quantize_int8._params 的进程级字典，
// Go 侧没有等价的全局注册表，改为由 Int8Vector 显式携带

import (
	"fmt"
	"math"
	"strconv"
)

// QuantizationType 量化类型，取值与 Python QuantizationType.value 一致
type QuantizationType string

const (
	QuantizationFloat32 QuantizationType = "float32"
	QuantizationInt8    QuantizationType = "int8"
	QuantizationPQ      QuantizationType = "pq"
)

// Valid 判断是否为已知量化类型
func (t QuantizationType) Valid() bool {
	switch t {
	case QuantizationFloat32, QuantizationInt8, QuantizationPQ:
		return true
	default:
		return false
	}
}

// String 对齐 Python 的 quant_type.value
func (t QuantizationType) String() string {
	return string(t)
}

// Int8Vector 标量量化结果，对齐 quantize_vector(INT8) 返回的 int8 向量
// Min/Max 对应 Python 存进 _params 的归一化参数
type Int8Vector struct {
	Values []int8
	Min    float32
	Max    float32
}

// PQVector 乘积量化结果，对齐 quantize_vector(PQ) 返回的 (codes, centroids)
// Centroids 是 [M][K][DS] 的展平 float32 数组
type PQVector struct {
	Codes     []uint8
	Centroids []float32
	M         int
	K         int
	DS        int
}

// QuantizeVectorFloat32 对齐 quantize_vector(FLOAT32)，即 astype(np.float32)
func QuantizeVectorFloat32(vector []float32) []float32 {
	out := make([]float32, len(vector))
	copy(out, vector)
	return out
}

// QuantizeVectorInt8 对齐 _scalar_quantize_int8
func QuantizeVectorInt8(vector []float32) (Int8Vector, error) {
	if len(vector) == 0 {
		return Int8Vector{}, fmt.Errorf("zero-size array to reduction operation minimum which has no identity")
	}
	minVal, maxVal := float32MinMax(vector)
	values := make([]int8, len(vector))
	if maxVal == minVal {
		return Int8Vector{Values: values, Min: minVal, Max: maxVal}, nil
	}
	span := maxVal - minVal
	for i, value := range vector {
		// 对齐 (vector - min) / (max - min) * 255 的 float32 逐步运算
		delta := value - minVal
		ratio := delta / span
		scaled := ratio * 255
		shifted := scaled - 128
		values[i] = numpyFloatToInt8(float64(math.RoundToEven(float64(shifted))))
	}
	return Int8Vector{Values: values, Min: minVal, Max: maxVal}, nil
}

// QuantizeVector 对齐 quantize_vector 的分发逻辑
// FLOAT32 返回 []float32，INT8 返回 Int8Vector，PQ 返回 PQVector（子空间数 8、聚类中心 256）
func QuantizeVector(vector []float32, quantType QuantizationType) (any, error) {
	switch quantType {
	case QuantizationFloat32:
		return QuantizeVectorFloat32(vector), nil
	case QuantizationInt8:
		return QuantizeVectorInt8(vector)
	case QuantizationPQ:
		return QuantizeVectorPQ(vector, 8, 256)
	default:
		return nil, fmt.Errorf("不支持的量化类型: %s", quantType)
	}
}

// DequantizeVectorFloat32 对齐 dequantize_vector(FLOAT32)
func DequantizeVectorFloat32(vector []float32) []float32 {
	out := make([]float32, len(vector))
	copy(out, vector)
	return out
}

// DequantizeVectorInt8 对齐 _scalar_dequantize_int8 在 _params 不存在时的分支
// 即默认按 [-1, 1] 范围假设：(q + 128) / 255 * 2 - 1
func DequantizeVectorInt8(quantized []int8) []float32 {
	out := make([]float32, len(quantized))
	for i, value := range quantized {
		asFloat := float32(value)
		out[i] = (asFloat+128.0)/255.0*2.0 - 1.0
	}
	return out
}

// DequantizeVectorInt8Normalized 对齐 _scalar_dequantize_int8 在 _params 存在时的分支
// 即已归一化的数据：(q + 128) / 255
func DequantizeVectorInt8Normalized(quantized []int8) []float32 {
	out := make([]float32, len(quantized))
	for i, value := range quantized {
		asFloat := float32(value)
		out[i] = (asFloat + 128.0) / 255.0
	}
	return out
}

// RecoverInt8Vector 用 Python 注释里给出的完整恢复公式还原原始量级
// 公式：(q + 128) / 255 * (max - min) + min；Python 代码本身没有实现这一分支
func RecoverInt8Vector(quantized Int8Vector) []float32 {
	span := quantized.Max - quantized.Min
	out := make([]float32, len(quantized.Values))
	for i, value := range quantized.Values {
		asFloat := float32(value)
		out[i] = (asFloat+128.0)/255.0*span + quantized.Min
	}
	return out
}

// DequantizeVectorPQ 对齐 _product_dequantize
func DequantizeVectorPQ(quantized PQVector) ([]float32, error) {
	if quantized.M*quantized.K*quantized.DS != len(quantized.Centroids) || len(quantized.Codes) != quantized.M {
		return nil, fmt.Errorf("PQ 编码与聚类中心形状不匹配")
	}
	out := make([]float32, quantized.M*quantized.DS)
	for i := 0; i < quantized.M; i++ {
		code := int(quantized.Codes[i])
		if code >= quantized.K {
			return nil, fmt.Errorf("index %d is out of bounds for axis 0 with size %d", code, quantized.K)
		}
		offset := (i*quantized.K + code) * quantized.DS
		copy(out[i*quantized.DS:(i+1)*quantized.DS], quantized.Centroids[offset:offset+quantized.DS])
	}
	return out, nil
}

// DequantizeVector 对齐 dequantize_vector 的分发逻辑
// quantized 需与类型匹配：FLOAT32/INT8 传 []int8 或 Int8Vector，PQ 传 PQVector
func DequantizeVector(quantized any, quantType QuantizationType) ([]float32, error) {
	switch quantType {
	case QuantizationFloat32:
		values, ok := quantized.([]float32)
		if !ok {
			return nil, fmt.Errorf("float32 反量化需要 float32 向量")
		}
		return DequantizeVectorFloat32(values), nil
	case QuantizationInt8:
		values, ok := quantized.([]int8)
		if !ok {
			return nil, fmt.Errorf("int8 反量化需要 int8 向量")
		}
		return DequantizeVectorInt8(values), nil
	case QuantizationPQ:
		code, ok := quantized.(PQVector)
		if !ok {
			return nil, fmt.Errorf("PQ反量化需要列表/元组格式: (codes, centroids)")
		}
		return DequantizeVectorPQ(code)
	default:
		return nil, fmt.Errorf("不支持的量化类型: %s", quantType)
	}
}

// QuantizeMatrixFloat32 对齐 quantize_matrix(FLOAT32)
func QuantizeMatrixFloat32(matrix [][]float32) [][]float32 {
	out := make([][]float32, len(matrix))
	for i, row := range matrix {
		out[i] = make([]float32, len(row))
		copy(out[i], row)
	}
	return out
}

// QuantizeMatrixInt8 对齐 quantize_matrix(INT8)：整矩阵全局归一化
// 注意 Python 这里没有减 128，0..255 直接 astype(int8) 会按低 8 位回绕，这里保持一致
func QuantizeMatrixInt8(matrix [][]float32) ([][]int8, error) {
	minVal, maxVal, ok := matrixMinMax(matrix)
	if !ok {
		return nil, fmt.Errorf("zero-size array to reduction operation minimum which has no identity")
	}
	out := make([][]int8, len(matrix))
	for i, row := range matrix {
		out[i] = make([]int8, len(row))
	}
	if maxVal == minVal {
		return out, nil
	}
	span := maxVal - minVal
	for i, row := range matrix {
		for j, value := range row {
			delta := value - minVal
			ratio := delta / span
			scaled := ratio * 255
			out[i][j] = numpyFloatToInt8(float64(math.RoundToEven(float64(scaled))))
		}
	}
	return out, nil
}

// DequantizeMatrixFloat32 对齐 dequantize_matrix(FLOAT32)
func DequantizeMatrixFloat32(matrix [][]float32) [][]float32 {
	return QuantizeMatrixFloat32(matrix)
}

// DequantizeMatrixInt8 对齐 dequantize_matrix(INT8)
// minVal/maxVal 都提供时按 q / 255 * (max - min) + min 还原，否则按默认 q / 127
func DequantizeMatrixInt8(matrix [][]int8, minVal *float64, maxVal *float64) [][]float32 {
	out := make([][]float32, len(matrix))
	for i, row := range matrix {
		out[i] = make([]float32, len(row))
	}
	if minVal == nil || maxVal == nil {
		for i, row := range matrix {
			for j, value := range row {
				out[i][j] = float32(value) / 127.0
			}
		}
		return out
	}
	span := float32(*maxVal - *minVal)
	low := float32(*minVal)
	for i, row := range matrix {
		for j, value := range row {
			normalized := float32(value) / 255.0
			out[i][j] = normalized*span + low
		}
	}
	return out
}

// QuantizeVectorPQ 对齐 _product_quantize
// 按子空间最小值/最大值均匀切分出 k 个量化点，再用子空间均值找最近的量化点
func QuantizeVectorPQ(vector []float32, m int, k int) (PQVector, error) {
	d := len(vector)
	if d == 0 {
		return PQVector{}, fmt.Errorf("zero-size array to reduction operation minimum which has no identity")
	}
	if m <= 0 {
		return PQVector{}, fmt.Errorf("integer modulo by zero")
	}
	if d%m != 0 {
		return PQVector{}, fmt.Errorf("维度 %d 必须能被子空间数量 %d 整除", d, m)
	}
	if k <= 0 {
		return PQVector{}, fmt.Errorf("attempt to get argmin of an empty sequence")
	}
	ds := d / m
	codes := make([]uint8, m)
	centroids := make([]float32, m*k*ds)
	linspace := make([]float32, k)
	for i := 0; i < m; i++ {
		sub := vector[i*ds : (i+1)*ds]
		subMin, subMax := float32MinMax(sub)
		if k == 1 {
			linspace[0] = subMin
		} else if subMax == subMin {
			for j := range linspace {
				linspace[j] = 0
			}
		} else {
			// np.linspace 的 float32 实现：y = arange * step + start，末位直接取 stop
			step := (subMax - subMin) / float32(k-1)
			for j := 0; j < k-1; j++ {
				linspace[j] = subMin + float32(j)*step
			}
			linspace[k-1] = subMax
		}
		for j := 0; j < k; j++ {
			offset := (i*k + j) * ds
			for t := 0; t < ds; t++ {
				centroids[offset+t] = linspace[j]
			}
		}
		mean := float32Mean(sub)
		best := 0
		bestDistance := float32(math.Abs(float64(linspace[0] - mean)))
		for j := 1; j < k; j++ {
			distance := float32(math.Abs(float64(linspace[j] - mean)))
			if distance < bestDistance {
				bestDistance = distance
				best = j
			}
		}
		codes[i] = uint8(best)
	}
	return PQVector{Codes: codes, Centroids: centroids, M: m, K: k, DS: ds}, nil
}

// EstimateMemoryReduction 估算内存节省量，对齐 estimate_memory_reduction
// 返回 (原始大小MB, 量化后大小MB, 节省比例)
func EstimateMemoryReduction(numVectors int, dimension int, fromType QuantizationType, toType QuantizationType) (float64, float64, float64, error) {
	bytesPerElement := map[QuantizationType]float64{
		QuantizationFloat32: 4,
		QuantizationInt8:    1,
		QuantizationPQ:      0.25,
	}
	fromBytes, ok := bytesPerElement[fromType]
	if !ok {
		return 0, 0, 0, fmt.Errorf("不支持的量化类型: %s", fromType)
	}
	toBytes, ok := bytesPerElement[toType]
	if !ok {
		return 0, 0, 0, fmt.Errorf("不支持的量化类型: %s", toType)
	}
	originalBytes := float64(numVectors) * float64(dimension) * fromBytes
	quantizedBytes := float64(numVectors) * float64(dimension) * toBytes
	originalMB := originalBytes / 1024 / 1024
	quantizedMB := quantizedBytes / 1024 / 1024
	if originalBytes == 0 {
		// 对齐 Python：整数相除报 division by zero，含 PQ 的浮点相除报 float division by zero
		if fromBytes == 4 || fromBytes == 1 {
			return 0, 0, 0, fmt.Errorf("division by zero")
		}
		return 0, 0, 0, fmt.Errorf("float division by zero")
	}
	reductionRatio := (originalBytes - quantizedBytes) / originalBytes
	return originalMB, quantizedMB, reductionRatio, nil
}

// CompressionStats 对齐 estimate_compression_stats 返回的字典，字段名即 Python 的键名
type CompressionStats struct {
	NumVectors       int     `json:"num_vectors"`
	Dimension        int     `json:"dimension"`
	QuantizationType string  `json:"quantization_type"`
	OriginalSizeMB   float64 `json:"original_size_mb"`
	QuantizedSizeMB  float64 `json:"quantized_size_mb"`
	SavedMB          float64 `json:"saved_mb"`
	CompressionRatio float64 `json:"compression_ratio"`
}

// EstimateCompressionStats 估算压缩统计信息，对齐 estimate_compression_stats
// 各尺寸字段对齐 Python round(x, 2) 的银行家舍入
func EstimateCompressionStats(numVectors int, dimension int, quantType QuantizationType) (CompressionStats, error) {
	originalMB, quantizedMB, ratio, err := EstimateMemoryReduction(numVectors, dimension, QuantizationFloat32, quantType)
	if err != nil {
		return CompressionStats{}, err
	}
	return CompressionStats{
		NumVectors:       numVectors,
		Dimension:        dimension,
		QuantizationType: string(quantType),
		OriginalSizeMB:   pyRound(originalMB, 2),
		QuantizedSizeMB:  pyRound(quantizedMB, 2),
		SavedMB:          pyRound(originalMB-quantizedMB, 2),
		CompressionRatio: pyRound(ratio*100, 2),
	}, nil
}

// pyRound 对齐 Python round(value, digits)：按精确二进制值做正确舍入，平局取偶
// 用 strconv 的定点格式化实现（Go 的十进制转换同样是正确舍入且平局取偶）
func pyRound(value float64, digits int) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return value
	}
	rounded, err := strconv.ParseFloat(strconv.FormatFloat(value, 'f', digits, 64), 64)
	if err != nil {
		return value
	}
	return rounded
}

// numpyFloatToInt8 复刻 numpy 的 float -> int8 astype 语义
// 向零截断后按低 8 位回绕；NaN、正负 Inf 以及超出 int64 范围的取值在 numpy 2.5/x86 上得到 0
func numpyFloatToInt8(value float64) int8 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	if math.Abs(value) >= 9223372036854775808.0 {
		return 0
	}
	return int8(int64(value))
}

// float32MinMax 对齐 np.min / np.max：任一元素为 NaN 时结果整体为 NaN
func float32MinMax(values []float32) (float32, float32) {
	for _, value := range values {
		if value != value {
			nan := float32(math.NaN())
			return nan, nan
		}
	}
	minVal, maxVal := values[0], values[0]
	for _, value := range values[1:] {
		if value < minVal {
			minVal = value
		}
		if value > maxVal {
			maxVal = value
		}
	}
	return minVal, maxVal
}

// matrixMinMax 对齐整矩阵的 np.min / np.max，空矩阵返回 ok=false
func matrixMinMax(matrix [][]float32) (float32, float32, bool) {
	minVal, maxVal := float32(0), float32(0)
	found := false
	for _, row := range matrix {
		for _, value := range row {
			if value != value {
				nan := float32(math.NaN())
				return nan, nan, true
			}
			if !found {
				minVal, maxVal = value, value
				found = true
				continue
			}
			if value < minVal {
				minVal = value
			}
			if value > maxVal {
				maxVal = value
			}
		}
	}
	if !found {
		return 0, 0, false
	}
	return minVal, maxVal, true
}

// float32Mean 对齐 np.mean(float32)：float32 累加后除以元素个数
// numpy 在元素数超过 8 时使用成对累加，这里用顺序累加，仅在末位存在极微小差异
func float32Mean(values []float32) float32 {
	sum := float32(0)
	for _, value := range values {
		sum += value
	}
	return sum / float32(len(values))
}

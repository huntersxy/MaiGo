package storage

// A_memorix 向量存储的底层编解码工具
// 覆盖 JSON 元数据读写、float16 半精度转换、npy/npz 读写与多重集比较

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	memorixutils "MaiGo/internal/A_memorix/core/utils"
)

// readJSONObject 读取 JSON 对象，对齐 _read_json_object
// 使用 json.Number 保留整数字面量，避免大整数被浮点化
func readJSONObject(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("JSON 元数据必须是对象: %s", path)
	}
	return object, nil
}

// writeJSONObject 原子写入 JSON 对象，对齐 _write_json_object
// 键按字典序、缩进 2 空格、非 ASCII 不转义，并以换行结尾
func writeJSONObject(path string, payload map[string]any) error {
	return memorixutils.AtomicWriteFile(path, 0o666, func(file *os.File) error {
		encoder := json.NewEncoder(file)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(payload); err != nil {
			return err
		}
		return file.Sync()
	})
}

// jsonInteger 判断 JSON 数值是否为整数，对齐 Python 的 int（排除 bool）
func jsonInteger(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case int:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case uint:
		return int64(typed), true
	case uint64:
		if typed > math.MaxInt64 {
			return 0, false
		}
		return int64(typed), true
	}
	return 0, false
}

// jsonStringSlice 取第一个非空值解析为字符串列表，对齐 _json_string_list
// Python 侧用 known_hashes 优先、ids 兜底，因此这里接受可变参数
func jsonStringSlice(values ...any) ([]string, error) {
	var selected any
	found := false
	for _, value := range values {
		if value != nil {
			selected = value
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("缺少字符串列表字段")
	}
	items, ok := selected.([]any)
	if !ok {
		return nil, fmt.Errorf("字符串列表字段类型无效")
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok || text == "" {
			return nil, fmt.Errorf("字符串列表字段存在非法元素")
		}
		out = append(out, text)
	}
	return out, nil
}

// jsonInt64Slice 解析整数列表，缺省视为空列表，对齐 _json_int_list
func jsonInt64Slice(value any) ([]int64, error) {
	if value == nil {
		return []int64{}, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("整数列表字段类型无效")
	}
	out := make([]int64, 0, len(items))
	for _, item := range items {
		parsed, ok := jsonInteger(item)
		if !ok {
			return nil, fmt.Errorf("整数列表字段存在非法元素")
		}
		out = append(out, parsed)
	}
	return out, nil
}

// asJSONObject 把任意值转成 JSON 对象，非对象返回 nil
func asJSONObject(value any) map[string]any {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return object
}

// pyFormatValue 近似 Python 的 repr，用于对齐错误文案
func pyFormatValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case string:
		return "'" + typed + "'"
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case json.Number:
		return typed.String()
	case int:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	}
	return fmt.Sprintf("%v", value)
}

// multisetDifference 统计左侧比右侧多出的元素数，对齐 Counter 差集求和
func multisetDifference(left map[int64]int, right map[int64]int) int {
	total := 0
	for key, count := range left {
		if extra := count - right[key]; extra > 0 {
			total += extra
		}
	}
	return total
}

// multisetEqual 判断两个多重集是否完全一致
func multisetEqual(left map[int64]int, right map[int64]int) bool {
	if len(left) != len(right) {
		return false
	}
	for key, count := range left {
		if right[key] != count {
			return false
		}
	}
	return true
}

// readVectorIDFile 读取大端 int64 的向量 ID 文件
func readVectorIDFile(path string) ([]int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw)%8 != 0 {
		return nil, fmt.Errorf("向量 ID 文件长度不是 8 的整数倍: %d", len(raw))
	}
	out := make([]int64, 0, len(raw)/8)
	for offset := 0; offset < len(raw); offset += 8 {
		out = append(out, int64(binary.BigEndian.Uint64(raw[offset:offset+8])))
	}
	return out, nil
}

// encodeInt64List 把 ID 列表编码为大端 int64 字节串
func encodeInt64List(ids []int64) []byte {
	out := make([]byte, len(ids)*8)
	for index, value := range ids {
		binary.BigEndian.PutUint64(out[index*8:], uint64(value))
	}
	return out
}

// float16ToFloat32 把 IEEE754 半精度位模式转成 float32
func float16ToFloat32(bits uint16) float32 {
	sign := uint32(bits>>15) & 0x1
	exponent := uint32(bits>>10) & 0x1F
	mantissa := uint32(bits) & 0x3FF
	switch {
	case exponent == 0:
		if mantissa == 0 {
			return math.Float32frombits(sign << 31)
		}
		// 次正规数，逐位归一化到单精度
		shift := uint32(0)
		for mantissa&0x400 == 0 {
			mantissa <<= 1
			shift++
		}
		mantissa &= 0x3FF
		exponent32 := uint32(127-15-int32(shift)) + 1
		return math.Float32frombits(sign<<31 | exponent32<<23 | mantissa<<13)
	case exponent == 0x1F:
		return math.Float32frombits(sign<<31 | 0xFF<<23 | mantissa<<13)
	default:
		return math.Float32frombits(sign<<31 | (exponent+127-15)<<23 | mantissa<<13)
	}
}

// float32ToFloat16 把 float32 舍入到最近偶数后转成半精度位模式
func float32ToFloat16(value float32) uint16 {
	bits := math.Float32bits(value)
	sign := uint16(bits>>16) & 0x8000
	exponent := int32(bits>>23) & 0xFF
	mantissa := bits & 0x7FFFFF
	switch {
	case exponent == 0xFF:
		if mantissa != 0 {
			return sign | 0x7E00
		}
		return sign | 0x7C00
	case exponent == 0:
		// 单精度次正规数低于半精度可表示范围，直接归零
		return sign
	}
	shifted := exponent - 127 + 15
	if shifted >= 0x1F {
		return sign | 0x7C00
	}
	if shifted <= 0 {
		if shifted < -10 {
			return sign
		}
		// 退化为半精度次正规数
		mantissa |= 0x800000
		drop := uint32(14 - shifted)
		half := mantissa >> drop
		remainder := mantissa & ((1 << drop) - 1)
		halfway := uint32(1) << (drop - 1)
		if remainder > halfway || (remainder == halfway && half&1 == 1) {
			half++
		}
		return sign | uint16(half)
	}
	half := uint16(shifted)<<10 | uint16(mantissa>>13)
	remainder := mantissa & 0x1FFF
	if remainder > 0x1000 || (remainder == 0x1000 && half&1 == 1) {
		half++
	}
	return sign | half
}

// decodeFloat16Row 把一段 float16 行解码为 float32
func decodeFloat16Row(raw []byte, dimension int) []float32 {
	out := make([]float32, dimension)
	for index := 0; index < dimension; index++ {
		offset := index * 2
		if offset+2 > len(raw) {
			out[index] = 0
			continue
		}
		out[index] = float16ToFloat32(binary.LittleEndian.Uint16(raw[offset:]))
	}
	return out
}

// encodeFloat16Row 把 float32 行编码为 float16 字节串
func encodeFloat16Row(vector []float32) []byte {
	out := make([]byte, len(vector)*2)
	for index, value := range vector {
		binary.LittleEndian.PutUint16(out[index*2:], float32ToFloat16(value))
	}
	return out
}

// decodeFloat32Row 把一段 float32 行解码为 float32
func decodeFloat32Row(raw []byte, dimension int) []float32 {
	out := make([]float32, dimension)
	for index := 0; index < dimension; index++ {
		offset := index * 4
		if offset+4 > len(raw) {
			out[index] = 0
			continue
		}
		out[index] = math.Float32frombits(binary.LittleEndian.Uint32(raw[offset:]))
	}
	return out
}

// encodeFloat32Row 把 float32 行编码为 float32 字节串
func encodeFloat32Row(vector []float32) []byte {
	out := make([]byte, len(vector)*4)
	for index, value := range vector {
		binary.LittleEndian.PutUint32(out[index*4:], math.Float32bits(value))
	}
	return out
}

// vectorItemSize 返回给定编码下单条向量的字节宽度
func vectorItemSize(dimension int, dtype string) int {
	if dtype == VectorDtypeFloat16 {
		return dimension * 2
	}
	return dimension * 4
}

// decodeVectorRow 按 dtype 解码一条向量
func decodeVectorRow(raw []byte, dimension int, dtype string) []float32 {
	if dtype == VectorDtypeFloat16 {
		return decodeFloat16Row(raw, dimension)
	}
	return decodeFloat32Row(raw, dimension)
}

// encodeVectorRow 按 dtype 编码一条向量
func encodeVectorRow(vector []float32, dtype string) []byte {
	if dtype == VectorDtypeFloat16 {
		return encodeFloat16Row(vector)
	}
	return encodeFloat32Row(vector)
}

// encodeVectorRows 按 dtype 拼接多条向量，写成 append-only 的连续行
func encodeVectorRows(vectors [][]float32, dtype string) []byte {
	rowSize := 0
	if len(vectors) > 0 {
		rowSize = vectorItemSize(len(vectors[0]), dtype)
	}
	out := make([]byte, 0, len(vectors)*rowSize)
	for _, vector := range vectors {
		out = append(out, encodeVectorRow(vector, dtype)...)
	}
	return out
}

// normalizeVector 原地做 L2 归一化，零范数按 1e-12 兜底
func normalizeVector(vector []float32) {
	norm := vectorNormValue(vector)
	if norm < 1e-12 {
		norm = 1e-12
	}
	for index := range vector {
		vector[index] = float32(float64(vector[index]) / norm)
	}
}

// vectorNormValue 返回向量的 L2 范数
func vectorNormValue(vector []float32) float64 {
	total := 0.0
	for _, value := range vector {
		total += float64(value) * float64(value)
	}
	return math.Sqrt(total)
}

// dotProduct 计算内积，内部用 float64 累加保证精度
func dotProduct(left []float32, right []float32) float32 {
	total := 0.0
	for index := range left {
		total += float64(left[index]) * float64(right[index])
	}
	return float32(total)
}

// npyArray 描述一个 npy 数组
type npyArray struct {
	Descr  string
	Shape  []int
	Data   []byte
	Length int
}

// npyMagic 是 npy 文件固定魔数
var npyMagic = []byte{0x93, 'N', 'U', 'M', 'P', 'Y'}

var (
	npyDescrPattern = regexp.MustCompile("'descr'\\s*:\\s*'([^']+)'")
	npyShapePattern = regexp.MustCompile("'shape'\\s*:\\s*\\(([^)]*)\\)")
)

// readNpyFile 读取最小可用的 npy 数组，只支持 1/2 维 float32/float16/float64 的 C 序数据
func readNpyFile(path string) (npyArray, error) {
	var array npyArray
	raw, err := os.ReadFile(path)
	if err != nil {
		return array, err
	}
	if len(raw) < 10 || !bytes.Equal(raw[:6], npyMagic) {
		return array, fmt.Errorf("不是合法的 npy 文件: %s", path)
	}
	major := raw[6]
	headerStart := 0
	headerLength := 0
	if major == 1 {
		headerStart = 10
		headerLength = int(binary.LittleEndian.Uint16(raw[8:10]))
	} else if major == 2 || major == 3 {
		if len(raw) < 12 {
			return array, fmt.Errorf("npy 文件头不完整: %s", path)
		}
		headerStart = 12
		headerLength = int(binary.LittleEndian.Uint32(raw[8:12]))
	} else {
		return array, fmt.Errorf("不支持的 npy 版本: %d", major)
	}
	if headerStart+headerLength > len(raw) {
		return array, fmt.Errorf("npy 文件头长度越界: %s", path)
	}
	header := string(raw[headerStart : headerStart+headerLength])
	descrMatch := npyDescrPattern.FindStringSubmatch(header)
	if descrMatch == nil {
		return array, fmt.Errorf("npy 文件头缺少 descr: %s", path)
	}
	array.Descr = descrMatch[1]
	if strings.Contains(header, "'fortran_order': True") {
		return array, fmt.Errorf("不支持 Fortran 顺序的 npy 文件: %s", path)
	}
	shapeMatch := npyShapePattern.FindStringSubmatch(header)
	if shapeMatch == nil {
		return array, fmt.Errorf("npy 文件头缺少 shape: %s", path)
	}
	for _, piece := range strings.Split(shapeMatch[1], ",") {
		piece = strings.TrimSpace(piece)
		if piece == "" {
			continue
		}
		value, convErr := strconv.Atoi(piece)
		if convErr != nil {
			return array, fmt.Errorf("npy 文件头 shape 无效: %s", path)
		}
		array.Shape = append(array.Shape, value)
	}
	if len(array.Shape) > 2 {
		return array, fmt.Errorf("npy 数组维度过多: %s", path)
	}
	array.Data = raw[headerStart+headerLength:]
	count := 1
	for _, dimension := range array.Shape {
		count *= dimension
	}
	array.Length = count
	switch strings.TrimLeft(array.Descr, "<>=|") {
	case "f2", "f4", "f8":
	default:
		return array, fmt.Errorf("不支持的 npy dtype: %s", array.Descr)
	}
	return array, nil
}

// npyFloat32 把 npy 数组转换成 float32 切片
func npyFloat32(array npyArray) ([]float32, error) {
	bigEndian := strings.HasPrefix(array.Descr, ">")
	descr := strings.TrimLeft(array.Descr, "<>=|")
	out := make([]float32, array.Length)
	for index := 0; index < array.Length; index++ {
		switch descr {
		case "f4":
			offset := index * 4
			if offset+4 > len(array.Data) {
				return nil, fmt.Errorf("npy 数据长度不足")
			}
			bits := binary.LittleEndian.Uint32(array.Data[offset:])
			if bigEndian {
				bits = binary.BigEndian.Uint32(array.Data[offset:])
			}
			out[index] = math.Float32frombits(bits)
		case "f8":
			offset := index * 8
			if offset+8 > len(array.Data) {
				return nil, fmt.Errorf("npy 数据长度不足")
			}
			bits := binary.LittleEndian.Uint64(array.Data[offset:])
			if bigEndian {
				bits = binary.BigEndian.Uint64(array.Data[offset:])
			}
			out[index] = float32(math.Float64frombits(bits))
		case "f2":
			offset := index * 2
			if offset+2 > len(array.Data) {
				return nil, fmt.Errorf("npy 数据长度不足")
			}
			bits := binary.LittleEndian.Uint16(array.Data[offset:])
			if bigEndian {
				bits = binary.BigEndian.Uint16(array.Data[offset:])
			}
			out[index] = float16ToFloat32(bits)
		default:
			return nil, fmt.Errorf("不支持的 npy dtype: %s", array.Descr)
		}
	}
	return out, nil
}

// buildNpyBytes 生成一个 npy 文件字节串，descr 形如 "<f2"、"<i8"
func buildNpyBytes(descr string, shape []int, payload []byte) []byte {
	parts := make([]string, 0, len(shape))
	for _, dimension := range shape {
		parts = append(parts, strconv.Itoa(dimension))
	}
	shapeText := "(" + strings.Join(parts, ", ")
	if len(shape) == 1 {
		shapeText += ","
	}
	shapeText += ")"
	header := fmt.Sprintf("{'descr': '%s', 'fortran_order': False, 'shape': %s, }", descr, shapeText)
	padding := 64 - (10+len(header)+1)%64
	if padding == 64 {
		padding = 0
	}
	header += strings.Repeat(" ", padding) + "\n"
	out := make([]byte, 0, 10+len(header)+len(payload))
	out = append(out, npyMagic...)
	out = append(out, 1, 0)
	out = append(out, byte(len(header)), byte(len(header)>>8))
	out = append(out, header...)
	out = append(out, payload...)
	return out
}

// npyFloat16Payload 把 float32 数据编码为 float16 小端负载
func npyFloat16Payload(values []float32) []byte {
	out := make([]byte, len(values)*2)
	for index, value := range values {
		binary.LittleEndian.PutUint16(out[index*2:], float32ToFloat16(value))
	}
	return out
}

// npyInt64Payload 把 int64 数据编码为小端负载
func npyInt64Payload(values []int64) []byte {
	out := make([]byte, len(values)*8)
	for index, value := range values {
		binary.LittleEndian.PutUint64(out[index*8:], uint64(value))
	}
	return out
}

// npzMember 描述 npz 归档中的单个数组
type npzMember struct {
	Name    string
	Descr   string
	Shape   []int
	Payload []byte
}

// writeNpzFile 写出 npz 归档，成员名自动追加 .npy 后缀
func writeNpzFile(path string, members []npzMember) error {
	buffer := &bytes.Buffer{}
	archive := zip.NewWriter(buffer)
	ordered := append([]npzMember(nil), members...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	for _, member := range ordered {
		writer, err := archive.Create(member.Name + ".npy")
		if err != nil {
			return err
		}
		if _, err := writer.Write(buildNpyBytes(member.Descr, member.Shape, member.Payload)); err != nil {
			return err
		}
	}
	if err := archive.Close(); err != nil {
		return err
	}
	return memorixutils.AtomicWriteFile(path, 0o666, func(file *os.File) error {
		if _, err := file.Write(buffer.Bytes()); err != nil {
			return err
		}
		return file.Sync()
	})
}

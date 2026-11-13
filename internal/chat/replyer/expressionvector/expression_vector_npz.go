package expressionvector

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// expressionVectorArray 是对 numpy 二维 float32 矩阵的最小子集表示，行优先存储
type expressionVectorArray struct {
	rows int
	cols int
	data []float32
}

func newExpressionVectorArray(rows, cols int, data []float32) (expressionVectorArray, error) {
	if rows < 0 || cols < 0 {
		return expressionVectorArray{}, fmt.Errorf("表达向量数组维度非法: rows=%d cols=%d", rows, cols)
	}
	if rows*cols != len(data) {
		return expressionVectorArray{}, fmt.Errorf("表达向量数组长度不匹配: rows=%d cols=%d len=%d", rows, cols, len(data))
	}
	return expressionVectorArray{rows: rows, cols: cols, data: data}, nil
}

func (a expressionVectorArray) row(index int) []float32 {
	if index < 0 || index >= a.rows {
		return nil
	}
	start := index * a.cols
	return a.data[start : start+a.cols]
}

// writeExpressionVectorNPZ 按 numpy savez 的格式写 npz，键名会自动补 .npy 后缀
func writeExpressionVectorNPZ(path string, arrays map[string]expressionVectorArray) error {
	if len(arrays) == 0 {
		return fmt.Errorf("表达向量索引没有可写入的数组")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".expression-vectors-*.npz.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	committed := false
	defer func() {
		_ = temp.Close()
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()

	writer := zip.NewWriter(temp)
	keys := make([]string, 0, len(arrays))
	for key := range arrays {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("表达向量索引数组键为空")
		}
		entry, err := writer.CreateHeader(&zip.FileHeader{Name: key + ".npy", Method: zip.Store})
		if err != nil {
			return err
		}
		if _, err := entry.Write(encodeExpressionVectorNPY(arrays[key])); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}

// readExpressionVectorNPZ 读取 npz 中的全部二维 float32 数组
func readExpressionVectorNPZ(path string) (map[string]expressionVectorArray, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	arrays := make(map[string]expressionVectorArray, len(reader.File))
	for _, file := range reader.File {
		if !strings.HasSuffix(file.Name, ".npy") {
			continue
		}
		key := strings.TrimSuffix(file.Name, ".npy")
		if strings.TrimSpace(key) == "" {
			continue
		}
		handle, err := file.Open()
		if err != nil {
			return nil, err
		}
		raw, readErr := io.ReadAll(handle)
		closeErr := handle.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		array, err := decodeExpressionVectorNPY(raw)
		if err != nil {
			return nil, fmt.Errorf("表达向量索引数组 %s 解析失败: %w", key, err)
		}
		arrays[key] = array
	}
	return arrays, nil
}

var (
	npyDescrPattern   = regexp.MustCompile(`'descr'\s*:\s*'([^']+)'`)
	npyFortranPattern = regexp.MustCompile(`'fortran_order'\s*:\s*(True|False)`)
	npyShapePattern   = regexp.MustCompile(`'shape'\s*:\s*\(([^)]*)\)`)
)

const expressionVectorNPYMagic = "\x93NUMPY"

func encodeExpressionVectorNPY(array expressionVectorArray) []byte {
	header := fmt.Sprintf("{'descr': '<f4', 'fortran_order': False, 'shape': (%d, %d), }", array.rows, array.cols)
	padding := (64 - ((10 + len(header) + 1) % 64)) % 64
	header += strings.Repeat(" ", padding) + "\n"
	var out bytes.Buffer
	out.WriteString(expressionVectorNPYMagic)
	out.WriteByte(1)
	out.WriteByte(0)
	_ = binary.Write(&out, binary.LittleEndian, uint16(len(header)))
	out.WriteString(header)
	for _, value := range array.data {
		_ = binary.Write(&out, binary.LittleEndian, math.Float32bits(value))
	}
	return out.Bytes()
}

func decodeExpressionVectorNPY(raw []byte) (expressionVectorArray, error) {
	if len(raw) < 10 || string(raw[:6]) != expressionVectorNPYMagic {
		return expressionVectorArray{}, fmt.Errorf("不是合法的 npy 文件")
	}
	major := int(raw[6])
	var headerLength int
	var headerStart int
	switch major {
	case 1:
		headerLength = int(binary.LittleEndian.Uint16(raw[8:10]))
		headerStart = 10
	case 2, 3:
		if len(raw) < 12 {
			return expressionVectorArray{}, fmt.Errorf("npy 头部长度不足")
		}
		headerLength = int(binary.LittleEndian.Uint32(raw[8:12]))
		headerStart = 12
	default:
		return expressionVectorArray{}, fmt.Errorf("不支持的 npy 版本: %d", major)
	}
	if headerLength <= 0 || headerLength > 1<<20 || headerStart+headerLength > len(raw) {
		return expressionVectorArray{}, fmt.Errorf("npy 头部长度异常: %d", headerLength)
	}
	header := string(raw[headerStart : headerStart+headerLength])
	descrMatch := npyDescrPattern.FindStringSubmatch(header)
	if len(descrMatch) != 2 {
		return expressionVectorArray{}, fmt.Errorf("npy 缺少 descr")
	}
	fortranMatch := npyFortranPattern.FindStringSubmatch(header)
	if len(fortranMatch) == 2 && fortranMatch[1] == "True" {
		return expressionVectorArray{}, fmt.Errorf("不支持 Fortran 顺序的 npy 数组")
	}
	shapeMatch := npyShapePattern.FindStringSubmatch(header)
	if len(shapeMatch) != 2 {
		return expressionVectorArray{}, fmt.Errorf("npy 缺少 shape")
	}
	shapeParts := strings.Split(shapeMatch[1], ",")
	dimensions := make([]int, 0, 2)
	for _, part := range shapeParts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return expressionVectorArray{}, fmt.Errorf("npy shape 非法: %q", shapeMatch[1])
		}
		dimensions = append(dimensions, value)
	}
	if len(dimensions) != 2 {
		return expressionVectorArray{}, fmt.Errorf("表达向量数组必须是二维: %v", dimensions)
	}
	rows, cols := dimensions[0], dimensions[1]
	dataOffset := headerStart + headerLength
	elementSize, decode, err := expressionVectorNPYDecoder(descrMatch[1])
	if err != nil {
		return expressionVectorArray{}, err
	}
	if rows > 0 && cols > 1<<30 || rows > 1<<30 {
		return expressionVectorArray{}, fmt.Errorf("npy 数组过大: %dx%d", rows, cols)
	}
	if rows*cols*elementSize != len(raw)-dataOffset {
		return expressionVectorArray{}, fmt.Errorf("npy 数据长度不匹配: shape=%dx%d data=%d", rows, cols, len(raw)-dataOffset)
	}
	data := make([]float32, rows*cols)
	for index := 0; index < rows*cols; index++ {
		data[index] = decode(raw[dataOffset+index*elementSize:])
	}
	return expressionVectorArray{rows: rows, cols: cols, data: data}, nil
}

func expressionVectorNPYDecoder(descr string) (int, func([]byte) float32, error) {
	order := byte('<')
	if len(descr) > 0 && (descr[0] == '<' || descr[0] == '>' || descr[0] == '|') {
		order = descr[0]
		descr = descr[1:]
	}
	if order == '>' {
		return 0, nil, fmt.Errorf("不支持大端 npy 数组")
	}
	switch descr {
	case "f4":
		return 4, func(raw []byte) float32 {
			return math.Float32frombits(binary.LittleEndian.Uint32(raw))
		}, nil
	case "f8":
		return 8, func(raw []byte) float32 {
			return float32(math.Float64frombits(binary.LittleEndian.Uint64(raw)))
		}, nil
	default:
		return 0, nil, fmt.Errorf("不支持的 npy dtype: %s", descr)
	}
}

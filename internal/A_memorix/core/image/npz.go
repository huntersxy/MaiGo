package imagememory

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// imageVectorsNPZ 表示从记忆包中解析出的图片向量数组
type imageVectorsNPZ struct {
	IDs            []string
	Vectors        [][]float32
	Rows           int
	Cols           int
	TwoDimensional bool
}

var (
	npyDescrPattern   = regexp.MustCompile(`'descr'\s*:\s*'([^']*)'`)
	npyFortranPattern = regexp.MustCompile(`'fortran_order'\s*:\s*(True|False)`)
	npyShapePattern   = regexp.MustCompile(`'shape'\s*:\s*\(([^)]*)\)`)
)

// encodeImageVectorsNPZ 生成与 np.savez_compressed(ids=..., vectors=...) 兼容的压缩包
func encodeImageVectorsNPZ(ids []string, vectors [][]float32) ([]byte, error) {
	idsPayload := encodeUnicodeNPY(ids)
	vectorsPayload, err := encodeFloatNPY(vectors)
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	idsEntry, err := writer.Create("ids.npy")
	if err != nil {
		return nil, err
	}
	if _, err := idsEntry.Write(idsPayload); err != nil {
		return nil, err
	}
	vectorsEntry, err := writer.Create("vectors.npy")
	if err != nil {
		return nil, err
	}
	if _, err := vectorsEntry.Write(vectorsPayload); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// decodeImageVectorsNPZ 解析记忆包图片向量
func decodeImageVectorsNPZ(payload []byte) (*imageVectorsNPZ, error) {
	reader, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		return nil, err
	}
	result := &imageVectorsNPZ{}
	foundIDs := false
	foundVectors := false
	for _, file := range reader.File {
		name := strings.TrimSuffix(file.Name, ".npy")
		switch name {
		case "ids":
			data, readErr := readZipEntry(file)
			if readErr != nil {
				return nil, readErr
			}
			ids, decodeErr := decodeUnicodeNPY(data)
			if decodeErr != nil {
				return nil, decodeErr
			}
			result.IDs = ids
			foundIDs = true
		case "vectors":
			data, readErr := readZipEntry(file)
			if readErr != nil {
				return nil, readErr
			}
			decoded, decodeErr := decodeFloatNPY(data)
			if decodeErr != nil {
				return nil, decodeErr
			}
			result.Vectors = decoded.vectors
			result.Rows = decoded.rows
			result.Cols = decoded.cols
			result.TwoDimensional = decoded.twoDimensional
			foundVectors = true
		}
	}
	if !foundIDs || !foundVectors {
		return nil, fmt.Errorf("记忆包图片向量缺少 ids 或 vectors 数组")
	}
	return result, nil
}

// readZipEntry 读取压缩包成员
func readZipEntry(file *zip.File) ([]byte, error) {
	handle, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	return io.ReadAll(handle)
}

// encodeUnicodeNPY 生成 NumPy unicode 一维数组
func encodeUnicodeNPY(values []string) []byte {
	maxRunes := 0
	for _, value := range values {
		if count := utf8.RuneCountInString(value); count > maxRunes {
			maxRunes = count
		}
	}
	if maxRunes == 0 {
		maxRunes = 1
	}
	payload := make([]byte, len(values)*maxRunes*4)
	offset := 0
	for _, value := range values {
		symbols := []rune(value)
		for position := 0; position < maxRunes; position++ {
			var symbol rune
			if position < len(symbols) {
				symbol = symbols[position]
			}
			binary.LittleEndian.PutUint32(payload[offset:offset+4], uint32(symbol))
			offset += 4
		}
	}
	header := buildNPYHeader("<U"+strconv.Itoa(maxRunes), []int{len(values)})
	return append(header, payload...)
}

// encodeFloatNPY 生成 NumPy float32 二维数组
func encodeFloatNPY(vectors [][]float32) ([]byte, error) {
	rows := len(vectors)
	columns := 0
	if rows > 0 {
		columns = len(vectors[0])
	}
	for _, vector := range vectors {
		if len(vector) != columns {
			return nil, fmt.Errorf("向量维度不一致")
		}
	}
	payload := make([]byte, rows*columns*4)
	offset := 0
	for _, vector := range vectors {
		for _, value := range vector {
			binary.LittleEndian.PutUint32(payload[offset:offset+4], math.Float32bits(value))
			offset += 4
		}
	}
	header := buildNPYHeader("<f4", []int{rows, columns})
	return append(header, payload...), nil
}

// buildNPYHeader 生成 NumPy v1 头，按 64 字节对齐
func buildNPYHeader(descr string, shape []int) []byte {
	shapeParts := make([]string, len(shape))
	for index, dimension := range shape {
		shapeParts[index] = strconv.Itoa(dimension)
	}
	shapeText := "()"
	if len(shape) == 1 {
		shapeText = "(" + shapeParts[0] + ",)"
	} else if len(shape) > 1 {
		shapeText = "(" + strings.Join(shapeParts, ", ") + ")"
	}
	header := "{'descr': '" + descr + "', 'fortran_order': False, 'shape': " + shapeText + ", }"
	total := 10 + len(header) + 1
	padding := (64 - total%64) % 64
	header += strings.Repeat(" ", padding) + "\n"
	magic := []byte{0x93, 'N', 'U', 'M', 'P', 'Y', 1, 0}
	length := make([]byte, 2)
	binary.LittleEndian.PutUint16(length, uint16(len(header)))
	payload := append(magic, length...)
	return append(payload, []byte(header)...)
}

// decodeUnicodeNPY 解析 NumPy unicode 一维数组
func decodeUnicodeNPY(data []byte) ([]string, error) {
	descr, _, shape, raw, err := parseNPYParts(data)
	if err != nil {
		return nil, err
	}
	if len(shape) != 1 {
		return nil, fmt.Errorf("ids 数组必须是一维")
	}
	maxRunes, err := parseUnicodeDescr(descr)
	if err != nil {
		return nil, err
	}
	count := shape[0]
	itemSize := maxRunes * 4
	if itemSize <= 0 || count < 0 || len(raw) < count*itemSize {
		return nil, fmt.Errorf("ids 数组数据长度不足")
	}
	order := npyByteOrder(descr)
	values := make([]string, count)
	for index := 0; index < count; index++ {
		base := index * itemSize
		length := maxRunes
		for length > 0 && order.Uint32(raw[base+(length-1)*4:base+length*4]) == 0 {
			length--
		}
		symbols := make([]rune, length)
		for position := 0; position < length; position++ {
			symbols[position] = rune(order.Uint32(raw[base+position*4 : base+position*4+4]))
		}
		values[index] = string(symbols)
	}
	return values, nil
}

// parsedFloatNPY 表示解析后的浮点数组
type parsedFloatNPY struct {
	vectors        [][]float32
	rows           int
	cols           int
	twoDimensional bool
}

// decodeFloatNPY 解析 NumPy 浮点一维或二维数组
func decodeFloatNPY(data []byte) (parsedFloatNPY, error) {
	descr, fortran, shape, raw, err := parseNPYParts(data)
	if err != nil {
		return parsedFloatNPY{}, err
	}
	elemSize := 0
	switch {
	case strings.HasSuffix(descr, "f4"):
		elemSize = 4
	case strings.HasSuffix(descr, "f8"):
		elemSize = 8
	default:
		return parsedFloatNPY{}, fmt.Errorf("不支持的向量数据类型: %s", descr)
	}
	result := parsedFloatNPY{}
	switch len(shape) {
	case 0:
		result.rows = 1
		result.cols = 0
		return result, nil
	case 1:
		result.rows = shape[0]
		result.cols = 0
		return result, nil
	case 2:
		result.rows = shape[0]
		result.cols = shape[1]
		result.twoDimensional = true
	default:
		result.rows = shape[0]
		result.cols = shape[1]
		return result, nil
	}
	rows, cols := result.rows, result.cols
	if rows < 0 || cols < 0 || len(raw) < rows*cols*elemSize {
		return parsedFloatNPY{}, fmt.Errorf("向量数组数据长度不足")
	}
	order := npyByteOrder(descr)
	vectors := make([][]float32, rows)
	for row := 0; row < rows; row++ {
		vector := make([]float32, cols)
		for column := 0; column < cols; column++ {
			source := row*cols + column
			if fortran {
				source = column*rows + row
			}
			offset := source * elemSize
			if elemSize == 4 {
				vector[column] = math.Float32frombits(order.Uint32(raw[offset : offset+4]))
			} else {
				vector[column] = float32(math.Float64frombits(order.Uint64(raw[offset : offset+8])))
			}
		}
		vectors[row] = vector
	}
	result.vectors = vectors
	return result, nil
}

// parseNPYParts 解析 npy 魔数、头部与数据段
func parseNPYParts(data []byte) (string, bool, []int, []byte, error) {
	magic := []byte{0x93, 'N', 'U', 'M', 'P', 'Y'}
	if len(data) < 10 || !bytes.Equal(data[:6], magic) {
		return "", false, nil, nil, fmt.Errorf("不是有效的 NumPy 数组")
	}
	start := 0
	headerLength := 0
	switch data[6] {
	case 1:
		headerLength = int(binary.LittleEndian.Uint16(data[8:10]))
		start = 10
	case 2, 3:
		if len(data) < 12 {
			return "", false, nil, nil, fmt.Errorf("NumPy 头部长度不足")
		}
		headerLength = int(binary.LittleEndian.Uint32(data[8:12]))
		start = 12
	default:
		return "", false, nil, nil, fmt.Errorf("不支持的 NumPy 版本: %d", data[6])
	}
	if headerLength < 0 || start+headerLength > len(data) {
		return "", false, nil, nil, fmt.Errorf("NumPy 头部长度非法")
	}
	header := string(data[start : start+headerLength])
	descrMatch := npyDescrPattern.FindStringSubmatch(header)
	if len(descrMatch) < 2 {
		return "", false, nil, nil, fmt.Errorf("NumPy 头部缺少 descr")
	}
	fortranMatch := npyFortranPattern.FindStringSubmatch(header)
	fortran := len(fortranMatch) >= 2 && fortranMatch[1] == "True"
	shapeMatch := npyShapePattern.FindStringSubmatch(header)
	if len(shapeMatch) < 2 {
		return "", false, nil, nil, fmt.Errorf("NumPy 头部缺少 shape")
	}
	shape := []int{}
	for _, part := range strings.Split(shapeMatch[1], ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		dimension, err := strconv.Atoi(part)
		if err != nil {
			return "", false, nil, nil, fmt.Errorf("NumPy shape 非法: %s", part)
		}
		shape = append(shape, dimension)
	}
	return descrMatch[1], fortran, shape, data[start+headerLength:], nil
}

// parseUnicodeDescr 解析 <U12 形式的 dtype
func parseUnicodeDescr(descr string) (int, error) {
	text := descr
	if len(text) > 0 && strings.ContainsRune("<>|=", rune(text[0])) {
		text = text[1:]
	}
	if !strings.HasPrefix(text, "U") {
		return 0, fmt.Errorf("不支持的 ID 数据类型: %s", descr)
	}
	width, err := strconv.Atoi(text[1:])
	if err != nil || width <= 0 {
		return 0, fmt.Errorf("不支持的 ID 数据类型: %s", descr)
	}
	return width, nil
}

// npyByteOrder 返回 dtype 对应的字节序
func npyByteOrder(descr string) binary.ByteOrder {
	if strings.HasPrefix(descr, ">") {
		return binary.BigEndian
	}
	return binary.LittleEndian
}

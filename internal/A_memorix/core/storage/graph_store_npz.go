package storage

// A_memorix 邻接矩阵 npz 读写，兼容 scipy.sparse.save_npz/load_npz（npy v1/v2/v3 头）
// 仅支持 scipy 图邻接矩阵会出现的数值与字节数组，拒绝大端、object/pickle 与 fortran 顺序

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// graphNpyArray 解码后的 npy 数组，data 为去掉头部后的原始字节
type graphNpyArray struct {
	descr string
	shape []int
	data  []byte
}

// npyDtype 解析后的 dtype 描述
type npyDtype struct {
	kind byte
	size int
}

// writeAdjacencyNPZ 以 scipy 兼容格式写出邻接矩阵 npz，format 决定 CSR/CSC 布局
func writeAdjacencyNPZ(path string, matrix *sparseMatrix, format SparseMatrixFormat) error {
	view := matrix.csrView()
	if normalizeMatrixFormat(format) == SparseMatrixFormatCSC {
		view = matrix.cscView()
	}
	formatBytes := []byte(string(view.Format))

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	parts := []struct {
		name  string
		descr string
		shape []int
		data  []byte
	}{
		{name: "format", descr: fmt.Sprintf("|S%d", len(formatBytes)), shape: []int{}, data: formatBytes},
		{name: "shape", descr: "<i8", shape: []int{2}, data: encodeInt64LE([]int64{int64(view.Size), int64(view.Size)})},
		{name: "indices", descr: "<i8", shape: []int{len(view.Indices)}, data: encodeInt64LE(int32ToInt64(view.Indices))},
		{name: "indptr", descr: "<i8", shape: []int{len(view.Indptr)}, data: encodeInt64LE(int32ToInt64(view.Indptr))},
		{name: "data", descr: "<f4", shape: []int{len(view.Data)}, data: encodeFloat32LE(view.Data)},
	}
	for _, part := range parts {
		header := &zip.FileHeader{Name: part.name + ".npy", Method: zip.Deflate}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			_ = writer.Close()
			return fmt.Errorf("写入图邻接快照失败: %w", err)
		}
		if _, err := entry.Write(encodeNPY(part.descr, part.shape, part.data)); err != nil {
			_ = writer.Close()
			return fmt.Errorf("写入图邻接快照失败: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("写入图邻接快照失败: %w", err)
	}
	return atomicWriteFile(path, buffer.Bytes())
}

// readAdjacencyNPZ 读取 scipy 兼容的邻接矩阵 npz，格式不支持时显式报错
func readAdjacencyNPZ(path string) (*sparseMatrix, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("读取图邻接快照失败: %w", err)
	}
	defer func() { _ = reader.Close() }()

	arrays := make(map[string]*graphNpyArray, len(reader.File))
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		name := strings.TrimSuffix(file.Name, ".npy")
		handle, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("读取图邻接快照失败: %w", err)
		}
		raw, err := io.ReadAll(handle)
		_ = handle.Close()
		if err != nil {
			return nil, fmt.Errorf("读取图邻接快照失败: %w", err)
		}
		array, err := decodeNPY(raw)
		if err != nil {
			return nil, fmt.Errorf("读取图邻接快照 %s 失败: %w", name, err)
		}
		arrays[name] = array
	}

	for _, required := range []string{"format", "shape", "data"} {
		if _, ok := arrays[required]; !ok {
			return nil, fmt.Errorf("图邻接快照缺少 %s 数组", required)
		}
	}
	shape, err := arrays["shape"].int64Values()
	if err != nil {
		return nil, err
	}
	if len(shape) != 2 {
		return nil, fmt.Errorf("图邻接矩阵维度必须为二维: %v", shape)
	}
	rows := int(shape[0])
	cols := int(shape[1])
	if rows < 0 || cols < 0 {
		return nil, fmt.Errorf("图邻接矩阵维度非法: %v", shape)
	}
	if rows != cols {
		return nil, fmt.Errorf("图邻接矩阵必须为方阵: %d x %d", rows, cols)
	}

	formatText, err := arrays["format"].byteString()
	if err != nil {
		return nil, err
	}
	format := strings.ToLower(strings.TrimSpace(formatText))
	data, err := arrays["data"].float32Values()
	if err != nil {
		return nil, err
	}

	switch format {
	case "csr":
		return buildCSRFromNPZ(arrays, data, rows)
	case "csc":
		return buildCSCFromNPZ(arrays, data, rows)
	case "coo":
		return buildCOOFromNPZ(arrays, data, rows)
	default:
		return nil, fmt.Errorf("不支持的图邻接矩阵格式: %s", formatText)
	}
}

// buildCSRFromNPZ 由 CSR 数组构建邻接矩阵
func buildCSRFromNPZ(arrays map[string]*graphNpyArray, data []float32, size int) (*sparseMatrix, error) {
	indices, indptr, nnz, err := readIndicesAndIndptr(arrays, data, size)
	if err != nil {
		return nil, err
	}
	matrix := newSparseMatrix(size)
	for row := 0; row < size; row++ {
		start := int(indptr[row])
		end := int(indptr[row+1])
		if start < 0 || end < start || end > nnz {
			return nil, fmt.Errorf("图邻接矩阵 CSR indptr 非法: 行 %d [%d, %d)", row, start, end)
		}
		for pos := start; pos < end; pos++ {
			col := int(indices[pos])
			if col < 0 || col >= size {
				return nil, fmt.Errorf("图邻接矩阵 CSR 列索引越界: %d", col)
			}
			matrix.setRaw(row, col, matrix.get(row, col)+data[pos])
		}
	}
	return matrix, nil
}

// buildCSCFromNPZ 由 CSC 数组构建邻接矩阵
func buildCSCFromNPZ(arrays map[string]*graphNpyArray, data []float32, size int) (*sparseMatrix, error) {
	indices, indptr, nnz, err := readIndicesAndIndptr(arrays, data, size)
	if err != nil {
		return nil, err
	}
	matrix := newSparseMatrix(size)
	for col := 0; col < size; col++ {
		start := int(indptr[col])
		end := int(indptr[col+1])
		if start < 0 || end < start || end > nnz {
			return nil, fmt.Errorf("图邻接矩阵 CSC indptr 非法: 列 %d [%d, %d)", col, start, end)
		}
		for pos := start; pos < end; pos++ {
			row := int(indices[pos])
			if row < 0 || row >= size {
				return nil, fmt.Errorf("图邻接矩阵 CSC 行索引越界: %d", row)
			}
			matrix.setRaw(row, col, matrix.get(row, col)+data[pos])
		}
	}
	return matrix, nil
}

// buildCOOFromNPZ 由 COO 数组构建邻接矩阵
func buildCOOFromNPZ(arrays map[string]*graphNpyArray, data []float32, size int) (*sparseMatrix, error) {
	rowArray, ok := arrays["row"]
	if !ok {
		return nil, fmt.Errorf("图邻接快照缺少 row 数组")
	}
	colArray, ok := arrays["col"]
	if !ok {
		return nil, fmt.Errorf("图邻接快照缺少 col 数组")
	}
	rows, err := rowArray.int64Values()
	if err != nil {
		return nil, err
	}
	cols, err := colArray.int64Values()
	if err != nil {
		return nil, err
	}
	if len(rows) != len(cols) || len(rows) != len(data) {
		return nil, fmt.Errorf("图邻接矩阵 COO 数组长度不一致: %d, %d, %d", len(rows), len(cols), len(data))
	}
	matrix := newSparseMatrix(size)
	for idx := range data {
		row := int(rows[idx])
		col := int(cols[idx])
		if row < 0 || row >= size || col < 0 || col >= size {
			return nil, fmt.Errorf("图邻接矩阵 COO 索引越界: %d, %d", row, col)
		}
		matrix.setRaw(row, col, matrix.get(row, col)+data[idx])
	}
	return matrix, nil
}

// readIndicesAndIndptr 校验并读取 indices/indptr
func readIndicesAndIndptr(arrays map[string]*graphNpyArray, data []float32, size int) ([]int64, []int64, int, error) {
	for _, required := range []string{"indices", "indptr"} {
		if _, ok := arrays[required]; !ok {
			return nil, nil, 0, fmt.Errorf("图邻接快照缺少 %s 数组", required)
		}
	}
	indices, err := arrays["indices"].int64Values()
	if err != nil {
		return nil, nil, 0, err
	}
	indptr, err := arrays["indptr"].int64Values()
	if err != nil {
		return nil, nil, 0, err
	}
	if len(indptr) != size+1 {
		return nil, nil, 0, fmt.Errorf("图邻接矩阵 indptr 长度非法: %d, 期望 %d", len(indptr), size+1)
	}
	if int(indptr[0]) != 0 {
		return nil, nil, 0, fmt.Errorf("图邻接矩阵 indptr 必须从 0 开始")
	}
	nnz := int(indptr[size])
	if nnz < 0 || nnz > len(indices) || nnz > len(data) {
		return nil, nil, 0, fmt.Errorf("图邻接矩阵非零元素数量非法: %d", nnz)
	}
	return indices, indptr, nnz, nil
}

// encodeNPY 构造 npy v1.0 字节流，头部按 numpy 规则补齐到 64 字节倍数
func encodeNPY(descr string, shape []int, data []byte) []byte {
	var shapeText string
	switch len(shape) {
	case 0:
		shapeText = "()"
	case 1:
		shapeText = fmt.Sprintf("(%d,)", shape[0])
	default:
		parts := make([]string, len(shape))
		for idx, dim := range shape {
			parts[idx] = strconv.Itoa(dim)
		}
		shapeText = "(" + strings.Join(parts, ", ") + ")"
	}
	header := fmt.Sprintf("{'descr': '%s', 'fortran_order': False, 'shape': %s, }", descr, shapeText)
	total := 10 + len(header) + 1
	if padding := (64 - total%64) % 64; padding > 0 {
		header += strings.Repeat(" ", padding)
	}
	header += "\n"

	var buffer bytes.Buffer
	buffer.Write([]byte{0x93, 'N', 'U', 'M', 'P', 'Y', 1, 0})
	var length [2]byte
	binary.LittleEndian.PutUint16(length[:], uint16(len(header)))
	buffer.Write(length[:])
	buffer.WriteString(header)
	buffer.Write(data)
	return buffer.Bytes()
}

// decodeNPY 解析 npy v1/v2/v3 字节流并校验数据长度
func decodeNPY(raw []byte) (*graphNpyArray, error) {
	if len(raw) < 10 || raw[0] != 0x93 || string(raw[1:6]) != "NUMPY" {
		return nil, fmt.Errorf("不是合法的 npy 文件")
	}
	major := raw[6]
	var headerLength int
	var offset int
	switch major {
	case 1, 2:
		headerLength = int(binary.LittleEndian.Uint16(raw[8:10]))
		offset = 10
	case 3:
		if len(raw) < 12 {
			return nil, fmt.Errorf("npy 头部长度不足")
		}
		headerLength = int(binary.LittleEndian.Uint32(raw[8:12]))
		offset = 12
	default:
		return nil, fmt.Errorf("不支持的 npy 版本: %d", major)
	}
	if headerLength < 0 || offset+headerLength > len(raw) {
		return nil, fmt.Errorf("npy 头部长度非法: %d", headerLength)
	}
	descr, fortran, shape, err := parseNPYHeader(string(raw[offset : offset+headerLength]))
	if err != nil {
		return nil, err
	}
	if fortran {
		return nil, fmt.Errorf("不支持的 npy fortran_order=True")
	}
	dtype, err := parseNPYDtype(descr)
	if err != nil {
		return nil, err
	}
	count := shapeProduct(shape)
	if count < 0 {
		return nil, fmt.Errorf("npy 数组维度非法: %v", shape)
	}
	expected := count * dtype.size
	body := raw[offset+headerLength:]
	if expected > len(body) {
		return nil, fmt.Errorf("npy 数据长度不足: 需要 %d, 实际 %d", expected, len(body))
	}
	return &graphNpyArray{descr: descr, shape: shape, data: body[:expected]}, nil
}

// parseNPYHeader 解析 numpy 头部字典中的 descr、fortran_order 与 shape
func parseNPYHeader(header string) (string, bool, []int, error) {
	descr, ok := npyFieldValue(header, "'descr'")
	if !ok {
		return "", false, nil, fmt.Errorf("npy 头部缺少 descr 字段: %s", strings.TrimSpace(header))
	}
	fortranText, ok := npyFieldValue(header, "'fortran_order'")
	if !ok {
		return "", false, nil, fmt.Errorf("npy 头部缺少 fortran_order 字段: %s", strings.TrimSpace(header))
	}
	shapeText, ok := npyFieldValue(header, "'shape'")
	if !ok || len(shapeText) < 2 || shapeText[0] != '(' || shapeText[len(shapeText)-1] != ')' {
		return "", false, nil, fmt.Errorf("npy 头部 shape 字段非法: %s", shapeText)
	}
	shape := make([]int, 0)
	for _, part := range strings.Split(shapeText[1:len(shapeText)-1], ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		dim, err := strconv.Atoi(part)
		if err != nil || dim < 0 {
			return "", false, nil, fmt.Errorf("npy 数组维度非法: %s", part)
		}
		shape = append(shape, dim)
	}
	return descr, strings.TrimSpace(fortranText) == "True", shape, nil
}

// npyFieldValue 从 numpy 头部字典中取出指定键的值
func npyFieldValue(header string, key string) (string, bool) {
	pos := strings.Index(header, key)
	if pos < 0 {
		return "", false
	}
	rest := header[pos+len(key):]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return "", false
	}
	rest = strings.TrimSpace(rest[colon+1:])
	if rest == "" {
		return "", false
	}
	if rest[0] == '\'' {
		end := strings.IndexByte(rest[1:], '\'')
		if end < 0 {
			return "", false
		}
		return rest[1 : 1+end], true
	}
	if rest[0] == '(' {
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			return "", false
		}
		return rest[:end+1], true
	}
	end := strings.IndexAny(rest, ",}")
	if end < 0 {
		end = len(rest)
	}
	return strings.TrimSpace(rest[:end]), true
}

// parseNPYDtype 解析 dtype 描述，拒绝大端与 object/pickle
func parseNPYDtype(descr string) (npyDtype, error) {
	if descr == "" {
		return npyDtype{}, fmt.Errorf("npy dtype 为空")
	}
	order := descr[0]
	if order == '>' {
		return npyDtype{}, fmt.Errorf("不支持的 npy 大端 dtype: %s", descr)
	}
	rest := descr[1:]
	if rest == "" {
		return npyDtype{}, fmt.Errorf("不支持的 npy dtype: %s", descr)
	}
	kind := rest[0]
	sizeText := rest[1:]
	switch kind {
	case 'f', 'i', 'u', 'S', 'U':
	default:
		return npyDtype{}, fmt.Errorf("不支持的 npy dtype: %s", descr)
	}
	size, err := strconv.Atoi(sizeText)
	if err != nil || size <= 0 {
		return npyDtype{}, fmt.Errorf("不支持的 npy dtype: %s", descr)
	}
	if kind == 'U' {
		size *= 4
	}
	return npyDtype{kind: kind, size: size}, nil
}

// shapeProduct 计算数组元素个数
func shapeProduct(shape []int) int {
	total := 1
	for _, dim := range shape {
		if dim < 0 || (dim != 0 && total > math.MaxInt/dim) {
			return -1
		}
		total *= dim
	}
	return total
}

// int64Values 将整数数组转换为 int64 切片
func (a *graphNpyArray) int64Values() ([]int64, error) {
	dtype, err := parseNPYDtype(a.descr)
	if err != nil {
		return nil, err
	}
	if dtype.kind != 'i' && dtype.kind != 'u' {
		return nil, fmt.Errorf("npy 数组不是整数类型: %s", a.descr)
	}
	count := len(a.data) / dtype.size
	values := make([]int64, count)
	for idx := range values {
		values[idx] = readLittleEndianInt(a.data[idx*dtype.size:], dtype.size)
	}
	return values, nil
}

// float32Values 将浮点数组转换为 float32 切片
func (a *graphNpyArray) float32Values() ([]float32, error) {
	dtype, err := parseNPYDtype(a.descr)
	if err != nil {
		return nil, err
	}
	if dtype.kind != 'f' || (dtype.size != 4 && dtype.size != 8) {
		return nil, fmt.Errorf("npy 数组不是支持的浮点类型: %s", a.descr)
	}
	count := len(a.data) / dtype.size
	values := make([]float32, count)
	if dtype.size == 4 {
		for idx := range values {
			values[idx] = math.Float32frombits(binary.LittleEndian.Uint32(a.data[idx*4:]))
		}
		return values, nil
	}
	for idx := range values {
		values[idx] = float32(math.Float64frombits(binary.LittleEndian.Uint64(a.data[idx*8:])))
	}
	return values, nil
}

// byteString 将 |S 字节数组转换为字符串
func (a *graphNpyArray) byteString() (string, error) {
	dtype, err := parseNPYDtype(a.descr)
	if err != nil {
		return "", err
	}
	if dtype.kind != 'S' {
		return "", fmt.Errorf("npy 数组不是字节串类型: %s", a.descr)
	}
	return string(bytes.TrimRight(a.data, "\x00")), nil
}

// readLittleEndianInt 读取小端有符号/无符号整数
func readLittleEndianInt(raw []byte, size int) int64 {
	switch size {
	case 1:
		return int64(int8(raw[0]))
	case 2:
		return int64(int16(binary.LittleEndian.Uint16(raw)))
	case 4:
		return int64(int32(binary.LittleEndian.Uint32(raw)))
	case 8:
		return int64(binary.LittleEndian.Uint64(raw))
	default:
		return 0
	}
}

// encodeInt64LE 编码小端 int64 数组
func encodeInt64LE(values []int64) []byte {
	raw := make([]byte, len(values)*8)
	for idx, value := range values {
		binary.LittleEndian.PutUint64(raw[idx*8:], uint64(value))
	}
	return raw
}

// encodeFloat32LE 编码小端 float32 数组
func encodeFloat32LE(values []float32) []byte {
	raw := make([]byte, len(values)*4)
	for idx, value := range values {
		binary.LittleEndian.PutUint32(raw[idx*4:], math.Float32bits(value))
	}
	return raw
}

// int32ToInt64 转换索引切片
func int32ToInt64(values []int32) []int64 {
	result := make([]int64, len(values))
	for idx, value := range values {
		result[idx] = int64(value)
	}
	return result
}

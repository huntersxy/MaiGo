package embedding

// NumPy npz 缓存文件读写，对齐 manager.py 中 save_cache/load_cache 的 .npz 格式
// Python 侧使用 numpy.savez_compressed（zip + 每个键一个 .npy 条目）
// 这里不引入 numpy 依赖，按 .npy 规范手写编解码

import (
	"archive/zip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

const (
	npyMagic       = "\x93NUMPY"
	npyVersionV1   = 1
	npyVersionV2   = 2
	npyHeaderAlign = 64
)

// writeNPZCompressed 将文本哈希到向量的映射写入压缩 npz 文件
// 键排序保证同一缓存的字节内容稳定，便于比对
func writeNPZCompressed(path string, entries map[string][]float32) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	writer := zip.NewWriter(file)
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		entry, err := writer.Create(key + ".npy")
		if err != nil {
			writer.Close()
			file.Close()
			return err
		}
		if _, err := entry.Write(encodeNPYFloat32(entries[key])); err != nil {
			writer.Close()
			file.Close()
			return err
		}
	}
	if err := writer.Close(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// encodeNPYFloat32 编码单个一维 float32 数组为 .npy 字节
// 头部为 Python repr 形式的字典，且 (10 + headerLen) 必须是 64 的倍数
func encodeNPYFloat32(vector []float32) []byte {
	header := fmt.Sprintf("{'descr': '<f4', 'fortran_order': False, 'shape': (%d,), }", len(vector))
	headerLengthWithNewline := 10 + len(header) + 1
	padding := (npyHeaderAlign - headerLengthWithNewline%npyHeaderAlign) % npyHeaderAlign
	header += strings.Repeat(" ", padding) + "\n"

	buffer := make([]byte, 10+len(header)+len(vector)*4)
	copy(buffer[0:6], npyMagic)
	buffer[6] = npyVersionV1
	buffer[7] = 0
	binary.LittleEndian.PutUint16(buffer[8:10], uint16(len(header)))
	copy(buffer[10:], header)
	offset := 10 + len(header)
	for _, value := range vector {
		binary.LittleEndian.PutUint32(buffer[offset:], math.Float32bits(value))
		offset += 4
	}
	return buffer
}

// readNPZCompressed 读取压缩 npz 文件，返回去掉 .npy 后缀的键到向量映射
func readNPZCompressed(path string) (map[string][]float32, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	entries := make(map[string][]float32, len(reader.File))
	for _, file := range reader.File {
		if file.FileInfo().IsDir() || !strings.HasSuffix(file.Name, ".npy") {
			continue
		}
		handle, err := file.Open()
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(handle)
		closeErr := handle.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		vector, err := decodeNPYFloat32(data)
		if err != nil {
			return nil, fmt.Errorf("解析缓存条目 %s 失败: %w", file.Name, err)
		}
		entries[strings.TrimSuffix(file.Name, ".npy")] = vector
	}
	return entries, nil
}

// decodeNPYFloat32 解析 .npy 字节为一维 float32 向量
func decodeNPYFloat32(data []byte) ([]float32, error) {
	if len(data) < 10 {
		return nil, errors.New("npy 数据长度不足")
	}
	if string(data[0:6]) != npyMagic {
		return nil, errors.New("npy 魔数不匹配")
	}
	version := data[6]
	headerStart := 10
	var headerLength int
	switch version {
	case npyVersionV1:
		headerLength = int(binary.LittleEndian.Uint16(data[8:10]))
	case npyVersionV2:
		if len(data) < 12 {
			return nil, errors.New("npy 数据长度不足")
		}
		headerLength = int(binary.LittleEndian.Uint32(data[8:12]))
		headerStart = 12
	default:
		return nil, fmt.Errorf("不支持的 npy 版本: %d.%d", data[6], data[7])
	}
	if headerStart+headerLength > len(data) {
		return nil, errors.New("npy 头部长度越界")
	}
	header := string(data[headerStart : headerStart+headerLength])
	descr, err := npyHeaderString(header, "descr")
	if err != nil {
		return nil, err
	}
	fortranOrder, err := npyHeaderBool(header, "fortran_order")
	if err != nil {
		return nil, err
	}
	shape, err := npyHeaderShape(header)
	if err != nil {
		return nil, err
	}
	if fortranOrder {
		return nil, errors.New("npy 不支持 fortran_order=True")
	}
	if len(shape) != 1 {
		return nil, fmt.Errorf("npy shape 非一维: %v", shape)
	}
	valueSize, isFloat32, err := npyDTypeLayout(descr)
	if err != nil {
		return nil, err
	}
	payload := data[headerStart+headerLength:]
	expected := shape[0] * valueSize
	if len(payload) < expected {
		return nil, fmt.Errorf("npy 数据长度不足: 期望 %d, 实际 %d", expected, len(payload))
	}
	vector := make([]float32, shape[0])
	for index := 0; index < shape[0]; index++ {
		offset := index * valueSize
		if isFloat32 {
			vector[index] = math.Float32frombits(binary.LittleEndian.Uint32(payload[offset:]))
			continue
		}
		vector[index] = float32(math.Float64frombits(binary.LittleEndian.Uint64(payload[offset:])))
	}
	return vector, nil
}

// npyHeaderString 从 npy 头部字典中取出字符串字段
func npyHeaderString(header, key string) (string, error) {
	rest, err := npyHeaderValue(header, key)
	if err != nil {
		return "", err
	}
	if len(rest) < 2 || rest[0] != '\'' {
		return "", fmt.Errorf("npy 头部 %s 格式非法", key)
	}
	rest = rest[1:]
	end := strings.Index(rest, "'")
	if end < 0 {
		return "", fmt.Errorf("npy 头部 %s 格式非法", key)
	}
	return rest[:end], nil
}

// npyHeaderBool 从 npy 头部字典中取出布尔字段
func npyHeaderBool(header, key string) (bool, error) {
	rest, err := npyHeaderValue(header, key)
	if err != nil {
		return false, err
	}
	if strings.HasPrefix(rest, "True") {
		return true, nil
	}
	if strings.HasPrefix(rest, "False") {
		return false, nil
	}
	return false, fmt.Errorf("npy 头部 %s 格式非法", key)
}

// npyHeaderShape 从 npy 头部字典中取出 shape 元组
func npyHeaderShape(header string) ([]int, error) {
	rest, err := npyHeaderValue(header, "shape")
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(rest, "(") {
		return nil, errors.New("npy 头部 shape 格式非法")
	}
	end := strings.Index(rest, ")")
	if end < 0 {
		return nil, errors.New("npy 头部 shape 格式非法")
	}
	inside := rest[1:end]
	shape := make([]int, 0, 2)
	for _, part := range strings.Split(inside, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		dimension, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("npy 头部 shape 格式非法: %s", part)
		}
		shape = append(shape, dimension)
	}
	return shape, nil
}

// npyHeaderValue 取出头部字典中某个键后的值文本
func npyHeaderValue(header, key string) (string, error) {
	marker := "'" + key + "':"
	index := strings.Index(header, marker)
	if index < 0 {
		return "", fmt.Errorf("npy 头部缺少 %s", key)
	}
	return strings.TrimSpace(header[index+len(marker):]), nil
}

// npyDTypeLayout 返回 dtype 单值字节数与是否为 float32
func npyDTypeLayout(descr string) (int, bool, error) {
	switch descr {
	case "<f4", "|f4":
		return 4, true, nil
	case "<f8":
		return 8, false, nil
	default:
		return 0, false, fmt.Errorf("不支持的 npy dtype: %s", descr)
	}
}

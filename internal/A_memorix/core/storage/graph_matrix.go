package storage

// A_memorix 纯 Go 稀疏邻接矩阵，对齐 scipy csr/csc 的存储语义
// 邻接表是唯一事实来源，CSR/CSC 视图按需构建，不依赖 scipy/gonum

import (
	"sort"
	"strings"
)

// matrixEntry 单条存储项
type matrixEntry struct {
	col    int32
	weight float32
}

// sparseMatrix 行主序稀疏邻接矩阵
// 与 scipy LIL 语义一致：写入 0 权重即删除该存储项
type sparseMatrix struct {
	size       int
	rows       [][]matrixEntry
	entryCount int
}

// newSparseMatrix 创建指定阶数的零矩阵
func newSparseMatrix(size int) *sparseMatrix {
	if size < 0 {
		size = 0
	}
	return &sparseMatrix{size: size, rows: make([][]matrixEntry, size)}
}

// growTo 扩容到指定阶数，新增行列保持为空
func (m *sparseMatrix) growTo(newSize int) {
	if newSize <= m.size {
		return
	}
	for len(m.rows) < newSize {
		m.rows = append(m.rows, nil)
	}
	m.size = newSize
}

// findEntry 返回行内插入位置与是否命中
func (m *sparseMatrix) findEntry(row int, col int32) (int, bool) {
	entries := m.rows[row]
	idx := sort.Search(len(entries), func(i int) bool { return entries[i].col >= col })
	if idx < len(entries) && entries[idx].col == col {
		return idx, true
	}
	return idx, false
}

// get 读取矩阵元素，缺失返回 0
func (m *sparseMatrix) get(row int, col int) float32 {
	if row < 0 || row >= m.size || col < 0 || col >= m.size {
		return 0
	}
	idx, ok := m.findEntry(row, int32(col))
	if !ok {
		return 0
	}
	return m.rows[row][idx].weight
}

// set 写入矩阵元素，0 权重删除存储项（对齐 scipy LIL 赋值 0 的行为）
func (m *sparseMatrix) set(row int, col int, weight float32) {
	if weight == 0 {
		m.remove(row, col)
		return
	}
	m.setRaw(row, col, weight)
}

// setRaw 写入存储项并保留显式 0，仅用于加载外部 npz
func (m *sparseMatrix) setRaw(row int, col int, weight float32) {
	if row < 0 || row >= m.size || col < 0 || col >= m.size {
		return
	}
	entries := m.rows[row]
	idx, ok := m.findEntry(row, int32(col))
	if ok {
		entries[idx].weight = weight
		return
	}
	entries = append(entries, matrixEntry{})
	copy(entries[idx+1:], entries[idx:])
	entries[idx] = matrixEntry{col: int32(col), weight: weight}
	m.rows[row] = entries
	m.entryCount++
}

// remove 删除存储项，返回是否命中
func (m *sparseMatrix) remove(row int, col int) bool {
	if row < 0 || row >= m.size || col < 0 || col >= m.size {
		return false
	}
	idx, ok := m.findEntry(row, int32(col))
	if !ok {
		return false
	}
	entries := m.rows[row]
	entries = append(entries[:idx], entries[idx+1:]...)
	m.rows[row] = entries
	m.entryCount--
	return true
}

// eliminateZeros 删除所有显式 0 存储项，对齐 scipy eliminate_zeros
func (m *sparseMatrix) eliminateZeros() {
	for r := range m.rows {
		entries := m.rows[r]
		kept := entries[:0]
		for _, e := range entries {
			if e.weight != 0 {
				kept = append(kept, e)
			}
		}
		m.entryCount -= len(entries) - len(kept)
		m.rows[r] = kept
	}
}

// numEdges 返回存储项数量（scipy 的 nnz）
func (m *sparseMatrix) numEdges() int {
	return m.entryCount
}

// forEach 按行主序（行内按列升序）遍历存储项
func (m *sparseMatrix) forEach(fn func(row int, col int, weight float32)) {
	for r, entries := range m.rows {
		for _, e := range entries {
			fn(r, int(e.col), e.weight)
		}
	}
}

// sumRow 计算行和，float32 累加顺序与 scipy CSR 行内顺序一致
func (m *sparseMatrix) sumRow(row int) float32 {
	var total float32
	for _, e := range m.rows[row] {
		total += e.weight
	}
	return total
}

// columnSums 计算列和，按行主序累加
func (m *sparseMatrix) columnSums() []float32 {
	sums := make([]float32, m.size)
	for _, entries := range m.rows {
		for _, e := range entries {
			sums[e.col] += e.weight
		}
	}
	return sums
}

// transpose 转置，返回新的行主序矩阵
func (m *sparseMatrix) transpose() *sparseMatrix {
	result := newSparseMatrix(m.size)
	counts := make([]int, m.size)
	for _, entries := range m.rows {
		for _, e := range entries {
			counts[e.col]++
		}
	}
	for col := 0; col < m.size; col++ {
		if counts[col] > 0 {
			result.rows[col] = make([]matrixEntry, 0, counts[col])
		}
	}
	for r, entries := range m.rows {
		for _, e := range entries {
			result.rows[e.col] = append(result.rows[e.col], matrixEntry{col: int32(r), weight: e.weight})
		}
	}
	result.entryCount = m.entryCount
	return result
}

// SparseMatrixView 稀疏矩阵的 CSR/CSC 只读视图
// 与 scipy 的 indptr/indices/data 布局一致，索引为存储项下标
type SparseMatrixView struct {
	Format  SparseMatrixFormat
	Size    int
	Indptr  []int32
	Indices []int32
	Data    []float32
}

// csrView 构建 CSR 视图（行主序，行内保持存储顺序）
func (m *sparseMatrix) csrView() SparseMatrixView {
	indptr := make([]int32, m.size+1)
	indices := make([]int32, 0, m.entryCount)
	data := make([]float32, 0, m.entryCount)
	for r := 0; r < m.size; r++ {
		indptr[r] = int32(len(indices))
		for _, e := range m.rows[r] {
			indices = append(indices, e.col)
			data = append(data, e.weight)
		}
	}
	indptr[m.size] = int32(len(indices))
	return SparseMatrixView{Format: SparseMatrixFormatCSR, Size: m.size, Indptr: indptr, Indices: indices, Data: data}
}

// cscView 构建 CSC 视图（列主序，列内按行升序）
func (m *sparseMatrix) cscView() SparseMatrixView {
	counts := make([]int32, m.size)
	for _, entries := range m.rows {
		for _, e := range entries {
			counts[e.col]++
		}
	}
	indptr := make([]int32, m.size+1)
	for col := 0; col < m.size; col++ {
		indptr[col+1] = indptr[col] + counts[col]
	}
	positions := make([]int32, m.size)
	copy(positions, indptr[:m.size])
	indices := make([]int32, m.entryCount)
	data := make([]float32, m.entryCount)
	for r, entries := range m.rows {
		for _, e := range entries {
			pos := positions[e.col]
			positions[e.col]++
			indices[pos] = int32(r)
			data[pos] = e.weight
		}
	}
	return SparseMatrixView{Format: SparseMatrixFormatCSC, Size: m.size, Indptr: indptr, Indices: indices, Data: data}
}

// view 按格式构建视图，未知格式回退为 CSR
func (m *sparseMatrix) view(format SparseMatrixFormat) SparseMatrixView {
	if format == SparseMatrixFormatCSC {
		return m.cscView()
	}
	return m.csrView()
}

// AdjacencyView 返回当前邻接矩阵的只读稀疏视图，矩阵为空时返回空视图
func (g *GraphStore) AdjacencyView(format SparseMatrixFormat) SparseMatrixView {
	if g.adjacency == nil {
		return SparseMatrixView{Format: normalizeMatrixFormat(format), Indptr: []int32{0}}
	}
	return g.adjacency.view(normalizeMatrixFormat(format))
}

// normalizeMatrixFormat 归一化矩阵格式，未知值回退为 CSR
func normalizeMatrixFormat(format SparseMatrixFormat) SparseMatrixFormat {
	if SparseMatrixFormat(strings.ToLower(string(format))) == SparseMatrixFormatCSC {
		return SparseMatrixFormatCSC
	}
	return SparseMatrixFormatCSR
}

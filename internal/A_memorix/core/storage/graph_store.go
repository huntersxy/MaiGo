package storage

// A_memorix 图存储，对齐 Python core/storage/graph_store.py 的节点/边增删与版本管理
// 邻接矩阵由 graph_matrix.go 的纯 Go 稀疏邻接表实现，不依赖 scipy/gonum

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

// SparseMatrixFormat 稀疏矩阵格式
type SparseMatrixFormat string

const (
	SparseMatrixFormatCSR SparseMatrixFormat = "csr"
	SparseMatrixFormatCSC SparseMatrixFormat = "csc"
)

// GraphModificationMode 图修改模式
type GraphModificationMode string

const (
	GraphModificationModeBatch       GraphModificationMode = "batch"
	GraphModificationModeIncremental GraphModificationMode = "incremental"
	GraphModificationModeReadOnly    GraphModificationMode = "read_only"
)

// 图算法默认参数，对齐 Python GraphStore 中的默认值
const (
	DefaultPageRankAlpha          = 0.85
	DefaultPageRankMaxIter        = 100
	DefaultPageRankTolerance      = 1e-6
	DefaultFindPathsMaxDepth      = 3
	DefaultFindPathsMaxPaths      = 5
	DefaultFindPathsMaxExpansions = 20000
	DefaultSynonymThreshold       = 0.85
	graphSchemaVersion            = 1
)

// Edge 有向边端点
type Edge struct {
	Source string
	Target string
}

// WeightedNeighbor 带权邻居
type WeightedNeighbor struct {
	Node   string
	Weight float64
}

// EdgeHashEntry 边关系哈希条目，Hashes 为只读副本
type EdgeHashEntry struct {
	Source string
	Target string
	Hashes map[string]struct{}
}

// RelationHashOperation 关系哈希修剪操作
type RelationHashOperation struct {
	Source string
	Target string
	Hash   string
}

// RelationTriple 关系三元组，顺序为 subject、predicate、object、hash
type RelationTriple struct {
	Subject   string
	Predicate string
	Object    string
	Hash      string
}

// edgeKey 规范化节点下标组成的边键
type edgeKey struct {
	src int32
	dst int32
}

// GraphStore 稀疏有向图存储
// 节点与边为单一写者模型，不额外加锁（对齐 Python 侧未实际使用的 asyncio.Lock）
type GraphStore struct {
	matrixFormat SparseMatrixFormat
	dataDir      string

	nodes     []string
	nodeToIdx map[string]int
	nodeAttrs map[string]map[string]any

	adjacency      *sparseMatrix
	adjacencyT     *sparseMatrix
	adjacencyDirty bool
	saliencyCache  map[string]float64

	totalNodesAdded   int64
	totalEdgesAdded   int64
	totalNodesDeleted int64
	totalEdgesDeleted int64

	modificationMode  GraphModificationMode
	batchUpdateDepth  int
	batchGraphChanged bool
	batchNodesChanged bool

	graphRevision int64
	nodeRevision  int64

	edgeHashMap   map[edgeKey]map[string]struct{}
	edgeHashOrder []edgeKey
}

// NewGraphStore 创建图存储，matrixFormat 非法时回退为 CSR
func NewGraphStore(matrixFormat SparseMatrixFormat, dataDir string) *GraphStore {
	return &GraphStore{
		matrixFormat:     normalizeMatrixFormat(matrixFormat),
		dataDir:          dataDir,
		nodes:            []string{},
		nodeToIdx:        map[string]int{},
		nodeAttrs:        map[string]map[string]any{},
		modificationMode: GraphModificationModeBatch,
		adjacencyDirty:   true,
		edgeHashMap:      map[edgeKey]map[string]struct{}{},
	}
}

// MatrixFormat 返回当前稀疏矩阵格式
func (g *GraphStore) MatrixFormat() SparseMatrixFormat { return g.matrixFormat }

// DataDir 返回构造函数传入的数据目录，Save 不会修改它
func (g *GraphStore) DataDir() string { return g.dataDir }

// ModificationMode 返回当前修改模式
func (g *GraphStore) ModificationMode() GraphModificationMode { return g.modificationMode }

// CanonicalizeNode 规范化节点名：非空时去首尾空白并转小写
func (g *GraphStore) CanonicalizeNode(node string) string { return g.canonicalize(node) }

func (g *GraphStore) canonicalize(node string) string {
	if node == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(node))
}

// recordGraphChange 记录图变更，批量作用域内延迟到提交时统一增加版本
func (g *GraphStore) recordGraphChange(nodesChanged bool) {
	if g.batchUpdateDepth > 0 {
		g.batchGraphChanged = true
		if nodesChanged {
			g.batchNodesChanged = true
		}
		return
	}
	g.graphRevision++
	if nodesChanged {
		g.nodeRevision++
	}
}

// commitBatchRevisions 在最外层批量作用域退出时提交版本
func (g *GraphStore) commitBatchRevisions() {
	// 对齐 Python：只有批量作用域内记录过变更才递增版本，空批量不产生新版本
	if g.batchGraphChanged {
		g.graphRevision++
	}
	if g.batchNodesChanged {
		g.nodeRevision++
	}
	g.batchGraphChanged = false
	g.batchNodesChanged = false
}

// BatchUpdate 在批量模式下执行修改，最外层退出时统一提交版本
func (g *GraphStore) BatchUpdate(fn func() error) (err error) {
	outermost := g.batchUpdateDepth == 0
	var previous GraphModificationMode
	if outermost {
		previous = g.modificationMode
		g.batchGraphChanged = false
		g.batchNodesChanged = false
		g.switchMode(GraphModificationModeIncremental)
	}
	g.batchUpdateDepth++
	defer func() {
		g.batchUpdateDepth--
		if outermost {
			g.switchMode(previous)
			g.commitBatchRevisions()
		}
	}()
	return fn()
}

// switchMode 切换修改模式
// Python 侧在此把 scipy 矩阵在 LIL 与 CSR/CSC 之间转换，本实现用统一邻接表，无需转换
func (g *GraphStore) switchMode(newMode GraphModificationMode) {
	if newMode == g.modificationMode {
		return
	}
	g.modificationMode = newMode
}

// AddNodes 批量添加节点，返回实际新增数量
func (g *GraphStore) AddNodes(nodes []string, attributes map[string]map[string]any) int {
	added := 0
	for _, node := range nodes {
		canon := g.canonicalize(node)
		if _, exists := g.nodeToIdx[canon]; exists {
			continue
		}
		idx := len(g.nodes)
		g.nodes = append(g.nodes, node)
		g.nodeToIdx[canon] = idx
		g.adjacencyDirty = true
		g.saliencyCache = nil
		if attrs, ok := attributes[node]; ok {
			g.nodeAttrs[canon] = attrs
		} else {
			g.nodeAttrs[canon] = map[string]any{}
		}
		added++
		g.totalNodesAdded++
	}
	if added > 0 {
		g.recordGraphChange(true)
		g.expandAdjacencyMatrix(added)
	}
	return added
}

// expandAdjacencyMatrix 按新增节点数扩展邻接矩阵
func (g *GraphStore) expandAdjacencyMatrix(added int) {
	if g.adjacency == nil {
		g.adjacency = newSparseMatrix(len(g.nodes))
		return
	}
	g.adjacency.growTo(g.adjacency.size + added)
}

// AddEdges 批量添加或更新边，返回处理的边数量
// weights 为 nil 时全部使用 1.0，relationHashes 为 nil 时跳过关系哈希
func (g *GraphStore) AddEdges(edges []Edge, weights []float64, relationHashes []string) (int, error) {
	if len(edges) == 0 {
		return 0, nil
	}
	if weights != nil && len(weights) != len(edges) {
		return 0, fmt.Errorf("边数量与权重数量不匹配: %d vs %d", len(edges), len(weights))
	}
	if relationHashes != nil && len(relationHashes) != len(edges) {
		return 0, fmt.Errorf("边数量与关系哈希数量不匹配: %d vs %d", len(edges), len(relationHashes))
	}

	missingSet := make(map[string]struct{}, len(edges))
	missing := make([]string, 0, len(edges))
	for _, edge := range edges {
		for _, node := range []string{edge.Source, edge.Target} {
			canon := g.canonicalize(node)
			if _, exists := g.nodeToIdx[canon]; exists {
				continue
			}
			if _, seen := missingSet[canon]; seen {
				continue
			}
			missingSet[canon] = struct{}{}
			missing = append(missing, node)
		}
	}
	if len(missing) > 0 {
		g.AddNodes(missing, nil)
	}

	if weights == nil {
		weights = make([]float64, len(edges))
		for i := range weights {
			weights[i] = 1.0
		}
	}
	values := make([]float32, len(edges))
	for i, edge := range edges {
		value := weights[i]
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > math.MaxFloat32 {
			return 0, fmt.Errorf("边权重必须为有限数值: %s -> %s = %s", edge.Source, edge.Target, formatPythonFloat(value))
		}
		values[i] = float32(value)
	}

	// 同一端点只保留最后一次出现的权重，按端点键升序应用（对齐 np.unique 的 last-wins 行为）
	total := len(g.nodes)
	keys := make([]int64, len(edges))
	keyToLast := make(map[int64]int, len(edges))
	for i, edge := range edges {
		srcIdx := g.nodeToIdx[g.canonicalize(edge.Source)]
		dstIdx := g.nodeToIdx[g.canonicalize(edge.Target)]
		key := int64(srcIdx)*int64(total) + int64(dstIdx)
		keys[i] = key
		keyToLast[key] = i
	}
	uniqueKeys := make([]int64, 0, len(keyToLast))
	for key := range keyToLast {
		uniqueKeys = append(uniqueKeys, key)
	}
	sort.Slice(uniqueKeys, func(i, j int) bool { return uniqueKeys[i] < uniqueKeys[j] })
	updates := make([]matrixEntry, 0, len(uniqueKeys))
	updatesRow := make([]int32, 0, len(uniqueKeys))
	for _, key := range uniqueKeys {
		last := keyToLast[key]
		updates = append(updates, matrixEntry{col: int32(key % int64(total)), weight: values[last]})
		updatesRow = append(updatesRow, int32(key/int64(total)))
	}

	if g.adjacency == nil {
		g.adjacency = newSparseMatrix(total)
	}
	if g.adjacency.size < total {
		g.adjacency.growTo(total)
	}

	graphChanged := false
	if g.modificationMode == GraphModificationModeIncremental {
		for i, entry := range updates {
			row := int(updatesRow[i])
			col := int(entry.col)
			if g.adjacency.get(row, col) != entry.weight {
				graphChanged = true
				g.adjacency.set(row, col, entry.weight)
			}
		}
	} else if g.adjacency.numEdges() == 0 {
		for i, entry := range updates {
			if entry.weight != 0 {
				graphChanged = true
				g.adjacency.setRaw(int(updatesRow[i]), int(entry.col), entry.weight)
			}
		}
	} else {
		changed := make([]bool, len(updates))
		anyChanged := false
		for i, entry := range updates {
			if g.adjacency.get(int(updatesRow[i]), int(entry.col)) != entry.weight {
				changed[i] = true
				anyChanged = true
			}
		}
		if anyChanged {
			graphChanged = true
			for i, entry := range updates {
				if changed[i] {
					g.adjacency.set(int(updatesRow[i]), int(entry.col), entry.weight)
				}
			}
			g.adjacency.eliminateZeros()
		}
	}

	g.totalEdgesAdded += int64(len(edges))
	if graphChanged {
		g.adjacencyDirty = true
		g.saliencyCache = nil
		g.recordGraphChange(false)
	}

	if relationHashes != nil {
		for i, edge := range edges {
			if relationHashes[i] == "" {
				continue
			}
			srcIdx := g.nodeToIdx[g.canonicalize(edge.Source)]
			dstIdx := g.nodeToIdx[g.canonicalize(edge.Target)]
			g.addEdgeHash(edgeKey{src: int32(srcIdx), dst: int32(dstIdx)}, relationHashes[i])
		}
	}
	return len(edges), nil
}

// UpdateEdgeWeight 按增量更新边权重并夹在 [minWeight, maxWeight]
// 端点不存在或当前权重为 0 且增量为非正时返回 0
func (g *GraphStore) UpdateEdgeWeight(source, target string, delta float64, minWeight float64, maxWeight float64) (float64, error) {
	if _, ok := g.nodeToIdx[g.canonicalize(source)]; !ok {
		zap.L().Warn("更新边权重失败：源节点不存在", zap.String("node", source))
		return 0, nil
	}
	if _, ok := g.nodeToIdx[g.canonicalize(target)]; !ok {
		zap.L().Warn("更新边权重失败：目标节点不存在", zap.String("node", target))
		return 0, nil
	}
	current := g.GetEdgeWeight(source, target)
	if current == 0.0 && delta <= 0 {
		return 0, nil
	}
	newWeight := pythonMax(minWeight, pythonMin(maxWeight, current+delta))
	err := g.BatchUpdate(func() error {
		_, addErr := g.AddEdges([]Edge{{Source: source, Target: target}}, []float64{newWeight}, nil)
		return addErr
	})
	return newWeight, err
}

// DeleteNodes 删除节点及其关联边，返回按输入命中的节点数量（重复计数）
func (g *GraphStore) DeleteNodes(nodes []string) int {
	if len(nodes) == 0 {
		return 0
	}
	existing := make([]int, 0, len(nodes))
	for _, node := range nodes {
		if idx, ok := g.nodeToIdx[g.canonicalize(node)]; ok {
			existing = append(existing, idx)
		}
	}
	if len(existing) == 0 {
		return 0
	}
	deleted := make([]bool, len(g.nodes))
	for _, idx := range existing {
		deleted[idx] = true
	}

	oldToNew := make([]int, len(g.nodes))
	for i := range oldToNew {
		oldToNew[i] = -1
	}
	newNodes := make([]string, 0, len(g.nodes)-len(existing))
	for idx, name := range g.nodes {
		if deleted[idx] {
			continue
		}
		oldToNew[idx] = len(newNodes)
		newNodes = append(newNodes, name)
	}

	oldAttrs := g.nodeAttrs
	newAttrs := make(map[string]map[string]any, len(newNodes))
	newIndex := make(map[string]int, len(newNodes))
	for idx, name := range newNodes {
		canon := g.canonicalize(name)
		newIndex[canon] = idx
		if attrs, ok := oldAttrs[canon]; ok {
			newAttrs[canon] = attrs
		}
	}

	var newAdjacency *sparseMatrix
	if g.adjacency != nil {
		newAdjacency = newSparseMatrix(len(newNodes))
		g.adjacency.forEach(func(row int, col int, weight float32) {
			if oldToNew[row] < 0 || oldToNew[col] < 0 {
				return
			}
			newAdjacency.setRaw(oldToNew[row], oldToNew[col], weight)
		})
	}

	newEdgeMap := make(map[edgeKey]map[string]struct{}, len(g.edgeHashMap))
	newOrder := make([]edgeKey, 0, len(g.edgeHashOrder))
	for _, key := range g.edgeHashOrder {
		hashes, ok := g.edgeHashMap[key]
		if !ok || len(hashes) == 0 {
			continue
		}
		if key.src < 0 || key.dst < 0 || int(key.src) >= len(g.nodes) || int(key.dst) >= len(g.nodes) {
			continue
		}
		if oldToNew[key.src] < 0 || oldToNew[key.dst] < 0 {
			continue
		}
		newKey := edgeKey{src: int32(oldToNew[key.src]), dst: int32(oldToNew[key.dst])}
		copied := make(map[string]struct{}, len(hashes))
		for hash := range hashes {
			copied[hash] = struct{}{}
		}
		newEdgeMap[newKey] = copied
		newOrder = append(newOrder, newKey)
	}

	g.nodes = newNodes
	g.nodeToIdx = newIndex
	g.nodeAttrs = newAttrs
	g.adjacency = newAdjacency
	g.adjacencyT = nil
	g.edgeHashMap = newEdgeMap
	g.edgeHashOrder = newOrder
	g.adjacencyDirty = true
	g.saliencyCache = nil
	g.recordGraphChange(true)
	g.totalNodesDeleted += int64(len(existing))
	return len(existing)
}

// RemoveNodes 删除节点的同义方法
func (g *GraphStore) RemoveNodes(nodes []string) int {
	return g.DeleteNodes(nodes)
}

// DeleteEdges 删除边，返回实际删除的存储项数量
func (g *GraphStore) DeleteEdges(edges []Edge) int {
	if len(edges) == 0 {
		return 0
	}
	toDelete := make(map[edgeKey]struct{}, len(edges))
	for _, edge := range edges {
		srcIdx, okSrc := g.nodeToIdx[g.canonicalize(edge.Source)]
		dstIdx, okDst := g.nodeToIdx[g.canonicalize(edge.Target)]
		if okSrc && okDst {
			toDelete[edgeKey{src: int32(srcIdx), dst: int32(dstIdx)}] = struct{}{}
		}
	}

	deleted := 0
	if g.adjacency != nil && len(toDelete) > 0 {
		oldAdjacency := g.adjacency
		newAdjacency := newSparseMatrix(oldAdjacency.size)
		for row := 0; row < oldAdjacency.size; row++ {
			for _, entry := range oldAdjacency.rows[row] {
				if _, hit := toDelete[edgeKey{src: int32(row), dst: entry.col}]; hit {
					deleted++
					continue
				}
				newAdjacency.setRaw(row, int(entry.col), entry.weight)
			}
		}
		g.adjacency = newAdjacency
	}
	for key := range toDelete {
		g.removeEdgeHash(key)
	}

	g.totalEdgesDeleted += int64(deleted)
	g.adjacencyDirty = true
	g.saliencyCache = nil
	if deleted > 0 {
		g.recordGraphChange(false)
	}
	return deleted
}

// RemoveEdges 删除边的同义方法
func (g *GraphStore) RemoveEdges(edges []Edge) int {
	return g.DeleteEdges(edges)
}

// GetNodes 返回节点名副本
func (g *GraphStore) GetNodes() []string {
	return append([]string{}, g.nodes...)
}

// HasNode 判断规范化节点名是否存在
func (g *GraphStore) HasNode(node string) bool {
	_, ok := g.nodeToIdx[g.canonicalize(node)]
	return ok
}

// FindNode 查找节点并返回原始名，ignoreCase 仅对齐 Python 签名，实际始终忽略大小写
func (g *GraphStore) FindNode(node string, ignoreCase bool) (string, bool) {
	_ = ignoreCase
	idx, ok := g.nodeToIdx[g.canonicalize(node)]
	if !ok {
		return "", false
	}
	return g.nodes[idx], true
}

// GetNodeAttributes 返回节点属性，不存在时返回 nil
func (g *GraphStore) GetNodeAttributes(node string) map[string]any {
	return g.nodeAttrs[g.canonicalize(node)]
}

// GetEdgeWeight 返回边权重，端点或邻接矩阵缺失时为 0
func (g *GraphStore) GetEdgeWeight(source, target string) float64 {
	srcIdx, okSrc := g.nodeToIdx[g.canonicalize(source)]
	dstIdx, okDst := g.nodeToIdx[g.canonicalize(target)]
	if !okSrc || !okDst || g.adjacency == nil {
		return 0
	}
	return float64(g.adjacency.get(srcIdx, dstIdx))
}

// GraphRevision 返回图版本号
func (g *GraphStore) GraphRevision() int64 { return g.graphRevision }

// NodeRevision 返回节点版本号
func (g *GraphStore) NodeRevision() int64 { return g.nodeRevision }

// NumNodes 返回节点数量
func (g *GraphStore) NumNodes() int { return len(g.nodes) }

// NumEdges 返回存储项数量
func (g *GraphStore) NumEdges() int {
	if g.adjacency == nil {
		return 0
	}
	return g.adjacency.numEdges()
}

// Density 返回图密度，节点少于 2 时为 0
func (g *GraphStore) Density() float64 {
	nodes := len(g.nodes)
	if nodes < 2 {
		return 0
	}
	return float64(g.NumEdges()) / float64(nodes*(nodes-1))
}

// HasData 判断构造时数据目录下是否存在 legacy 图元数据
func (g *GraphStore) HasData() bool {
	if g.dataDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(g.dataDir, graphMetadataFile))
	return err == nil
}

// Clear 清空图内容，不重置版本号
func (g *GraphStore) Clear() {
	hadNodes := len(g.nodes) > 0
	hadContent := hadNodes || (g.adjacency != nil && g.adjacency.numEdges() > 0)
	g.nodes = []string{}
	g.nodeToIdx = map[string]int{}
	g.nodeAttrs = map[string]map[string]any{}
	g.adjacency = nil
	g.adjacencyT = nil
	g.adjacencyDirty = true
	g.saliencyCache = nil
	g.edgeHashMap = map[edgeKey]map[string]struct{}{}
	g.edgeHashOrder = nil
	g.totalNodesAdded = 0
	g.totalEdgesAdded = 0
	g.totalNodesDeleted = 0
	g.totalEdgesDeleted = 0
	if hadContent {
		g.recordGraphChange(hadNodes)
	}
}

// addEdgeHash 记录边关系哈希并保持首次插入顺序
func (g *GraphStore) addEdgeHash(key edgeKey, hash string) {
	hashes, ok := g.edgeHashMap[key]
	if !ok {
		hashes = make(map[string]struct{})
		g.edgeHashMap[key] = hashes
		g.edgeHashOrder = append(g.edgeHashOrder, key)
	}
	hashes[hash] = struct{}{}
}

// removeEdgeHash 删除边键及其关系哈希
func (g *GraphStore) removeEdgeHash(key edgeKey) {
	if _, ok := g.edgeHashMap[key]; !ok {
		return
	}
	delete(g.edgeHashMap, key)
	for i, existing := range g.edgeHashOrder {
		if existing == key {
			g.edgeHashOrder = append(g.edgeHashOrder[:i], g.edgeHashOrder[i+1:]...)
			return
		}
	}
}

// pythonMax 对齐 Python max 的 NaN 语义：第二个参数与第一个比较为真才替换
func pythonMax(first, second float64) float64 {
	if second > first {
		return second
	}
	return first
}

// pythonMin 对齐 Python min 的 NaN 语义：第二个参数与第一个比较为真才替换
func pythonMin(first, second float64) float64 {
	if second < first {
		return second
	}
	return first
}

// formatPythonFloat 尽力模拟 Python str(float) 的展示形式
func formatPythonFloat(value float64) string {
	if math.IsNaN(value) {
		return "nan"
	}
	if math.IsInf(value, 0) {
		if value > 0 {
			return "inf"
		}
		return "-inf"
	}
	text := strconv.FormatFloat(value, 'g', -1, 64)
	if !strings.ContainsAny(text, ".eE") {
		text += ".0"
	}
	return text
}

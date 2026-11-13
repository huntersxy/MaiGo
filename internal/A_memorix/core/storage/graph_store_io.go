package storage

// A_memorix 图持久化，对齐 Python graph_store.py 的快照目录 + legacy 镜像 + SQLite 边映射布局
// 邻接矩阵使用 scipy 兼容 npz（graph_store_npz.go），图元数据使用 JSON

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// 图持久化文件名与表名，对齐 Python 侧常量
const (
	graphMetadataFile        = "graph_metadata.json"
	graphAdjacencyFile       = "graph_adjacency.npz"
	graphSnapshotPointerFile = "graph_snapshot.json"
	graphSnapshotsDirName    = "graph_snapshots"
	graphEdgeMapTable        = "graph_edge_relation_map"
)

// EdgeMapConn 图边映射表所需的数据库能力，*Conn、*MigrationDB、*sql.DB 均满足
type EdgeMapConn interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// EnsureGraphEdgeMapTable 确保图边关系哈希表存在
func EnsureGraphEdgeMapTable(conn EdgeMapConn) error {
	if conn == nil {
		return fmt.Errorf("图边映射数据库连接为空")
	}
	if _, err := conn.Exec(graphEdgeMapDDL); err != nil {
		return fmt.Errorf("创建图边映射表失败: %w", err)
	}
	if _, err := conn.Exec(graphEdgeMapIndexDDL); err != nil {
		return fmt.Errorf("创建图边映射索引失败: %w", err)
	}
	return nil
}

// Save 保存图到指定目录，dataDir 为空时使用构造时的目录
func (g *GraphStore) Save(dataDir string) error {
	if dataDir == "" {
		dataDir = g.dataDir
	}
	if dataDir == "" {
		return fmt.Errorf("未指定数据目录")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("创建图数据目录失败: %w", err)
	}

	metadata := g.buildMetadata()
	generation, err := g.prepareSnapshot(dataDir, metadata)
	if err != nil {
		return err
	}

	matrixPath := filepath.Join(dataDir, graphAdjacencyFile)
	if g.adjacency != nil {
		if err := writeAdjacencyNPZ(matrixPath, g.adjacency, g.matrixFormat); err != nil {
			return err
		}
	} else if err := os.Remove(matrixPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除陈旧图邻接矩阵失败: %w", err)
	}

	saved, err := g.saveEdgeHashMap(dataDir)
	if err != nil {
		return err
	}
	if !saved {
		metadata["edge_hash_map"] = g.serializeEdgeHashMap()
	}

	if err := writeGraphJSONObject(filepath.Join(dataDir, graphMetadataFile), metadata); err != nil {
		return fmt.Errorf("保存图元数据失败: %w", err)
	}
	if err := activateSnapshot(dataDir, generation); err != nil {
		return err
	}
	cleanupOldSnapshots(dataDir, generation)
	zap.L().Debug("图存储已保存", zap.String("data_dir", dataDir))
	return nil
}

// buildMetadata 组装 legacy 图元数据
func (g *GraphStore) buildMetadata() map[string]any {
	nodeToIdx := make(map[string]int, len(g.nodeToIdx))
	for key, value := range g.nodeToIdx {
		nodeToIdx[key] = value
	}
	nodeAttrs := make(map[string]map[string]any, len(g.nodeAttrs))
	for key, value := range g.nodeAttrs {
		nodeAttrs[key] = value
	}
	return map[string]any{
		"nodes":               append([]string{}, g.nodes...),
		"node_to_idx":         nodeToIdx,
		"node_attrs":          nodeAttrs,
		"matrix_format":       string(g.matrixFormat),
		"total_nodes_added":   g.totalNodesAdded,
		"total_edges_added":   g.totalEdgesAdded,
		"total_nodes_deleted": g.totalNodesDeleted,
		"total_edges_deleted": g.totalEdgesDeleted,
		"schema_version":      graphSchemaVersion,
	}
}

// prepareSnapshot 写入不可变快照目录并返回 generation
func (g *GraphStore) prepareSnapshot(dataDir string, metadata map[string]any) (string, error) {
	snapshotsRoot := filepath.Join(dataDir, graphSnapshotsDirName)
	if err := os.MkdirAll(snapshotsRoot, 0o755); err != nil {
		return "", fmt.Errorf("创建图快照目录失败: %w", err)
	}
	generation := "graph-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	snapshotDir := filepath.Join(snapshotsRoot, generation)
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		return "", fmt.Errorf("创建图快照失败: %w", err)
	}

	snapshotMetadata := make(map[string]any, len(metadata)+3)
	for key, value := range metadata {
		snapshotMetadata[key] = value
	}
	snapshotMetadata["snapshot_generation"] = generation
	snapshotMetadata["has_adjacency"] = g.adjacency != nil
	snapshotMetadata["edge_hash_map"] = g.serializeEdgeHashMap()

	if g.adjacency != nil {
		if err := writeAdjacencyNPZ(filepath.Join(snapshotDir, graphAdjacencyFile), g.adjacency, g.matrixFormat); err != nil {
			return "", err
		}
	}
	if err := writeGraphJSONObject(filepath.Join(snapshotDir, graphMetadataFile), snapshotMetadata); err != nil {
		return "", fmt.Errorf("写入图快照元数据失败: %w", err)
	}
	return generation, nil
}

// activateSnapshot 原子切换快照指针
func activateSnapshot(dataDir string, generation string) error {
	payload := map[string]any{"generation": generation, "schema_version": graphSchemaVersion}
	if err := writeGraphJSONObject(filepath.Join(dataDir, graphSnapshotPointerFile), payload); err != nil {
		return fmt.Errorf("激活图快照失败: %w", err)
	}
	return nil
}

// cleanupOldSnapshots 保留当前快照与最新两个快照，清理其余目录
func cleanupOldSnapshots(dataDir string, activeGeneration string) {
	snapshotsRoot := filepath.Join(dataDir, graphSnapshotsDirName)
	entries, err := os.ReadDir(snapshotsRoot)
	if err != nil {
		if !os.IsNotExist(err) {
			zap.L().Warn("读取图快照目录失败", zap.Error(err))
		}
		return
	}
	type snapshotDir struct {
		name    string
		modTime time.Time
	}
	dirs := make([]snapshotDir, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			zap.L().Warn("读取图快照信息失败", zap.String("name", entry.Name()), zap.Error(err))
			continue
		}
		dirs = append(dirs, snapshotDir{name: entry.Name(), modTime: info.ModTime()})
	}
	sort.Slice(dirs, func(i, j int) bool {
		if !dirs[i].modTime.Equal(dirs[j].modTime) {
			return dirs[i].modTime.After(dirs[j].modTime)
		}
		return dirs[i].name < dirs[j].name
	})
	keep := map[string]struct{}{activeGeneration: {}}
	for idx := 0; idx < len(dirs) && idx < 2; idx++ {
		keep[dirs[idx].name] = struct{}{}
	}
	for _, dir := range dirs {
		if _, ok := keep[dir.name]; ok {
			continue
		}
		if err := os.RemoveAll(filepath.Join(snapshotsRoot, dir.name)); err != nil {
			zap.L().Warn("清理旧图快照失败", zap.String("name", dir.name), zap.Error(err))
		}
	}
}

// Load 从指定目录加载图，dataDir 为空时使用构造时的目录
func (g *GraphStore) Load(dataDir string) error {
	if dataDir == "" {
		dataDir = g.dataDir
	}
	if dataDir == "" {
		return fmt.Errorf("未指定数据目录")
	}
	info, err := os.Stat(dataDir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("数据目录不存在: %s", dataDir)
	}

	metadataPath, matrixPath, snapshotActive, err := resolveSnapshotPaths(dataDir)
	if err != nil {
		return err
	}
	if _, err := os.Stat(metadataPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("元数据文件不存在: %s", metadataPath)
		}
		return fmt.Errorf("读取图元数据失败: %w", err)
	}
	metadata, err := readGraphJSONObject(metadataPath)
	if err != nil {
		return err
	}

	nodes, err := metadataNodes(metadata)
	if err != nil {
		return err
	}
	origAttrs, err := metadataNodeAttrs(metadata)
	if err != nil {
		return err
	}
	nodeToIdx := make(map[string]int, len(nodes))
	nodeAttrs := make(map[string]map[string]any, len(nodes))
	for idx, name := range nodes {
		canon := g.canonicalize(name)
		if _, exists := nodeToIdx[canon]; !exists {
			nodeToIdx[canon] = idx
		}
		if attrs, ok := origAttrs[name]; ok {
			if _, exists := nodeAttrs[canon]; !exists {
				nodeAttrs[canon] = attrs
			}
		}
	}

	matrixFormatText, err := metadataRequiredString(metadata, "matrix_format")
	if err != nil {
		return err
	}
	totalNodesAdded, err := metadataRequiredInt(metadata, "total_nodes_added")
	if err != nil {
		return err
	}
	totalEdgesAdded, err := metadataRequiredInt(metadata, "total_edges_added")
	if err != nil {
		return err
	}
	totalNodesDeleted, err := metadataRequiredInt(metadata, "total_nodes_deleted")
	if err != nil {
		return err
	}
	totalEdgesDeleted, err := metadataRequiredInt(metadata, "total_edges_deleted")
	if err != nil {
		return err
	}

	edgeMap := map[edgeKey]map[string]struct{}{}
	edgeOrder := make([]edgeKey, 0)
	if !snapshotActive {
		edgeMap, edgeOrder, err = loadEdgeHashMap(dataDir)
		if err != nil {
			return err
		}
	}
	if len(edgeMap) == 0 {
		if raw, ok := metadata["edge_hash_map"]; ok {
			if metadataMap, ok := raw.(map[string]any); ok {
				keys := make([]string, 0, len(metadataMap))
				for key := range metadataMap {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, keyText := range keys {
					key, ok, err := parseEdgeMapKey(keyText)
					if err != nil {
						return err
					}
					if !ok {
						continue
					}
					hashes := edgeHashMapValues(metadataMap[keyText])
					set := make(map[string]struct{}, len(hashes))
					for _, hash := range hashes {
						set[hash] = struct{}{}
					}
					if _, exists := edgeMap[key]; !exists {
						edgeOrder = append(edgeOrder, key)
					}
					edgeMap[key] = set
				}
			}
		}
	}

	if _, err := os.Stat(matrixPath); err == nil {
		matrix, err := readAdjacencyNPZ(matrixPath)
		if err != nil {
			return err
		}
		g.adjacency = matrix
	} else if os.IsNotExist(err) {
		if snapshotActive && metadataBool(metadata, "has_adjacency") {
			return fmt.Errorf("图快照邻接矩阵不存在: %s", matrixPath)
		}
		g.adjacency = nil
	} else {
		return fmt.Errorf("读取图邻接矩阵失败: %w", err)
	}

	currentN := len(nodes)
	if g.adjacency != nil {
		adjacencyN := g.adjacency.size
		switch {
		case currentN == 0:
			zap.L().Warn("检测到空图元数据但邻接矩阵仍然存在，已重置为空图")
			g.adjacency = nil
			edgeMap = map[edgeKey]map[string]struct{}{}
			edgeOrder = nil
		case currentN > adjacencyN:
			zap.L().Warn("检测到图存储维度不匹配，正在自动扩展",
				zap.Int("nodes", currentN), zap.Int("matrix", adjacencyN))
			g.adjacency.growTo(currentN)
		case currentN < adjacencyN:
			zap.L().Warn("检测到过期邻接矩阵，正在重置",
				zap.Int("nodes", currentN), zap.Int("matrix", adjacencyN))
			g.adjacency = newSparseMatrix(currentN)
			filtered := make(map[edgeKey]map[string]struct{}, len(edgeMap))
			filteredOrder := make([]edgeKey, 0, len(edgeOrder))
			for _, key := range edgeOrder {
				hashes, ok := edgeMap[key]
				if !ok {
					continue
				}
				if int(key.src) < currentN && int(key.dst) < currentN {
					filtered[key] = hashes
					filteredOrder = append(filteredOrder, key)
				}
			}
			edgeMap = filtered
			edgeOrder = filteredOrder
		}
	}

	g.nodes = nodes
	g.nodeToIdx = nodeToIdx
	g.nodeAttrs = nodeAttrs
	g.matrixFormat = SparseMatrixFormat(strings.ToLower(matrixFormatText))
	g.totalNodesAdded = totalNodesAdded
	g.totalEdgesAdded = totalEdgesAdded
	g.totalNodesDeleted = totalNodesDeleted
	g.totalEdgesDeleted = totalEdgesDeleted
	g.edgeHashMap = edgeMap
	g.edgeHashOrder = edgeOrder
	g.adjacencyT = nil
	g.adjacencyDirty = true
	g.saliencyCache = nil
	g.recordGraphChange(true)
	zap.L().Info("图存储已加载", zap.Int("nodes", len(g.nodes)), zap.Int("edges", g.NumEdges()))
	return nil
}

// resolveSnapshotPaths 解析快照指针，返回元数据路径、邻接矩阵路径与是否启用快照
func resolveSnapshotPaths(dataDir string) (string, string, bool, error) {
	pointerPath := filepath.Join(dataDir, graphSnapshotPointerFile)
	if _, err := os.Stat(pointerPath); err != nil {
		if os.IsNotExist(err) {
			return filepath.Join(dataDir, graphMetadataFile), filepath.Join(dataDir, graphAdjacencyFile), false, nil
		}
		return "", "", false, fmt.Errorf("读取图快照指针失败: %w", err)
	}
	pointer, err := readGraphJSONObject(pointerPath)
	if err != nil {
		return "", "", false, err
	}
	generation := ""
	if raw, ok := pointer["generation"]; ok && raw != nil {
		generation = strings.TrimSpace(fmt.Sprint(raw))
	}
	if generation == "" || filepath.Base(generation) != generation || !strings.HasPrefix(generation, "graph-") {
		return "", "", false, fmt.Errorf("图快照指针包含非法 generation")
	}
	snapshotDir := filepath.Join(dataDir, graphSnapshotsDirName, generation)
	metadataPath := filepath.Join(snapshotDir, graphMetadataFile)
	if _, err := os.Stat(metadataPath); err != nil {
		if os.IsNotExist(err) {
			return "", "", false, fmt.Errorf("图快照元数据不存在: %s", metadataPath)
		}
		return "", "", false, fmt.Errorf("读取图快照元数据失败: %w", err)
	}
	return metadataPath, filepath.Join(snapshotDir, graphAdjacencyFile), true, nil
}

// serializeEdgeHashMap 序列化边关系哈希映射，键为 "src,dst"，值为排序后的哈希列表
func (g *GraphStore) serializeEdgeHashMap() map[string][]string {
	result := make(map[string][]string, len(g.edgeHashMap))
	for _, key := range g.edgeHashOrder {
		hashes, ok := g.edgeHashMap[key]
		if !ok {
			continue
		}
		values := make([]string, 0, len(hashes))
		for hash := range hashes {
			if strings.TrimSpace(hash) != "" {
				values = append(values, hash)
			}
		}
		sort.Strings(values)
		result[fmt.Sprintf("%d,%d", key.src, key.dst)] = values
	}
	return result
}

// saveEdgeHashMap 将边关系哈希写入 SQLite，数据库不存在时返回 false
func (g *GraphStore) saveEdgeHashMap(dataDir string) (bool, error) {
	dbPath := edgeMapDBPath(dataDir)
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("检查图边映射数据库失败: %w", err)
	}
	conn, err := newConn(dbPath, DefaultTimeout)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	if err := EnsureGraphEdgeMapTable(conn); err != nil {
		return false, err
	}
	err = TransactionOn(conn, false, func(tx *Tx) error {
		if _, err := tx.Exec("DELETE FROM " + graphEdgeMapTable); err != nil {
			return err
		}
		for _, key := range g.edgeHashOrder {
			hashes, ok := g.edgeHashMap[key]
			if !ok {
				continue
			}
			values := make([]string, 0, len(hashes))
			for hash := range hashes {
				if strings.TrimSpace(hash) != "" {
					values = append(values, hash)
				}
			}
			sort.Strings(values)
			for _, hash := range values {
				if _, err := tx.Exec("INSERT OR IGNORE INTO "+graphEdgeMapTable+" (src_idx, dst_idx, relation_hash) VALUES (?, ?, ?)", key.src, key.dst, hash); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("保存图边映射失败: %w", err)
	}
	return true, nil
}

// loadEdgeHashMap 从 SQLite 读取边关系哈希，数据库不存在时返回空映射
func loadEdgeHashMap(dataDir string) (map[edgeKey]map[string]struct{}, []edgeKey, error) {
	dbPath := edgeMapDBPath(dataDir)
	result := map[edgeKey]map[string]struct{}{}
	order := make([]edgeKey, 0)
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return result, order, nil
		}
		return nil, nil, fmt.Errorf("检查图边映射数据库失败: %w", err)
	}
	conn, err := newConn(dbPath, DefaultTimeout)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = conn.Close() }()
	if err := EnsureGraphEdgeMapTable(conn); err != nil {
		return nil, nil, err
	}
	rows, err := conn.Query("SELECT src_idx, dst_idx, relation_hash FROM " + graphEdgeMapTable)
	if err != nil {
		return nil, nil, fmt.Errorf("读取图边映射失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var src int64
		var dst int64
		var relationHash sql.NullString
		if err := rows.Scan(&src, &dst, &relationHash); err != nil {
			return nil, nil, fmt.Errorf("读取图边映射失败: %w", err)
		}
		token := strings.TrimSpace(relationHash.String)
		if token == "" {
			continue
		}
		key := edgeKey{src: int32(src), dst: int32(dst)}
		if _, exists := result[key]; !exists {
			order = append(order, key)
		}
		if result[key] == nil {
			result[key] = map[string]struct{}{}
		}
		result[key][token] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("读取图边映射失败: %w", err)
	}
	return result, order, nil
}

// edgeMapDBPath 返回图边映射数据库路径：<data_dir 的父目录>/metadata/metadata.db
func edgeMapDBPath(dataDir string) string {
	parent := filepath.Dir(filepath.Clean(dataDir))
	return filepath.Join(parent, "metadata", "metadata.db")
}

// readJSONObject 读取 JSON 对象，非对象时显式报错
func readGraphJSONObject(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 JSON 元数据失败: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("解析 JSON 元数据失败: %w", err)
	}
	normalized, ok := normalizeJSONNumbers(payload).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("JSON 元数据必须是对象: %s", path)
	}
	return normalized, nil
}

// writeJSONObject 原子写入 JSON 对象，格式对齐 Python 的 indent=2、sort_keys=True
func writeGraphJSONObject(path string, payload map[string]any) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(payload); err != nil {
		return fmt.Errorf("序列化 JSON 元数据失败: %w", err)
	}
	return atomicWriteFile(path, buffer.Bytes())
}

// atomicWriteFile 写临时文件后重命名，模拟 Python atomic_write
func atomicWriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0o644); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("原子写入失败: %w", err)
	}
	return nil
}

// metadataNodes 读取节点列表，要求为字符串数组
func metadataNodes(metadata map[string]any) ([]string, error) {
	raw, ok := metadata["nodes"]
	if !ok {
		return nil, fmt.Errorf("图元数据缺少 nodes 字段")
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("图元数据 nodes 字段必须为数组")
	}
	nodes := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("图元数据节点名必须为字符串: %v", value)
		}
		nodes = append(nodes, text)
	}
	return nodes, nil
}

// metadataNodeAttrs 读取节点属性，要求值为 JSON 对象
func metadataNodeAttrs(metadata map[string]any) (map[string]map[string]any, error) {
	result := map[string]map[string]any{}
	raw, ok := metadata["node_attrs"]
	if !ok || raw == nil {
		return result, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("图元数据 node_attrs 字段必须为对象")
	}
	for key, value := range values {
		attrs, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("图元数据节点属性必须为对象: %s", key)
		}
		result[key] = attrs
	}
	return result, nil
}

// metadataRequiredString 读取必填字符串字段
func metadataRequiredString(metadata map[string]any, key string) (string, error) {
	raw, ok := metadata[key]
	if !ok {
		return "", fmt.Errorf("图元数据缺少 %s 字段", key)
	}
	text, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("图元数据 %s 字段必须为字符串", key)
	}
	return text, nil
}

// metadataRequiredInt 读取必填整数字段
func metadataRequiredInt(metadata map[string]any, key string) (int64, error) {
	raw, ok := metadata[key]
	if !ok {
		return 0, fmt.Errorf("图元数据缺少 %s 字段", key)
	}
	switch value := raw.(type) {
	case int64:
		return value, nil
	case float64:
		return int64(value), nil
	default:
		return 0, fmt.Errorf("图元数据 %s 字段必须为整数", key)
	}
}

// metadataBool 读取布尔字段，缺失或类型不符时返回 false
func metadataBool(metadata map[string]any, key string) bool {
	value, ok := metadata[key].(bool)
	return ok && value
}

// parseEdgeMapKey 解析 "src,dst" 形式的边映射键
func parseEdgeMapKey(text string) (edgeKey, bool, error) {
	token := strings.Trim(text, "()")
	if !strings.Contains(token, ",") {
		return edgeKey{}, false, nil
	}
	parts := strings.SplitN(token, ",", 2)
	src, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return edgeKey{}, false, fmt.Errorf("解析图边映射键失败: %s", text)
	}
	dst, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return edgeKey{}, false, fmt.Errorf("解析图边映射键失败: %s", text)
	}
	return edgeKey{src: int32(src), dst: int32(dst)}, true, nil
}

// edgeHashMapValues 将 JSON 值转换为哈希列表，非数组视为单值
func edgeHashMapValues(value any) []string {
	values, ok := value.([]any)
	if !ok {
		return []string{edgeHashValueString(value)}
	}
	result := make([]string, 0, len(values))
	for _, item := range values {
		result = append(result, edgeHashValueString(item))
	}
	return result
}

// edgeHashValueString 尽力模拟 Python str() 的文本形式
func edgeHashValueString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return "None"
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return formatPythonFloat(typed)
	default:
		return fmt.Sprint(typed)
	}
}

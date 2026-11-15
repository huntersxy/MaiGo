package scripts

// LPMM parquet 向量读取与写入，对齐 convert_vectors 及行级转换逻辑

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/parquet-go/parquet-go"

	"MaiGo/internal/A_memorix/core/storage"
)

// lpmmParquetFile 对齐 convert_vectors 中的 parquet_files 表项
type lpmmParquetFile struct {
	itemType string
	path     string
}

// convertVectors 对齐 convert_vectors
func (c *LPMMParquetConverter) convertVectors() error {
	files := []lpmmParquetFile{
		{itemType: "paragraph", path: filepath.Join(c.inputDir, "paragraph.parquet")},
		{itemType: "entity", path: filepath.Join(c.inputDir, "entity.parquet")},
		{itemType: "relation", path: filepath.Join(c.inputDir, "relation.parquet")},
	}
	for _, item := range files {
		if !pathExists(item.path) {
			c.logger.Warningf("文件未找到: %s, 跳过 %s 向量。", item.path, item.itemType)
			continue
		}
		c.logger.Infof("正在处理 %s 向量，来源: %s...", item.itemType, item.path)
		if err := c.convertParquetFile(item); err != nil {
			return err
		}
	}

	total := c.vectorStats["paragraph"] + c.vectorStats["entity"] + c.vectorStats["relation"]
	if total <= 0 {
		return fmt.Errorf("LPMM 输入没有产生任何可用向量，拒绝发布 ready 标志")
	}
	c.logger.Infof("向量转换完成。总向量数: %d", total)
	return nil
}

// convertParquetFile 处理单个 parquet 文件
func (c *LPMMParquetConverter) convertParquetFile(item lpmmParquetFile) error {
	handle, err := os.Open(item.path)
	if err != nil {
		return err
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil {
		return err
	}
	file, err := parquet.OpenFile(handle, info.Size())
	if err != nil {
		return err
	}
	if file.NumRows() == 0 {
		c.logger.Infof("%s 为空，跳过。", item.path)
		return nil
	}

	columnIndexes, columnNames := parquetTopLevelColumns(file)
	contentColumn := ""
	if _, ok := columnIndexes["str"]; ok {
		contentColumn = "str"
	} else if _, ok := columnIndexes["content"]; ok {
		contentColumn = "content"
	}
	_, hasSubject := columnIndexes["subject"]
	_, hasPredicate := columnIndexes["predicate"]
	_, hasObject := columnIndexes["object"]
	hasTripleColumns := hasSubject && hasPredicate && hasObject

	if _, ok := columnIndexes["embedding"]; !ok {
		return fmt.Errorf("%s 缺少 embedding 列: %s", item.path, pyStringListRepr(columnNames))
	}
	if item.itemType != "relation" && contentColumn == "" {
		return fmt.Errorf("%s 缺少 str 或 content 列: %s", item.path, pyStringListRepr(columnNames))
	}
	if item.itemType == "relation" && !hasTripleColumns && contentColumn == "" {
		return fmt.Errorf("%s 缺少关系三元组或可解析文本: %s", item.path, pyStringListRepr(columnNames))
	}

	processedRows, err := c.processParquetRows(file, columnIndexes, item.itemType, contentColumn, hasTripleColumns)
	if err != nil {
		return err
	}
	c.logger.Infof("%s 向量处理完成：总扫描 %d，总导入 %d", item.itemType, processedRows, c.vectorStats[item.itemType])
	return nil
}

// parquetTopLevelColumns 返回顶层字段到叶子列索引的映射，以及排序后的字段名
func parquetTopLevelColumns(file *parquet.File) (map[string]int, []string) {
	indexes := map[string]int{}
	names := make([]string, 0)
	for index, path := range file.Schema().Columns() {
		if len(path) == 0 {
			continue
		}
		name := path[0]
		if _, ok := indexes[name]; ok {
			continue
		}
		indexes[name] = index
		names = append(names, name)
	}
	sort.Strings(names)
	return indexes, names
}

// processParquetRows 按 batch 读取并写入向量，对齐 iter_batches 的批内去重
func (c *LPMMParquetConverter) processParquetRows(file *parquet.File, columnIndexes map[string]int, itemType string, contentColumn string, hasTripleColumns bool) (int, error) {
	reader := parquet.NewReader(file)
	defer reader.Close()
	processedRows := 0
	for {
		rows := make([]parquet.Row, c.batchSize)
		count, err := reader.ReadRows(rows)
		if count > 0 {
			if processErr := c.processParquetBatch(rows[:count], columnIndexes, itemType, contentColumn, hasTripleColumns, &processedRows); processErr != nil {
				return processedRows, processErr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return processedRows, err
		}
		if count == 0 {
			break
		}
	}
	return processedRows, nil
}

// processParquetBatch 转换一个 batch 的行并在末尾写入向量与关系边
func (c *LPMMParquetConverter) processParquetBatch(rows []parquet.Row, columnIndexes map[string]int, itemType string, contentColumn string, hasTripleColumns bool, processedRows *int) error {
	vectors := make([][]float32, 0, len(rows))
	ids := make([]string, 0, len(rows))
	relationHashes := make([]string, 0)
	relationEdges := make([]storage.Edge, 0)
	relationWeights := make([]float64, 0)
	seenVectorIDs := map[string]struct{}{}

	for _, row := range rows {
		(*processedRows)++
		rowValues := parquetRowValues(row)
		embeddingValues := rowValues[columnIndexes["embedding"]]
		embedding, err := decodeParquetEmbedding(embeddingValues)
		if err != nil {
			return fmt.Errorf("%s 第 %d 行: %w", itemType, *processedRows, err)
		}
		if len(embedding) != c.dimension {
			return fmt.Errorf("%s 第 %d 行向量维度不匹配: %s vs (%d,)", itemType, *processedRows, parquetEmbeddingShape(embeddingValues), c.dimension)
		}
		for _, value := range embedding {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return fmt.Errorf("%s 第 %d 行向量包含非有限值", itemType, *processedRows)
			}
		}

		var storeID string
		var vectorID string
		var subject string
		var object string
		switch itemType {
		case "relation":
			var predicate string
			subject, predicate, object = c.parquetRelationTriple(rowValues, columnIndexes, contentColumn, hasTripleColumns)
			if subject == "" || predicate == "" || object == "" {
				return fmt.Errorf("relation 第 %d 行无法解析为完整三元组", *processedRows)
			}
			storeID, err = c.metadataStore.AddRelation(subject, predicate, object, nil, 1.0, "", nil)
			if err != nil {
				return err
			}
			vectorID = "relation:" + storeID
		case "paragraph":
			content := strings.TrimSpace(parquetColumnText(rowValues, columnIndexes[contentColumn]))
			if content == "" {
				return fmt.Errorf("paragraph 第 %d 行内容为空", *processedRows)
			}
			storeID, err = c.metadataStore.AddParagraph(content, nil, "lpmm_import", nil, "factual", nil)
			if err != nil {
				return err
			}
			vectorID = storeID
		case "entity":
			content := strings.TrimSpace(parquetColumnText(rowValues, columnIndexes[contentColumn]))
			if content == "" {
				return fmt.Errorf("entity 第 %d 行内容为空", *processedRows)
			}
			storeID, err = c.metadataStore.AddEntity(content, nil, "", nil)
			if err != nil {
				return err
			}
			vectorID = "entity:" + storeID
		default:
			return fmt.Errorf("未知的 LPMM 数据类型: %s", itemType)
		}

		hashIndex, hasHash := columnIndexes["hash"]
		if hasHash {
			if hashValues := rowValues[hashIndex]; len(hashValues) > 0 && !hashValues[0].IsNull() && !parquetValueIsNaN(hashValues[0]) {
				c.registerIDMapping(parquetValueString(hashValues[0]), storeID, itemType)
			}
		}
		if _, exists := seenVectorIDs[vectorID]; exists {
			continue
		}
		seenVectorIDs[vectorID] = struct{}{}
		vectors = append(vectors, embedding)
		ids = append(ids, vectorID)
		if itemType == "relation" {
			relationHashes = append(relationHashes, storeID)
			relationEdges = append(relationEdges, storage.Edge{Source: subject, Target: object})
			relationWeights = append(relationWeights, 1.0)
		}
	}

	if len(vectors) == 0 {
		return nil
	}
	target := c.paragraphVectorStore
	if itemType != "paragraph" {
		target = c.graphVectorStore
	}
	added, err := target.Add(vectors, ids)
	if err != nil {
		return err
	}
	c.vectorStats[itemType] += added
	if len(relationEdges) > 0 {
		if _, err := c.graphStore.AddEdges(relationEdges, relationWeights, relationHashes); err != nil {
			return err
		}
		for _, relationHash := range relationHashes {
			if _, err := c.metadataStore.SetRelationVectorState(relationHash, "ready", nil, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// parquetRelationTriple 读取关系三元组，优先 subject/predicate/object 列
func (c *LPMMParquetConverter) parquetRelationTriple(rowValues map[int][]parquet.Value, columnIndexes map[string]int, contentColumn string, hasTripleColumns bool) (string, string, string) {
	if hasTripleColumns {
		return strings.TrimSpace(parquetColumnText(rowValues, columnIndexes["subject"])),
			strings.TrimSpace(parquetColumnText(rowValues, columnIndexes["predicate"])),
			strings.TrimSpace(parquetColumnText(rowValues, columnIndexes["object"]))
	}
	return parseLPMMRelationText(parquetColumnText(rowValues, columnIndexes[contentColumn]))
}

// parquetRowValues 按列索引收集一行中的值，重复列会给出多个值
func parquetRowValues(row parquet.Row) map[int][]parquet.Value {
	values := map[int][]parquet.Value{}
	row.Range(func(columnIndex int, columnValues []parquet.Value) bool {
		values[columnIndex] = columnValues
		return true
	})
	return values
}

// parquetColumnText 读取标量列文本，空值返回空字符串
func parquetColumnText(rowValues map[int][]parquet.Value, columnIndex int) string {
	values := rowValues[columnIndex]
	if len(values) == 0 {
		return ""
	}
	return parquetValueString(values[0])
}

// decodeParquetEmbedding 将 repeated float/double 列解码为 float32 向量
func decodeParquetEmbedding(values []parquet.Value) ([]float32, error) {
	embedding := make([]float32, 0, len(values))
	for _, value := range values {
		if value.IsNull() {
			embedding = append(embedding, float32(math.NaN()))
			continue
		}
		switch value.Kind() {
		case parquet.Float:
			embedding = append(embedding, value.Float())
		case parquet.Double:
			embedding = append(embedding, float32(value.Double()))
		case parquet.Int32:
			embedding = append(embedding, float32(value.Int32()))
		case parquet.Int64:
			embedding = append(embedding, float32(value.Int64()))
		default:
			return nil, fmt.Errorf("embedding 列包含不支持的物理类型: %s", value.Kind())
		}
	}
	return embedding, nil
}

// parquetEmbeddingShape 对齐 numpy shape 的展示
func parquetEmbeddingShape(values []parquet.Value) string {
	if len(values) == 0 {
		return "()"
	}
	return fmt.Sprintf("(%d,)", len(values))
}

// parquetValueString 对齐 pandas 单元格转 str 的常用行为
func parquetValueString(value parquet.Value) string {
	if value.IsNull() {
		return ""
	}
	switch value.Kind() {
	case parquet.ByteArray, parquet.FixedLenByteArray:
		return string(value.ByteArray())
	case parquet.Boolean:
		return strconv.FormatBool(value.Boolean())
	case parquet.Int32:
		return strconv.FormatInt(int64(value.Int32()), 10)
	case parquet.Int64:
		return strconv.FormatInt(value.Int64(), 10)
	case parquet.Float:
		return strconv.FormatFloat(float64(value.Float()), 'g', -1, 32)
	case parquet.Double:
		return strconv.FormatFloat(value.Double(), 'g', -1, 64)
	default:
		return ""
	}
}

// parquetValueIsNaN 判断浮点型 hash 是否为 NaN
func parquetValueIsNaN(value parquet.Value) bool {
	switch value.Kind() {
	case parquet.Float:
		return math.IsNaN(float64(value.Float()))
	case parquet.Double:
		return math.IsNaN(value.Double())
	default:
		return false
	}
}

// parseLPMMRelationText 对齐 _parse_relation_text
func parseLPMMRelationText(text string) (string, string, string) {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return "", "", ""
	}
	if strings.Contains(raw, "|") {
		parts := splitAndTrim(raw, "|")
		if len(parts) >= 3 {
			return parts[0], parts[1], parts[2]
		}
	}
	if strings.Contains(raw, "->") {
		parts := splitAndTrim(raw, "->")
		if len(parts) >= 3 {
			return parts[0], parts[1], parts[2]
		}
	}
	pieces := strings.Fields(raw)
	if len(pieces) >= 3 {
		return pieces[0], pieces[1], strings.Join(pieces[2:], " ")
	}
	return "", "", ""
}

// splitAndTrim 对齐 Python 按分隔符 split 后去空去空白
func splitAndTrim(value string, separator string) []string {
	rawParts := strings.Split(value, separator)
	parts := make([]string, 0, len(rawParts))
	for _, part := range rawParts {
		part = strings.TrimSpace(part)
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

// registerIDMapping 对齐 _register_id_mapping
func (c *LPMMParquetConverter) registerIDMapping(rawID any, mappedID string, itemType string) {
	raw := strings.TrimSpace(pyString(rawID))
	if raw == "" {
		return
	}
	c.idMapping[raw] = mappedID
	prefix := itemType + "-"
	if strings.HasPrefix(raw, prefix) {
		c.idMapping[strings.TrimPrefix(raw, prefix)] = mappedID
		return
	}
	c.idMapping[prefix+raw] = mappedID
}

// mapNodeID 对齐 _map_node_id
func (c *LPMMParquetConverter) mapNodeID(node string) string {
	if mapped, ok := c.idMapping[node]; ok {
		return mapped
	}
	return node
}

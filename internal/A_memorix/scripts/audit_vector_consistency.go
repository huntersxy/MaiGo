package scripts

// 向量一致性审计，对齐 src/A_memorix/scripts/audit_vector_consistency.py
// 统计向量覆盖率、关系向量状态分布、孤儿向量与状态不一致项

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"MaiGo/internal/A_memorix/core/storage"
	"MaiGo/internal/A_memorix/core/utils"
)

// auditResult 审计结果，字段顺序与 Python 输出一致
type auditResult struct {
	Counts struct {
		Paragraphs  int `json:"paragraphs"`
		Entities    int `json:"entities"`
		Relations   int `json:"relations"`
		VectorsLive int `json:"vectors_live"`
	} `json:"counts"`
	Coverage struct {
		ParagraphVectorCoverage float64 `json:"paragraph_vector_coverage"`
		EntityVectorCoverage    float64 `json:"entity_vector_coverage"`
		RelationVectorCoverage  float64 `json:"relation_vector_coverage"`
		RelationReadyCoverage   float64 `json:"relation_ready_coverage"`
	} `json:"coverage"`
	RelationStates map[string]int `json:"relation_states"`
	Orphans        struct {
		VectorOnlyCount  int      `json:"vector_only_count"`
		VectorOnlySample []string `json:"vector_only_sample"`
	} `json:"orphans"`
	ConsistencyChecks struct {
		ReadyButMissingVector    int `json:"ready_but_missing_vector"`
		NotReadyButVectorPresent int `json:"not_ready_but_vector_present"`
	} `json:"consistency_checks"`
}

// RunAuditVectorConsistency 执行 audit_vector_consistency.py 等价流程，返回进程退出码
func RunAuditVectorConsistency(ctx context.Context, argv []string) int {
	parser := newScriptParser(ctx, "audit_vector_consistency.py", "审计 A_Memorix 向量一致性")
	parser.AddString([]string{"--data-dir"}, DefaultDataDir(), "A_Memorix 数据目录（默认: data/a-memorix）")
	parser.AddString([]string{"--json-out"}, "", "可选：输出 JSON 文件路径")
	parser.AddBool([]string{"--strict"}, "若发现一致性异常则返回非 0 退出码")
	args, code, handled := parser.Parse(argv)
	if handled {
		return code
	}

	dataDir := ResolveRepoPath(args.String("data-dir"), DefaultDataDir())
	if _, err := os.Stat(dataDir); err != nil {
		scriptPrintf(ctx, "❌ 数据目录不存在: %s\n", dataDir)
		return 2
	}
	result, err := auditVectorConsistency(dataDir)
	if err != nil {
		scriptPrintf(ctx, "❌ 审计失败: %v\n", err)
		return 2
	}

	scriptPrintln(ctx, "=== A_Memorix Vector Consistency Audit ===")
	scriptPrintf(ctx, "data_dir: %s\n", dataDir)
	scriptPrintf(ctx, "paragraphs: %d\n", result.Counts.Paragraphs)
	scriptPrintf(ctx, "entities: %d\n", result.Counts.Entities)
	scriptPrintf(ctx, "relations: %d\n", result.Counts.Relations)
	scriptPrintf(ctx, "vectors_live: %d\n", result.Counts.VectorsLive)
	scriptPrintf(ctx, "coverage: paragraph=%.3f, entity=%.3f, relation=%.3f, relation_ready=%.3f\n",
		result.Coverage.ParagraphVectorCoverage,
		result.Coverage.EntityVectorCoverage,
		result.Coverage.RelationVectorCoverage,
		result.Coverage.RelationReadyCoverage,
	)
	scriptPrintf(ctx, "relation_states: %s\n", pythonStateRepr(result.RelationStates))
	scriptPrintf(ctx, "consistency_checks: ready_but_missing_vector=%d, not_ready_but_vector_present=%d\n",
		result.ConsistencyChecks.ReadyButMissingVector,
		result.ConsistencyChecks.NotReadyButVectorPresent,
	)
	scriptPrintf(ctx, "orphan_vectors: %d\n", result.Orphans.VectorOnlyCount)

	if jsonOut := strings.TrimSpace(args.String("json-out")); jsonOut != "" {
		outPath, err := filepath.Abs(jsonOut)
		if err != nil {
			scriptPrintf(ctx, "❌ 审计失败: %v\n", err)
			return 2
		}
		if err := os.MkdirAll(filepath.Dir(outPath), 0o777); err != nil {
			scriptPrintf(ctx, "❌ 审计失败: %v\n", err)
			return 2
		}
		payload, err := MarshalJSONIndent(result)
		if err != nil {
			scriptPrintf(ctx, "❌ 审计失败: %v\n", err)
			return 2
		}
		if err := os.WriteFile(outPath, []byte(payload), 0o666); err != nil {
			scriptPrintf(ctx, "❌ 审计失败: %v\n", err)
			return 2
		}
		scriptPrintf(ctx, "json_out: %s\n", outPath)
	}

	hasAnomaly := result.Orphans.VectorOnlyCount > 0 || result.ConsistencyChecks.ReadyButMissingVector > 0
	if args.Bool("strict") && hasAnomaly {
		return 1
	}
	return 0
}

// auditVectorConsistency 对齐 run_audit
func auditVectorConsistency(dataDir string) (auditResult, error) {
	var result auditResult
	vectorStore, err := loadAuditVectorStore(dataDir)
	if err != nil {
		return result, err
	}
	metadataDir := filepath.Join(dataDir, "metadata")
	metadataStore := storage.NewStore(metadataDir, "")
	if err := metadataStore.Connect(metadataDir, true); err != nil {
		return result, err
	}
	defer func() { _ = metadataStore.Close() }()

	paragraphHashes, err := auditHashSet(metadataStore, "paragraphs")
	if err != nil {
		return result, err
	}
	entityHashes, err := auditHashSet(metadataStore, "entities")
	if err != nil {
		return result, err
	}
	relationHashes, err := auditHashSet(metadataStore, "relations")
	if err != nil {
		return result, err
	}

	liveVectorHashes := make(map[string]struct{})
	for _, hashValue := range vectorStore.KnownHashes() {
		if vectorStore.Contains(hashValue) {
			liveVectorHashes[hashValue] = struct{}{}
		}
	}

	knownHashes := make(map[string]struct{}, len(paragraphHashes)+len(entityHashes)+len(relationHashes))
	for _, set := range []map[string]struct{}{paragraphHashes, entityHashes, relationHashes} {
		for hashValue := range set {
			knownHashes[hashValue] = struct{}{}
		}
	}
	orphanHashes := make([]string, 0)
	for hashValue := range liveVectorHashes {
		if _, known := knownHashes[hashValue]; !known {
			orphanHashes = append(orphanHashes, hashValue)
		}
	}
	sort.Strings(orphanHashes)
	sample := orphanHashes
	if len(sample) > 30 {
		sample = sample[:30]
	}

	relations, err := metadataStore.GetRelations("", "", "", true)
	if err != nil {
		return result, err
	}
	readyButMissing := 0
	notReadyButPresent := 0
	for _, relation := range relations {
		hashValue := strings.TrimSpace(utils.PyStr(relation["hash"]))
		state := strings.ToLower(utils.PyStr(relation["vector_state"]))
		if state == "" {
			state = "none"
		}
		_, inVector := liveVectorHashes[hashValue]
		if state == "ready" && !inVector {
			readyButMissing++
		}
		if state != "ready" && inVector {
			notReadyButPresent++
		}
	}

	states, err := metadataStore.CountRelationsByVectorState()
	if err != nil {
		return result, err
	}
	relationTotal := states["total"]
	readyCount := states["ready"]
	if relationTotal < 0 {
		relationTotal = 0
	}
	if readyCount < 0 {
		readyCount = 0
	}

	result.Counts.Paragraphs = len(paragraphHashes)
	result.Counts.Entities = len(entityHashes)
	result.Counts.Relations = len(relationHashes)
	result.Counts.VectorsLive = len(liveVectorHashes)
	result.Coverage.ParagraphVectorCoverage = auditSafeRatio(len(intersectHashSets(paragraphHashes, liveVectorHashes)), len(paragraphHashes))
	result.Coverage.EntityVectorCoverage = auditSafeRatio(len(intersectHashSets(entityHashes, liveVectorHashes)), len(entityHashes))
	result.Coverage.RelationVectorCoverage = auditSafeRatio(len(intersectHashSets(relationHashes, liveVectorHashes)), len(relationHashes))
	result.Coverage.RelationReadyCoverage = auditSafeRatio(readyCount, relationTotal)
	result.RelationStates = states
	result.Orphans.VectorOnlyCount = len(orphanHashes)
	result.Orphans.VectorOnlySample = sample
	result.ConsistencyChecks.ReadyButMissingVector = readyButMissing
	result.ConsistencyChecks.NotReadyButVectorPresent = notReadyButPresent
	return result, nil
}

// loadAuditVectorStore 对齐 _load_vector_store
func loadAuditVectorStore(dataDir string) (*storage.VectorStore, error) {
	vectorsDir := filepath.Join(dataDir, "vectors")
	metaPath := filepath.Join(vectorsDir, "vectors_metadata.json")
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("未找到向量元数据文件: %s", metaPath)
		}
		return nil, err
	}
	meta := map[string]any{}
	if err := UnmarshalJSON(raw, &meta); err != nil {
		return nil, err
	}
	dimension := 1024
	if value, exists := meta["dimension"]; exists {
		dimension = utils.PyIntOrZero(value)
	}
	if dimension < 1 {
		dimension = 1
	}
	store, err := storage.NewVectorStore(storage.VectorStoreConfig{
		Dimension:        dimension,
		QuantizationType: utils.QuantizationInt8,
		DataDir:          vectorsDir,
	})
	if err != nil {
		return nil, err
	}
	if store.HasData() {
		if err := store.Load("", nil, nil, ""); err != nil {
			return nil, err
		}
	}
	return store, nil
}

// auditHashSet 对齐 _hash_set
func auditHashSet(store *storage.Store, table string) (map[string]struct{}, error) {
	hashes, err := store.ListHashes(table)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(hashes))
	for _, hashValue := range hashes {
		out[utils.PyStr(hashValue)] = struct{}{}
	}
	return out, nil
}

// intersectHashSets 求两集合交集大小
func intersectHashSets(names map[string]struct{}, vectors map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{})
	for hashValue := range names {
		if _, exists := vectors[hashValue]; exists {
			out[hashValue] = struct{}{}
		}
	}
	return out
}

// auditSafeRatio 对齐 _safe_ratio
func auditSafeRatio(numerator int, denominator int) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

// pythonStateRepr 对齐 f-string 中 Python dict 的 repr 形态
// 差异说明：未知状态按字典序追加，Python 按游标返回顺序
func pythonStateRepr(states map[string]int) string {
	ordered := []string{"none", "pending", "ready", "failed"}
	seen := make(map[string]struct{}, len(ordered))
	parts := make([]string, 0, len(states))
	appendKey := func(key string, value int) {
		parts = append(parts, fmt.Sprintf("%s: %d", singleQuoteKey(key), value))
	}
	for _, key := range ordered {
		seen[key] = struct{}{}
		value, exists := states[key]
		if !exists {
			value = 0
		}
		appendKey(key, value)
	}
	extra := make([]string, 0)
	for key := range states {
		if _, exists := seen[key]; !exists && key != "total" {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	for _, key := range extra {
		appendKey(key, states[key])
	}
	appendKey("total", states["total"])
	return "{" + strings.Join(parts, ", ") + "}"
}

// singleQuoteKey 生成 Python repr 的单引号键
func singleQuoteKey(key string) string {
	escaped := strings.ReplaceAll(key, "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "'", "\\'")
	return "'" + escaped + "'"
}

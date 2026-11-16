package emojisystem

// 表情语义索引，对齐 src/emoji_system/semantic_match.py
//
// 标签与向量存放在实例目录的 data/emoji_semantic_index.sqlite3。
// 注册后异步排队索引；定时维护每轮补一小批，失败条目下轮轮转重试。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"MaiGo/internal/common/llmjson"
	"MaiGo/internal/llm_models"

	_ "github.com/glebarez/go-sqlite"
	"go.uber.org/zap"
)

const (
	emojiSemanticIndexRelativePath = "data/emoji_semantic_index.sqlite3"
	emojiSemanticBackfillBatch     = 4
)

var (
	emojiSemanticIndexPathOverride string
	emojiSemanticIndexLock         sync.Mutex
	emojiSemanticTagLock           sync.Mutex
	emojiSemanticPending           = map[string]struct{}{}
	emojiSemanticPendingMu         sync.Mutex
	emojiSemanticBackfillCursor    int
	emojiSemanticBackfillMu        sync.Mutex
)

// EmojiTags 表情包的情绪标签与画面标签
type EmojiTags struct {
	EmotionTags []string `json:"emotion_tags"`
	ContentTags []string `json:"content_tags"`
}

func (t EmojiTags) embeddingTexts() map[string]string {
	texts := map[string]string{"content": strings.Join(t.ContentTags, "、")}
	if len(t.EmotionTags) > 0 {
		texts["emotion"] = strings.Join(t.EmotionTags, "、")
	}
	return texts
}

func normalizeEmojiTags(tags []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}

func validateEmojiTags(raw string) (EmojiTags, error) {
	var tags EmojiTags
	if err := llmjson.Unmarshal(raw, &tags); err != nil {
		return tags, fmt.Errorf("表情分组标签不是合法 JSON: %w", err)
	}
	tags.EmotionTags = normalizeEmojiTags(tags.EmotionTags)
	tags.ContentTags = normalizeEmojiTags(tags.ContentTags)
	if len(tags.EmotionTags) > 5 {
		return tags, fmt.Errorf("表情分组标签的 emotion_tags 最多 5 个")
	}
	if len(tags.ContentTags) == 0 || len(tags.ContentTags) > 5 {
		return tags, fmt.Errorf("表情分组标签的 content_tags 数量必须在 1 到 5 之间")
	}
	return tags, nil
}

func emojiSemanticIndexPath() string {
	if path := strings.TrimSpace(emojiSemanticIndexPathOverride); path != "" {
		return path
	}
	registry := DefaultRegistry()
	registry.mu.RLock()
	mem := registry.mem
	registry.mu.RUnlock()
	if mem != nil {
		if root := strings.TrimSpace(mem.InstanceRoot()); root != "" {
			return filepath.Join(root, filepath.FromSlash(emojiSemanticIndexRelativePath))
		}
	}
	return filepath.FromSlash(emojiSemanticIndexRelativePath)
}

func openEmojiSemanticIndex() (*sql.DB, error) {
	path := emojiSemanticIndexPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS vectors (
		config TEXT, description TEXT, space TEXT, vector TEXT,
		PRIMARY KEY (config, description, space)
	)`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS emoji_tags (
		image_hash TEXT PRIMARY KEY, tags TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func readEmojiSemanticIndex(configKey, space string, labelsOnly bool) (map[string][]float64, error) {
	path := emojiSemanticIndexPath()
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return map[string][]float64{}, nil
		}
		return nil, err
	}
	db, err := openEmojiSemanticIndex()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	query := `SELECT description, vector FROM vectors WHERE config = ? AND space = ?`
	rows, err := db.Query(query, configKey, space)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]float64{}
	for rows.Next() {
		var label, encoded string
		if err := rows.Scan(&label, &encoded); err != nil {
			return nil, err
		}
		if labelsOnly {
			out[label] = nil
			continue
		}
		var vector []float64
		if err := json.Unmarshal([]byte(encoded), &vector); err != nil {
			continue
		}
		out[label] = vector
	}
	return out, rows.Err()
}

func readEmojiSemanticIndexLabels(configKey, space string) (map[string]struct{}, error) {
	values, err := readEmojiSemanticIndex(configKey, space, true)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(values))
	for label := range values {
		out[label] = struct{}{}
	}
	return out, nil
}

func saveEmojiSemanticVector(configKey, label, space string, vector []float64) error {
	db, err := openEmojiSemanticIndex()
	if err != nil {
		return err
	}
	defer db.Close()
	encoded, err := json.Marshal(vector)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT OR REPLACE INTO vectors(config, description, space, vector) VALUES(?,?,?,?)`,
		configKey, label, space, string(encoded))
	return err
}

func readEmojiTagsMap(emojiHash string) (map[string]EmojiTags, error) {
	path := emojiSemanticIndexPath()
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return map[string]EmojiTags{}, nil
		}
		return nil, err
	}
	db, err := openEmojiSemanticIndex()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	query := `SELECT image_hash, tags FROM emoji_tags`
	args := []any{}
	if strings.TrimSpace(emojiHash) != "" {
		query += ` WHERE image_hash = ?`
		args = append(args, emojiHash)
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]EmojiTags{}
	for rows.Next() {
		var hash, raw string
		if err := rows.Scan(&hash, &raw); err != nil {
			return nil, err
		}
		var tags EmojiTags
		if err := json.Unmarshal([]byte(raw), &tags); err != nil {
			continue
		}
		out[hash] = tags
	}
	return out, rows.Err()
}

// GetEmojiTags 读取表情包的分组标签；未索引时返回 false
func GetEmojiTags(emojiHash string) (EmojiTags, bool) {
	tags, err := readEmojiTagsMap(strings.TrimSpace(emojiHash))
	if err != nil {
		return EmojiTags{}, false
	}
	value, ok := tags[strings.TrimSpace(emojiHash)]
	return value, ok
}

func saveEmojiTags(emojiHash string, tags EmojiTags) error {
	db, err := openEmojiSemanticIndex()
	if err != nil {
		return err
	}
	defer db.Close()
	encoded, err := json.Marshal(tags)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT OR REPLACE INTO emoji_tags(image_hash, tags) VALUES(?,?)`, emojiHash, string(encoded))
	return err
}

type emojiSemanticEmbedding struct {
	client   llm.EmbeddingClient
	resolved llm.ResolvedRoute
}

func (r *Registry) semanticEmbedding(ctx context.Context, requestType, sessionID string) (emojiSemanticEmbedding, error) {
	r.mu.RLock()
	router := r.router
	r.mu.RUnlock()
	if router == nil || !router.HasTaskModels(llm.TaskEmbedding) {
		return emojiSemanticEmbedding{}, fmt.Errorf("未配置表情语义匹配所需的 Embedding 模型")
	}
	resolved, client, err := router.NewEmbeddingClientForTaskRequest(ctx, llm.TaskEmbedding, requestType, sessionID)
	if err != nil {
		return emojiSemanticEmbedding{}, err
	}
	return emojiSemanticEmbedding{client: client, resolved: resolved}, nil
}

func (r *Registry) semanticConfigKey(ctx context.Context) string {
	r.mu.RLock()
	router := r.router
	r.mu.RUnlock()
	if router == nil || !router.HasTaskModels(llm.TaskEmbedding) {
		return ""
	}
	resolved, err := router.Resolve(ctx, llm.TaskEmbedding)
	if err != nil {
		return ""
	}
	return semanticConfigKey(resolved)
}

func semanticConfigKey(resolved llm.ResolvedRoute) string {
	payload := []any{
		resolved.MaiBotModel,
		resolved.Config.Model,
		resolved.Config.ProviderName,
		resolved.Config.BaseURL,
	}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func semanticModelKey(resolved llm.ResolvedRoute, embedding []float64, model, identifier, provider string) string {
	input := llm.EmbeddingFingerprintInput{
		Model:           model,
		Provider:        provider,
		ModelIdentifier: identifier,
		BaseURL:         resolved.Config.BaseURL,
		Dimension:       len(embedding),
	}
	if fingerprint, err := llm.BuildEmbeddingFingerprint(input); err == nil {
		return fingerprint.Hash
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%d", model, identifier, provider, len(embedding))))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (r *Registry) vlmTaskConfigured() bool {
	r.mu.RLock()
	router := r.router
	r.mu.RUnlock()
	return router != nil && router.HasTaskModels(llm.TaskVLM)
}

func emojiSemanticAxisKey(configKey, axis string) string {
	return configKey + ":emoji-tags-v2:" + axis
}

func (r *Registry) indexEmoji(ctx context.Context, emoji *Emoji, configKey string) {
	if r == nil || emoji == nil {
		return
	}
	tags, ok := GetEmojiTags(emoji.FileHash)
	if !ok && r.vlmTaskConfigured() {
		emojiSemanticTagLock.Lock()
		tags, ok = GetEmojiTags(emoji.FileHash)
		if !ok {
			raw, err := r.AnalyzeEmojiImage(ctx, emoji, "emoji_grouped_tags")
			if err == nil {
				tags, err = validateEmojiTags(raw)
			}
			if err == nil {
				if saveErr := saveEmojiTags(emoji.FileHash, tags); saveErr != nil {
					zap.L().Warn("表情包分组标签写入失败", zap.String("hash", emoji.FileHash), zap.Error(saveErr))
				} else {
					ok = true
				}
			} else {
				zap.L().Warn("表情包分组标签生成失败，下次维护时重试", zap.String("hash", emoji.FileHash), zap.Error(err))
			}
		}
		emojiSemanticTagLock.Unlock()
	}
	if configKey == "" || !ok {
		return
	}
	for axis, label := range tags.embeddingTexts() {
		r.indexEmojiDescription(ctx, label, configKey, axis)
	}
}

func (r *Registry) indexEmojiDescription(ctx context.Context, label, configKey, axis string) {
	emojiSemanticIndexLock.Lock()
	defer emojiSemanticIndexLock.Unlock()
	space := emojiSemanticAxisKey(configKey, axis)
	embedding, err := r.semanticEmbedding(ctx, "emoji.index", "")
	if err != nil {
		return
	}
	if semanticConfigKey(embedding.resolved) != configKey {
		return
	}
	indexed, err := readEmojiSemanticIndexLabels(configKey, space)
	if err != nil {
		return
	}
	if _, ok := indexed[label]; ok {
		return
	}
	result, err := embedding.client.EmbedText(ctx, label)
	if err != nil {
		zap.L().Warn("表情包描述索引计算失败，下次维护时重试", zap.String("label", label), zap.Error(err))
		return
	}
	if len(result.Embedding) == 0 || !finiteVector(result.Embedding) || !nonZeroVector(result.Embedding) {
		zap.L().Warn("表情描述嵌入向量无效", zap.String("label", label))
		return
	}
	if r.semanticConfigKey(ctx) != configKey {
		return
	}
	modelKey := semanticModelKey(embedding.resolved, result.Embedding, result.Model, result.ModelIdentifier, result.APIProvider)
	if err := saveEmojiSemanticVector(configKey, label, modelKey, result.Embedding); err != nil {
		zap.L().Warn("表情包描述向量写入失败", zap.String("label", label), zap.Error(err))
	}
}

func finiteVector(vector []float64) bool {
	for _, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

func nonZeroVector(vector []float64) bool {
	for _, value := range vector {
		if value != 0 {
			return true
		}
	}
	return false
}

// ScheduleEmojiIndex 注册提交后异步排队索引，注册流程无需等待模型请求
func ScheduleEmojiIndex(emojiHash string) {
	emojiHash = strings.TrimSpace(emojiHash)
	if emojiHash == "" {
		return
	}
	registry := DefaultRegistry()
	if !registry.vlmTaskConfigured() && registry.semanticConfigKey(context.Background()) == "" {
		return
	}
	emojiSemanticPendingMu.Lock()
	if _, ok := emojiSemanticPending[emojiHash]; ok {
		emojiSemanticPendingMu.Unlock()
		return
	}
	emojiSemanticPending[emojiHash] = struct{}{}
	emojiSemanticPendingMu.Unlock()
	go func() {
		defer func() {
			emojiSemanticPendingMu.Lock()
			delete(emojiSemanticPending, emojiHash)
			emojiSemanticPendingMu.Unlock()
		}()
		ctx := context.Background()
		mgr := registry.Manager()
		if mgr == nil {
			return
		}
		emoji, err := mgr.GetByHash(ctx, emojiHash)
		if err != nil || emoji == nil {
			return
		}
		embedding, err := registry.semanticEmbedding(ctx, "emoji.index", "")
		if err != nil {
			return
		}
		registry.indexEmoji(ctx, emoji, semanticConfigKey(embedding.resolved))
	}()
}

// BackfillEmojiIndex 每轮只补一小批，失败条目下一轮轮转重试
func BackfillEmojiIndex() {
	registry := DefaultRegistry()
	ctx := context.Background()
	embedding, configErr := registry.semanticEmbedding(ctx, "emoji.index", "")
	configKey := ""
	if configErr == nil {
		configKey = semanticConfigKey(embedding.resolved)
	}
	canTag := registry.vlmTaskConfigured()
	if !canTag && configKey == "" {
		return
	}
	tagsByHash, err := readEmojiTagsMap("")
	if err != nil {
		zap.L().Warn("读取表情包分组标签失败", zap.Error(err))
		return
	}
	indexed := map[string]map[string]struct{}{}
	if configKey != "" {
		for _, axis := range []string{"emotion", "content"} {
			labels, err := readEmojiSemanticIndexLabels(configKey, emojiSemanticAxisKey(configKey, axis))
			if err != nil {
				return
			}
			indexed[axis] = labels
		}
	}
	mgr := registry.Manager()
	if mgr == nil {
		return
	}
	emojis := mgr.pool()
	sort.SliceStable(emojis, func(i, j int) bool { return emojis[i].FileHash < emojis[j].FileHash })
	if len(emojis) == 0 {
		return
	}
	emojiSemanticBackfillMu.Lock()
	cursor := emojiSemanticBackfillCursor % len(emojis)
	emojiSemanticBackfillMu.Unlock()
	attempted := 0
	for offset := 0; offset < len(emojis); offset++ {
		index := (cursor + offset) % len(emojis)
		emoji := emojis[index]
		tags, hasTags := tagsByHash[emoji.FileHash]
		needsVectors := false
		if hasTags && configKey != "" {
			for axis, label := range tags.embeddingTexts() {
				if _, ok := indexed[axis][label]; !ok {
					needsVectors = true
					break
				}
			}
		}
		if !((canTag && !hasTags) || needsVectors) {
			continue
		}
		registry.indexEmoji(ctx, &emoji, configKey)
		emojiSemanticBackfillMu.Lock()
		emojiSemanticBackfillCursor = cursor + offset + 1
		emojiSemanticBackfillMu.Unlock()
		attempted++
		if attempted >= emojiSemanticBackfillBatch {
			break
		}
	}
}

// MatchEmojiDescription 按情绪或画面描述匹配表情包，返回表情哈希
func MatchEmojiDescription(ctx context.Context, emotion, sessionID, content string) (string, error) {
	queries := map[string]string{}
	if strings.TrimSpace(emotion) != "" {
		queries["emotion"] = strings.TrimSpace(emotion)
	}
	if strings.TrimSpace(content) != "" {
		queries["content"] = strings.TrimSpace(content)
	}
	if len(queries) == 0 {
		return "", fmt.Errorf("请提供表情的情绪或画面内容")
	}
	registry := DefaultRegistry()
	embedding, err := registry.semanticEmbedding(ctx, "emoji.attach.semantic", sessionID)
	if err != nil {
		return "", err
	}
	configKey := semanticConfigKey(embedding.resolved)
	if configKey == "" {
		return "", fmt.Errorf("未配置表情语义匹配所需的 Embedding 模型")
	}
	tagsByHash, err := readEmojiTagsMap("")
	if err != nil {
		return "", err
	}
	mgr := registry.Manager()
	if mgr == nil {
		return "", fmt.Errorf("表情包管理器未初始化")
	}
	candidates := map[string]map[string]string{}
	for _, emoji := range mgr.pool() {
		tags, ok := tagsByHash[emoji.FileHash]
		if !ok {
			continue
		}
		candidates[emoji.FileHash] = tags.embeddingTexts()
	}
	axes := make([]string, 0, len(queries))
	for axis := range queries {
		axes = append(axes, axis)
	}
	sort.Strings(axes)
	for _, axis := range axes {
		labels, err := readEmojiSemanticIndexLabels(configKey, emojiSemanticAxisKey(configKey, axis))
		if err != nil {
			return "", err
		}
		for hash, values := range candidates {
			label := values[axis]
			if _, ok := labels[label]; !ok {
				delete(candidates, hash)
			}
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("当前没有完成所需维度向量索引的可用表情包")
	}
	type queryResult struct {
		axis   string
		result llm.EmbeddingResult
	}
	results := make([]queryResult, 0, len(axes))
	for _, axis := range axes {
		result, err := embedding.client.EmbedText(ctx, queries[axis])
		if err != nil {
			return "", err
		}
		results = append(results, queryResult{axis: axis, result: result})
	}
	if registry.semanticConfigKey(ctx) != configKey {
		return "", fmt.Errorf("表情语义匹配期间 Embedding 模型配置发生变化")
	}
	totals := make(map[string]float64, len(candidates))
	for hash := range candidates {
		totals[hash] = 0
	}
	for _, query := range results {
		modelKey := semanticModelKey(embedding.resolved, query.result.Embedding, query.result.Model,
			query.result.ModelIdentifier, query.result.APIProvider)
		vectors, err := readEmojiSemanticIndex(configKey, modelKey, false)
		if err != nil {
			return "", err
		}
		entries := make([][2]any, 0, len(totals))
		for hash := range totals {
			label := candidates[hash][query.axis]
			vector, ok := vectors[label]
			if !ok {
				continue
			}
			entries = append(entries, [2]any{hash, vector})
		}
		if len(entries) == 0 {
			return "", fmt.Errorf("当前 Embedding 模型空间中没有已索引的可用表情包")
		}
		scores, err := scoreEmojiCandidates(query.result.Embedding, entries)
		if err != nil {
			return "", err
		}
		for hash, score := range scores {
			totals[hash] += score
		}
	}
	bestHash := ""
	bestScore := math.Inf(-1)
	hashes := make([]string, 0, len(totals))
	for hash := range totals {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)
	for _, hash := range hashes {
		score := totals[hash] / float64(len(queries))
		if score > bestScore {
			bestScore = score
			bestHash = hash
		}
	}
	if bestHash == "" {
		return "", fmt.Errorf("当前 Embedding 模型空间中没有已索引的可用表情包")
	}
	return bestHash, nil
}

func scoreEmojiCandidates(query []float64, entries [][2]any) (map[string]float64, error) {
	queryNorm := vectorNorm(query)
	if math.IsNaN(queryNorm) || math.IsInf(queryNorm, 0) || queryNorm == 0 {
		return nil, fmt.Errorf("表情包语义匹配没有有效向量")
	}
	scores := map[string]float64{}
	for _, entry := range entries {
		hash, _ := entry[0].(string)
		vector, _ := entry[1].([]float64)
		norm := vectorNorm(vector)
		if len(vector) != len(query) || math.IsNaN(norm) || math.IsInf(norm, 0) || norm == 0 {
			continue
		}
		var dot float64
		for i := range query {
			dot += query[i] * vector[i]
		}
		scores[hash] = dot / (queryNorm * norm)
	}
	if len(scores) == 0 {
		return nil, fmt.Errorf("表情包语义匹配没有有效向量")
	}
	return scores, nil
}

func vectorNorm(vector []float64) float64 {
	var sum float64
	for _, value := range vector {
		sum += value * value
	}
	return math.Sqrt(sum)
}

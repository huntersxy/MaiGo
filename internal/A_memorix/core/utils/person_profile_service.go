package utils

// A_memorix 人物画像服务，对齐 Python core/utils/person_profile_service.py
// 主链路：person_id -> 别名 -> 关系与向量证据 -> 证据分桶画像 -> 版本化快照
// 存储、人物主档、检索与 LLM 均以接口注入，避免 utils 反向依赖 storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// 人物画像证据分类请求类型，对齐 PROFILE_CLASSIFICATION_REQUEST_TYPE
const PersonProfileClassificationRequestType = "A_Memorix.PersonProfileEvidenceClassify"

// PersonProfileGenerationVersion 画像生成版本，对齐 PROFILE_GENERATION_VERSION
const PersonProfileGenerationVersion = 3

// PersonProfilePersonInfo 人物主档，对齐 PersonInfo 表字段
// GroupCardname 与 MemoryPoints 保持原始值，由服务按 Python 规则解析
type PersonProfilePersonInfo struct {
	PersonID      string
	PersonName    string
	UserNickname  string
	GroupCardname any
	MemoryPoints  any
	UserID        string
}

// PersonProfileIdentitySource 人物主档读取能力，对齐 get_db_session 查询的 PersonInfo 部分
type PersonProfileIdentitySource interface {
	// GetPersonProfilePersonInfo 按 person_id 精确读取人物主档，不存在返回 nil
	GetPersonProfilePersonInfo(ctx context.Context, personID string) (*PersonProfilePersonInfo, error)
	// FindPersonProfileCandidates 按称呼模糊查询候选主档，精确匹配由服务完成
	FindPersonProfileCandidates(ctx context.Context, keyword string) ([]PersonProfilePersonInfo, error)
}

// PersonProfileStore 画像依赖的元数据存储能力
// 方法名与 storage.Store 的 Python 语义一致，由适配器完成结构体与字典互转
type PersonProfileStore interface {
	GetPersonIdentity(personID string) (map[string]any, error)
	FindPersonIdentities(keyword string) ([]map[string]any, error)
	GetPersonProfileAliasOverride(personID string) (map[string]any, error)
	GetPersonProfileOverride(personID string) (map[string]any, error)
	GetParagraphsByEntity(entityName string) ([]map[string]any, error)
	GetParagraphEntities(paragraphHash string) ([]map[string]any, error)
	GetRelations(subject string, object string, includeInactive bool) ([]map[string]any, error)
	GetParagraphsByRelationHashes(hashes []string) (map[string][]map[string]any, error)
	GetParagraph(hashValue string) (map[string]any, error)
	GetParagraphsBySource(source string) ([]map[string]any, error)
	SearchParagraphsByContent(contentQuery string) ([]map[string]any, error)
	ListPersonProfileFactClaims(personID string, effectiveAt *float64, limit int) ([]map[string]any, error)
	GetParagraphStaleRelationMarksBatch(paragraphHashes []string) (map[string][]map[string]any, error)
	GetRelationStatusBatch(hashes []string) (map[string]map[string]any, error)
	GetLatestPersonProfileSnapshot(personID string) (map[string]any, error)
	UpsertPersonProfileSnapshot(input PersonProfileSnapshotWrite) (map[string]any, error)
	RefreshPersonProfileSnapshotCache(snapshotID int64, expiresAt *float64, sourceNote string) (map[string]any, error)
}

// PersonProfileFactSupport 事实关系支撑校验，对齐 fact_relation_has_support
// 生产适配器必须实现，未实现时按“无支撑信息”保留关系属于降级路径
type PersonProfileFactSupport interface {
	FactRelationHasSupport(relationHash string, paragraph map[string]any) (bool, error)
}

// PersonProfileFactProjector 事实段落投影，对齐 project_fact_paragraph
type PersonProfileFactProjector interface {
	ProjectFactParagraph(paragraph map[string]any) (map[string]any, error)
}

// PersonProfileSnapshotWrite 画像快照写入参数，对齐 storage.PersonProfileSnapshotInput
type PersonProfileSnapshotWrite struct {
	PersonID            string
	ProfileText         string
	Aliases             []string
	RelationEdges       []map[string]any
	VectorEvidence      []map[string]any
	EvidenceIDs         []string
	FactClaimIDs        []string
	EvidenceFingerprint string
	ExpiresAt           *float64
	SourceNote          string
}

// PersonProfileRetrievedItem 向量召回结果，对齐检索返回项
type PersonProfileRetrievedItem struct {
	Hash       string
	ResultType string
	Score      float64
	Content    string
	Metadata   map[string]any
}

// PersonProfileRetriever 画像向量证据召回能力，对齐 DualPathRetriever.retrieve
type PersonProfileRetriever interface {
	RetrievePersonProfileEvidence(ctx context.Context, query string, topK int) ([]PersonProfileRetrievedItem, error)
}

// PersonProfileLLM 画像证据分类的文本生成能力
type PersonProfileLLM interface {
	GeneratePersonProfileClassification(ctx context.Context, prompt string, temperature float64, maxTokens int) (string, error)
}

// PersonProfileServiceOptions 画像服务依赖
type PersonProfileServiceOptions struct {
	Config         ConfigGetter
	Logger         Logger
	Store          PersonProfileStore
	Identity       PersonProfileIdentitySource
	Retriever      PersonProfileRetriever
	LLM            PersonProfileLLM
	ModelSignature func() map[string]any
	Now            func() float64
}

// PersonProfileService 人物画像聚合与刷新服务
type PersonProfileService struct {
	config         ConfigGetter
	logger         Logger
	store          PersonProfileStore
	identity       PersonProfileIdentitySource
	retriever      PersonProfileRetriever
	llm            PersonProfileLLM
	modelSignature func() map[string]any
	nowFunc        func() float64
}

// NewPersonProfileService 创建画像服务
func NewPersonProfileService(options PersonProfileServiceOptions) *PersonProfileService {
	service := &PersonProfileService{
		config:         options.Config,
		logger:         OrLogger(options.Logger),
		store:          options.Store,
		identity:       options.Identity,
		retriever:      options.Retriever,
		llm:            options.LLM,
		modelSignature: options.ModelSignature,
		nowFunc:        options.Now,
	}
	if service.nowFunc == nil {
		service.nowFunc = func() float64 {
			return float64(time.Now().UnixNano()) / 1e9
		}
	}
	return service
}

// Logger 返回服务日志器
func (s *PersonProfileService) Logger() Logger {
	return s.logger
}

// cfg 对齐 _cfg，按点号路径读取嵌套配置
func (s *PersonProfileService) cfg(key string, defaultValue any) any {
	if s.config == nil {
		return defaultValue
	}
	return s.config(key, defaultValue)
}

// profileClassificationMaxTokens 对齐 _profile_classification_max_tokens
func (s *PersonProfileService) profileClassificationMaxTokens() int {
	raw := s.cfg("person_profile.evidence_classification_max_tokens", 1200)
	value := 1200
	if isTruthy(raw) {
		if parsed, err := pyFloat64(raw); err == nil {
			value = int(parsed)
		}
	}
	if value < 128 {
		value = 128
	}
	if value > 32768 {
		value = 32768
	}
	return value
}

// ResolvePersonID 对齐 resolve_person_id，按 ID 或唯一的确切称呼解析
// 同名、子串匹配不能用来认领身份
func (s *PersonProfileService) ResolvePersonID(ctx context.Context, identifier string) (string, error) {
	key := pyStrip(identifier)
	if key == "" {
		return "", nil
	}

	candidates := map[string]struct{}{}
	resolved := false
	if s.identity != nil {
		record, err := s.identity.GetPersonProfilePersonInfo(ctx, key)
		if err != nil {
			s.logger.Warning(fmt.Sprintf("按别名解析 person_id 失败: identifier=%s, err=%v", key, err))
		} else if record != nil {
			if pyStrip(record.PersonID) == key {
				return pyStrip(record.PersonID), nil
			}
			if s.personInfoMatchesKeyword(record, key) {
				candidates[pyStrip(record.PersonID)] = struct{}{}
				resolved = true
			}
		}
		if !resolved {
			records, findErr := s.identity.FindPersonProfileCandidates(ctx, key)
			if findErr != nil {
				s.logger.Warning(fmt.Sprintf("按别名解析 person_id 失败: identifier=%s, err=%v", key, findErr))
			}
			for _, record := range records {
				if s.personInfoMatchesKeyword(&record, key) {
					candidates[pyStrip(record.PersonID)] = struct{}{}
				}
			}
		}
	}
	if s.store != nil {
		rows, err := s.store.FindPersonIdentities(key)
		if err != nil {
			s.logger.Warning(fmt.Sprintf("按别名解析 person_id 失败: identifier=%s, err=%v", key, err))
		}
		for _, row := range rows {
			personID := pyStrip(pyStr(row["person_id"]))
			if personID != "" {
				candidates[personID] = struct{}{}
			}
		}
	}
	if len(candidates) == 1 {
		for personID := range candidates {
			return personID, nil
		}
	}
	if len(candidates) > 1 {
		return "", nil
	}

	if len(key) == 32 && personProfileIsHex(key) {
		return pyLower(key), nil
	}
	return "", nil
}

// personInfoMatchesKeyword 对齐 Python 的精确称呼过滤
func (s *PersonProfileService) personInfoMatchesKeyword(record *PersonProfilePersonInfo, key string) bool {
	if record == nil || key == "" {
		return false
	}
	if record.PersonName == key || record.UserNickname == key {
		return true
	}
	for _, name := range ParsePersonProfileGroupNicks(record.GroupCardname) {
		if name == key {
			return true
		}
	}
	return false
}

// personProfileIsHex 判断是否为 32 位十六进制串
func personProfileIsHex(text string) bool {
	for _, char := range text {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

// ParsePersonProfileGroupNicks 对齐 _parse_group_nicks
func ParsePersonProfileGroupNicks(rawValue any) []string {
	if !isTruthy(rawValue) {
		return []string{}
	}
	var items []any
	switch typed := rawValue.(type) {
	case []any:
		items = typed
	case []string:
		items = make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
	case string:
		var payload any
		if err := json.Unmarshal([]byte(typed), &payload); err != nil {
			return []string{}
		}
		list, ok := payload.([]any)
		if !ok {
			return []string{}
		}
		items = list
	default:
		return []string{}
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		switch typed := item.(type) {
		case map[string]any:
			value := firstNonEmptyString(personProfileMapText(typed, "group_cardname"), personProfileMapText(typed, "group_nick_name"))
			if value != "" {
				names = append(names, value)
			}
		case string:
			if value := pyStrip(typed); value != "" {
				names = append(names, value)
			}
		}
	}
	return names
}

// ParsePersonProfileMemoryTraits 对齐 _parse_memory_traits
func ParsePersonProfileMemoryTraits(rawValue any) []string {
	if !isTruthy(rawValue) {
		return []string{}
	}
	var values []any
	switch typed := rawValue.(type) {
	case []any:
		values = typed
	case []string:
		values = make([]any, 0, len(typed))
		for _, item := range typed {
			values = append(values, item)
		}
	case string:
		var payload any
		if err := json.Unmarshal([]byte(typed), &payload); err != nil {
			return []string{}
		}
		list, ok := payload.([]any)
		if !ok {
			return []string{}
		}
		values = list
	default:
		return []string{}
	}
	traits := make([]string, 0, len(values))
	for _, item := range values {
		text := pyStrip(pyStr(item))
		if text == "" {
			continue
		}
		if strings.Contains(text, ":") {
			parts := strings.Split(text, ":")
			if len(parts) >= 3 {
				content := pyStrip(strings.Join(parts[1:len(parts)-1], ":"))
				if content != "" {
					traits = append(traits, content)
					continue
				}
			}
		}
		traits = append(traits, text)
	}
	if len(traits) > 10 {
		traits = traits[:10]
	}
	return traits
}

// collectAliasSuggestionsFromMemory 对齐 _collect_alias_suggestions_from_memory
// 收集与人物同段出现的实体，仅作为待人工确认的别名候选
func (s *PersonProfileService) collectAliasSuggestionsFromMemory(personID string, trustedAliases []string) []string {
	if personID == "" || s.store == nil {
		return []string{}
	}
	suggestions := make([]string, 0, 8)
	excluded := map[string]struct{}{}
	for _, item := range append([]string{personID}, trustedAliases...) {
		token := pyStrip(item)
		if token != "" {
			excluded[pyLower(token)] = struct{}{}
		}
	}
	seen := map[string]struct{}{}
	for key := range excluded {
		seen[key] = struct{}{}
	}

	order := make([]string, 0, 8)
	paragraphsByHash := map[string]map[string]any{}
	queryAliasKeys := map[string]struct{}{}
	for _, item := range trustedAliases {
		alias := pyStrip(item)
		aliasKey := pyLower(alias)
		if alias == "" || containsKey(queryAliasKeys, aliasKey) {
			continue
		}
		queryAliasKeys[aliasKey] = struct{}{}
		paragraphs, err := s.store.GetParagraphsByEntity(alias)
		if err != nil {
			s.logger.Warning(fmt.Sprintf("从记忆证据收集人物别名候选失败: person_id=%s, alias=%s, err=%v", personID, alias, err))
			continue
		}
		for _, paragraph := range paragraphs {
			paragraphHash := pyStrip(pyStr(paragraph["hash"]))
			if paragraphHash == "" {
				continue
			}
			if _, exists := paragraphsByHash[paragraphHash]; exists {
				continue
			}
			paragraphsByHash[paragraphHash] = paragraph
			order = append(order, paragraphHash)
		}
	}

	limit := len(order)
	if limit > 20 {
		limit = 20
	}
	for _, paragraphHash := range order[:limit] {
		entities, err := s.store.GetParagraphEntities(paragraphHash)
		if err != nil {
			entities = nil
		}
		for _, entity := range entities {
			name := pyStrip(pyStr(entity["name"]))
			if name == "" || name == personID {
				continue
			}
			key := pyLower(name)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			suggestions = append(suggestions, name)
		}
	}
	return suggestions
}

// derivedPersonAliases 对齐 _get_derived_person_aliases
func (s *PersonProfileService) derivedPersonAliases(ctx context.Context, personID string) ([]string, string, []string) {
	aliases := []string{}
	primaryName := ""
	memoryTraits := []string{}
	if personID == "" {
		return aliases, primaryName, memoryTraits
	}
	if s.store != nil {
		identity, err := s.store.GetPersonIdentity(personID)
		if err != nil {
			s.logger.Warning(fmt.Sprintf("解析人物别名失败: person_id=%s, err=%v", personID, err))
		} else if identity != nil {
			aliases = personProfileStringList(identity["aliases"])
			primaryName = pyStrip(personProfileMapText(identity, "canonical_name"))
		}
	}
	if s.identity == nil {
		if len(aliases) == 0 {
			aliases = []string{firstNonEmptyString(primaryName, personID)}
		}
		if primaryName == "" {
			primaryName = personID
		}
		return aliases, primaryName, memoryTraits
	}
	record, err := s.identity.GetPersonProfilePersonInfo(ctx, personID)
	if err != nil {
		s.logger.Warning(fmt.Sprintf("解析人物别名失败: person_id=%s, err=%v", personID, err))
	}
	if record == nil {
		if len(aliases) == 0 {
			aliases = []string{firstNonEmptyString(primaryName, personID)}
		}
		if primaryName == "" {
			primaryName = personID
		}
		return aliases, primaryName, memoryTraits
	}
	personName := pyStrip(record.PersonName)
	nickname := pyStrip(record.UserNickname)
	groupNicks := ParsePersonProfileGroupNicks(record.GroupCardname)
	memoryTraits = ParsePersonProfileMemoryTraits(record.MemoryPoints)
	if primaryName == "" {
		primaryName = firstNonEmptyString(personName, nickname)
		if primaryName == "" {
			for _, item := range groupNicks {
				if pyStrip(item) != "" {
					primaryName = pyStrip(item)
					break
				}
			}
		}
		if primaryName == "" {
			primaryName = pyStrip(record.UserID)
		}
		if primaryName == "" {
			primaryName = personID
		}
	}

	candidates := make([]string, 0, len(aliases)+len(groupNicks)+2)
	candidates = append(candidates, aliases...)
	candidates = append(candidates, personName, nickname)
	candidates = append(candidates, groupNicks...)
	unique := make([]string, 0, len(candidates))
	seen := map[string]struct{}{}
	for _, item := range candidates {
		norm := pyStrip(item)
		key := pyLower(norm)
		if norm == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, norm)
	}
	aliases = unique
	if len(aliases) == 0 {
		aliases = []string{firstNonEmptyString(primaryName, personID)}
		primaryName = firstNonEmptyString(primaryName, personID)
	}
	return aliases, primaryName, memoryTraits
}

// GetPersonAliasDetails 对齐 get_person_alias_details
func (s *PersonProfileService) GetPersonAliasDetails(ctx context.Context, personID string) map[string]any {
	token := pyStrip(personID)
	if token == "" {
		return map[string]any{
			"person_id":         "",
			"primary_name":      "",
			"derived_aliases":   []any{},
			"suggested_aliases": []any{},
			"manual_aliases":    []any{},
			"effective_aliases": []any{},
			"has_override":      false,
			"memory_traits":     []any{},
		}
	}
	derivedAliases, primaryName, memoryTraits := s.derivedPersonAliases(ctx, token)
	var override map[string]any
	if s.store != nil {
		value, err := s.store.GetPersonProfileAliasOverride(token)
		if err != nil {
			s.logger.Warning(fmt.Sprintf("读取人物别名覆盖失败: person_id=%s, err=%v", token, err))
		} else {
			override = value
		}
	}
	manualAliases := []string{}
	if override != nil {
		manualAliases = personProfileStringList(override["aliases"])
	}
	suggestionSource := append(append([]string{}, derivedAliases...), manualAliases...)
	suggestedAliases := s.collectAliasSuggestionsFromMemory(token, suggestionSource)
	effectiveAliases := derivedAliases
	if override != nil {
		effectiveAliases = manualAliases
	}
	payload := map[string]any{
		"person_id":         token,
		"primary_name":      primaryName,
		"derived_aliases":   personProfileAnySlice(derivedAliases),
		"suggested_aliases": personProfileAnySlice(suggestedAliases),
		"manual_aliases":    personProfileAnySlice(manualAliases),
		"effective_aliases": personProfileAnySlice(effectiveAliases),
		"has_override":      override != nil,
		"memory_traits":     personProfileAnySlice(memoryTraits),
		"override":          override,
	}
	return payload
}

// GetPersonAliases 对齐 get_person_aliases
func (s *PersonProfileService) GetPersonAliases(ctx context.Context, personID string) ([]string, string, []string) {
	details := s.GetPersonAliasDetails(ctx, personID)
	return personProfileStringList(details["effective_aliases"]),
		pyStr(details["primary_name"]),
		personProfileStringList(details["memory_traits"])
}

// personProfileStringList 把配置/字典值转成字符串列表
func personProfileStringList(value any) []string {
	switch typed := value.(type) {
	case nil:
		return []string{}
	case []string:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := pyStrip(item); trimmed != "" {
				result = append(result, trimmed)
			}
		}
		return result
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if !isTruthy(item) {
				continue
			}
			if trimmed := pyStrip(pyStr(item)); trimmed != "" {
				result = append(result, trimmed)
			}
		}
		return result
	default:
		if trimmed := pyStrip(pyStr(value)); trimmed != "" {
			return []string{trimmed}
		}
		return []string{}
	}
}

// personProfileAnySlice 输出 []any，便于与 Python 字典对齐
func personProfileAnySlice(values []string) []any {
	result := make([]any, 0, len(values))
	for _, item := range values {
		result = append(result, item)
	}
	return result
}

// firstNonEmptyString 返回第一个非空值
func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := pyStrip(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// containsKey 判断字符串键是否存在
func containsKey(keys map[string]struct{}, key string) bool {
	_, ok := keys[key]
	return ok
}

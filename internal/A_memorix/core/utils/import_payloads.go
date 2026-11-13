package utils

// A_memorix 导入载荷归一化，对齐 Python core/utils/import_payloads.py
// 知识类型解析由调用方注入存储层实现，避免 utils 反向依赖 storage

import (
	"fmt"
	"regexp"
	"strings"
)

// hashTokenPattern 对齐 _HASH_TOKEN_PATTERN
var hashTokenPattern = regexp.MustCompile(`^[0-9a-fA-F]+$`)

// entityNameKeys 对齐 _ENTITY_NAME_KEYS
var entityNameKeys = []string{"name", "label", "entity"}

// KnowledgeTypeResolver 注入 resolve_stored_knowledge_type，返回落库类型
type KnowledgeTypeResolver func(value any, content string) (string, error)

// ImportPayloadValidationError 导入负载校验异常，可用于上层按项跳过并记录告警
type ImportPayloadValidationError struct {
	Message string
	Code    string
	Field   string
	Value   string
}

// Error 实现 error 接口
func (e *ImportPayloadValidationError) Error() string { return e.Message }

// newImportPayloadError 构造校验异常
func newImportPayloadError(message string, code string, field string, value string) *ImportPayloadValidationError {
	return &ImportPayloadValidationError{Message: message, Code: code, Field: field, Value: value}
}

// ParagraphImportItem 归一化后的段落导入项，对齐 Python 返回的字典
type ParagraphImportItem struct {
	Content       string
	KnowledgeType string
	Source        string
	TimeMeta      map[string]any
	PersonIDs     []string
	Entities      []string
	Relations     []map[string]string
}

// IsProbableHashToken 判断文本是否疑似哈希值（hex 串，长度为 32/40/64）
func IsProbableHashToken(value any) bool {
	text := pyStrip(importValueText(value))
	switch len(text) {
	case 32, 40, 64:
		return hashTokenPattern.MatchString(text)
	default:
		return false
	}
}

// NormalizeEntityImportItem 标准化实体导入项，空值返回 false
func NormalizeEntityImportItem(item any) (string, bool) {
	name := ""
	switch typed := item.(type) {
	case string:
		name = pyStrip(typed)
	default:
		if object, ok := importObjectMap(item); ok {
			for _, key := range entityNameKeys {
				candidate := importValueText(object[key])
				if candidate != "" {
					name = candidate
					break
				}
			}
		}
	}
	if name == "" {
		return "", false
	}
	return name, true
}

// NormalizeRelationImportItem 标准化关系导入项，字段缺失返回 false
func NormalizeRelationImportItem(item any) (map[string]string, bool) {
	object, ok := importObjectMap(item)
	if !ok {
		return nil, false
	}
	subject := importValueText(object["subject"])
	predicate := importValueText(object["predicate"])
	objectValue := importValueText(object["object"])
	if subject == "" || predicate == "" || objectValue == "" {
		return nil, false
	}
	return map[string]string{"subject": subject, "predicate": predicate, "object": objectValue}, true
}

// NormalizeEntityImportList 对齐 _normalize_entities，按小写去重
func NormalizeEntityImportList(rawEntities any) []string {
	items, ok := rawEntities.([]any)
	if !ok {
		return []string{}
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		name, ok := NormalizeEntityImportItem(item)
		if !ok {
			continue
		}
		key := pyLowerRunes(name)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, name)
	}
	return out
}

// NormalizeRelationImportList 对齐 _normalize_relations
func NormalizeRelationImportList(rawRelations any) []map[string]string {
	items, ok := rawRelations.([]any)
	if !ok {
		return []map[string]string{}
	}
	out := make([]map[string]string, 0, len(items))
	for _, item := range items {
		relation, ok := NormalizeRelationImportItem(item)
		if !ok {
			continue
		}
		out = append(out, relation)
	}
	return out
}

// normalizePersonIDs 对齐 _normalize_person_ids
func normalizePersonIDs(raw any) ([]string, error) {
	if raw == nil {
		return []string{}, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, newImportPayloadError("段落 person_ids 必须为字符串数组", "paragraph_person_ids_invalid", "person_ids", "")
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		text, isString := item.(string)
		if !isString {
			return nil, newImportPayloadError("段落 person_ids 必须为字符串数组", "paragraph_person_ids_invalid", "person_ids", "")
		}
		personID := pyStrip(text)
		if personID == "" {
			continue
		}
		if _, exists := seen[personID]; exists {
			continue
		}
		seen[personID] = struct{}{}
		out = append(out, personID)
	}
	return out, nil
}

// NormalizeParagraphImportItem 归一化来自文本或 JSON 载荷的一条段落导入项
func NormalizeParagraphImportItem(item any, defaultSource string, resolve KnowledgeTypeResolver) (ParagraphImportItem, error) {
	if text, ok := item.(string); ok {
		if pyStrip(text) == "" {
			return ParagraphImportItem{}, newImportPayloadError("段落 content 不能为空", "paragraph_content_empty", "content", "")
		}
		if IsProbableHashToken(text) {
			return ParagraphImportItem{}, newImportPayloadError("段落 content 疑似哈希值，已跳过", "paragraph_content_hash_like", "content", text)
		}
		knowledgeType, err := resolveImportKnowledgeType(resolve, nil, text)
		if err != nil {
			return ParagraphImportItem{}, err
		}
		return ParagraphImportItem{
			Content:       text,
			KnowledgeType: knowledgeType,
			Source:        pyStrip(defaultSource),
			TimeMeta:      nil,
			PersonIDs:     []string{},
			Entities:      []string{},
			Relations:     []map[string]string{},
		}, nil
	}

	object, isObject := importObjectMap(item)
	if !isObject {
		return ParagraphImportItem{}, newImportPayloadError("段落项必须为字符串或包含 content 的对象", "paragraph_item_invalid", "content", "")
	}
	rawContent, hasContent := object["content"]
	if !hasContent {
		return ParagraphImportItem{}, newImportPayloadError("段落项必须为字符串或包含 content 的对象", "paragraph_item_invalid", "content", "")
	}
	content := ""
	if isTruthy(rawContent) {
		content = pyStr(rawContent)
	}
	if pyStrip(content) == "" {
		return ParagraphImportItem{}, newImportPayloadError("段落 content 不能为空", "paragraph_content_empty", "content", "")
	}
	if IsProbableHashToken(content) {
		return ParagraphImportItem{}, newImportPayloadError("段落 content 疑似哈希值，已跳过", "paragraph_content_hash_like", "content", content)
	}

	rawTimeMeta := map[string]any{
		"event_time":       object["event_time"],
		"event_time_start": object["event_time_start"],
		"event_time_end":   object["event_time_end"],
		"time_range":       object["time_range"],
		"time_granularity": object["time_granularity"],
		"time_confidence":  object["time_confidence"],
	}
	if nested, ok := object["time_meta"].(map[string]any); ok {
		for key, value := range nested {
			rawTimeMeta[key] = value
		}
	}

	knowledgeTypeRaw := object["knowledge_type"]
	if knowledgeTypeRaw == nil {
		knowledgeTypeRaw = object["type"]
	}
	knowledgeType, err := resolveImportKnowledgeType(resolve, knowledgeTypeRaw, content)
	if err != nil {
		return ParagraphImportItem{}, err
	}

	source := ""
	if isTruthy(object["source"]) {
		source = pyStrip(pyStr(object["source"]))
	}
	if source == "" {
		source = pyStrip(defaultSource)
	}

	normalizedTimeMeta, err := NormalizeTimeMeta(rawTimeMeta)
	if err != nil {
		return ParagraphImportItem{}, err
	}
	personIDs, err := normalizePersonIDs(object["person_ids"])
	if err != nil {
		return ParagraphImportItem{}, err
	}
	return ParagraphImportItem{
		Content:       content,
		KnowledgeType: knowledgeType,
		Source:        source,
		TimeMeta:      importEmptyToNil(normalizedTimeMeta),
		PersonIDs:     personIDs,
		Entities:      NormalizeEntityImportList(object["entities"]),
		Relations:     NormalizeRelationImportList(object["relations"]),
	}, nil
}

// NormalizeSummaryKnowledgeType 归一化由配置指定的摘要知识类型
func NormalizeSummaryKnowledgeType(value any, resolve KnowledgeTypeResolver) (string, error) {
	return resolveImportKnowledgeType(resolve, value, "")
}

// resolveImportKnowledgeType 调用注入解析器，未注入时按显式类型校验
func resolveImportKnowledgeType(resolve KnowledgeTypeResolver, value any, content string) (string, error) {
	if resolve != nil {
		return resolve(value, content)
	}
	if value == nil {
		return "", fmt.Errorf("knowledge_type 不能为空")
	}
	text := strings.ToLower(strings.TrimSpace(pyStr(value)))
	switch text {
	case "structured", "narrative", "factual", "quote", "mixed":
		return text, nil
	}
	return "", fmt.Errorf("非法 knowledge_type: %s", pyStr(value))
}

// importObjectMap 兼容 map[string]any 与 map[string]string
func importObjectMap(item any) (map[string]any, bool) {
	switch typed := item.(type) {
	case map[string]any:
		return typed, true
	case map[string]string:
		converted := make(map[string]any, len(typed))
		for key, value := range typed {
			converted[key] = value
		}
		return converted, true
	default:
		return nil, false
	}
}

// importValueText 对齐 str(value or "").strip()
func importValueText(value any) string {
	if !isTruthy(value) {
		return ""
	}
	return pyStrip(pyStr(value))
}

// importEmptyToNil 空字典返回 nil，对齐 Python 的 "非空才算 time_meta"
func importEmptyToNil(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	return values
}

// pyLowerRunes 对齐 Python str.lower() 的 ASCII 小写语义
func pyLowerRunes(text string) string {
	return pyLower(text)
}

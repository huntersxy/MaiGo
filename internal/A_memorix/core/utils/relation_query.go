package utils

// A_memorix 关系查询规格解析，对齐 Python core/utils/relation_query.py

import (
	"regexp"
	"strings"
)

// 关系查询解析错误，字符串与 Python 侧逐字对齐
const (
	// RelationQueryErrorEmpty 空查询
	RelationQueryErrorEmpty = "empty"
	// RelationQueryErrorInvalidPipeFormat 管道格式非法
	RelationQueryErrorInvalidPipeFormat = "invalid_pipe_format"
	// RelationQueryErrorInvalidArrowFormat 箭头格式非法
	RelationQueryErrorInvalidArrowFormat = "invalid_arrow_format"
)

// relationQueryNaturalLanguagePattern 对齐 _NATURAL_LANGUAGE_PATTERN
// 注意 Go RE2 的 \s 与 \b 为 ASCII 语义，Python 为 Unicode 语义
var relationQueryNaturalLanguagePattern = regexp.MustCompile(
	"(?i)(^\\s*(what|who|which|how|why|when|where)\\b|" +
		"\\?|？|" +
		"\\b(relation|related|between)\\b|" +
		"(什么关系|有哪些关系|之间|关联))",
)

// RelationQuerySpec 关系查询规格，对齐 Python dataclass RelationQuerySpec
// Subject/Predicate/Object 为 nil 表示 Python 的 None
type RelationQuerySpec struct {
	Raw          string
	IsStructured bool
	Subject      *string
	Predicate    *string
	Object       *string
	Error        string
}

// ParseRelationQuerySpec 对齐 parse_relation_query_spec
func ParseRelationQuerySpec(relationSpec string) RelationQuerySpec {
	raw := pyStrip(relationSpec)
	if raw == "" {
		return RelationQuerySpec{Raw: raw, Error: RelationQueryErrorEmpty}
	}

	if strings.Contains(raw, "|") {
		rawParts := strings.Split(raw, "|")
		parts := make([]string, len(rawParts))
		for index, part := range rawParts {
			parts[index] = pyStrip(part)
		}
		if len(parts) < 2 {
			return RelationQuerySpec{Raw: raw, IsStructured: true, Error: RelationQueryErrorInvalidPipeFormat}
		}
		spec := RelationQuerySpec{
			Raw:          raw,
			IsStructured: true,
			Subject:      optionalString(parts[0]),
			Predicate:    optionalString(parts[1]),
		}
		if len(parts) > 2 {
			spec.Object = optionalString(parts[2])
		}
		return spec
	}

	if strings.Contains(raw, "->") {
		parts := make([]string, 0, 3)
		for _, part := range strings.Split(raw, "->") {
			stripped := pyStrip(part)
			if stripped != "" {
				parts = append(parts, stripped)
			}
		}
		if len(parts) >= 3 {
			return RelationQuerySpec{
				Raw:          raw,
				IsStructured: true,
				Subject:      stringPointer(parts[0]),
				Predicate:    stringPointer(parts[1]),
				Object:       stringPointer(parts[2]),
			}
		}
		if len(parts) == 2 {
			return RelationQuerySpec{
				Raw:          raw,
				IsStructured: true,
				Subject:      stringPointer(parts[0]),
				Object:       stringPointer(parts[1]),
			}
		}
		return RelationQuerySpec{Raw: raw, IsStructured: true, Error: RelationQueryErrorInvalidArrowFormat}
	}

	if relationQueryLooksLikeNaturalLanguage(raw) {
		return RelationQuerySpec{Raw: raw}
	}

	// 仅保留低歧义的紧凑三元组作为兼容语法，例如 "Alice likes Apple"
	parts := strings.Fields(raw)
	if len(parts) == 3 {
		return RelationQuerySpec{
			Raw:          raw,
			IsStructured: true,
			Subject:      stringPointer(parts[0]),
			Predicate:    stringPointer(parts[1]),
			Object:       stringPointer(parts[2]),
		}
	}

	return RelationQuerySpec{Raw: raw}
}

// relationQueryLooksLikeNaturalLanguage 对齐 _looks_like_natural_language
func relationQueryLooksLikeNaturalLanguage(raw string) bool {
	text := pyStrip(raw)
	if text == "" {
		return false
	}
	return relationQueryNaturalLanguagePattern.FindStringIndex(text) != nil
}

// optionalString 对齐 "值 or None"
func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// stringPointer 返回字符串指针
func stringPointer(value string) *string {
	return &value
}

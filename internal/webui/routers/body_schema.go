package webuiapi

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// bodySchemaValidator 实现 Pydantic 2.x JSON Schema 的必要子集
// 只覆盖 MaiBot 1.3.19 路由真实使用的类型、约束与错误形状
type bodySchemaValidator struct {
	root map[string]any
}

func newBodySchemaValidator(root map[string]any) *bodySchemaValidator {
	return &bodySchemaValidator{root: root}
}

// validateBody 校验顶层请求体；Body 模型要求对象，dict 参数收到非对象时用 dict_type
func (v *bodySchemaValidator) validateBody(value any) []fastAPIValidationError {
	schema := v.resolve(v.root)
	if _, ok := value.(map[string]any); !ok {
		typeName := "model_attributes_type"
		message := "Input should be a valid dictionary or object to extract fields from"
		if isPlainDictSchema(schema) {
			typeName = "dict_type"
			message = "Input should be a valid dictionary"
		}
		return []fastAPIValidationError{{Type: typeName, Loc: []any{"body"}, Msg: message, Input: value}}
	}
	return v.validateValue(schema, value, []any{"body"})
}

func isPlainDictSchema(schema map[string]any) bool {
	if len(schema) == 0 {
		return false
	}
	if _, ok := schema["properties"]; ok {
		return false
	}
	if _, ok := schema["required"]; ok {
		return false
	}
	additional, ok := schema["additionalProperties"].(bool)
	return ok && additional
}

func (v *bodySchemaValidator) resolve(schema map[string]any) map[string]any {
	ref, ok := schema["$ref"].(string)
	if !ok || !strings.HasPrefix(ref, "#/") {
		return schema
	}
	current := v.root
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		next, ok := current[part].(map[string]any)
		if !ok {
			return schema
		}
		current = next
	}
	return current
}

func (v *bodySchemaValidator) validateValue(schema map[string]any, value any, loc []any) []fastAPIValidationError {
	schema = v.resolve(schema)
	if branches := schemaBranches(schema, "anyOf"); len(branches) > 0 {
		if errs, handled := v.validateAnyOf(branches, value, loc); handled {
			return errs
		}
	}
	if branches := schemaBranches(schema, "oneOf"); len(branches) > 0 {
		if errs, handled := v.validateAnyOf(branches, value, loc); handled {
			return errs
		}
	}
	if enum, ok := schema["enum"].([]any); ok {
		if !enumContains(enum, value) {
			expected := pydanticLiteralExpected(enum)
			return []fastAPIValidationError{{
				Type: "literal_error", Loc: loc, Msg: "Input should be " + expected, Input: value,
				Ctx: map[string]any{"expected": expected},
			}}
		}
		return nil
	}
	expectedType, _ := schema["type"].(string)
	if expectedType == "" {
		if _, ok := schema["properties"]; ok {
			expectedType = "object"
		}
	}
	switch expectedType {
	case "object":
		return v.validateObject(schema, value, loc)
	case "array":
		return v.validateArray(schema, value, loc)
	case "string":
		return v.validateString(schema, value, loc)
	case "integer":
		return v.validateInteger(schema, value, loc)
	case "number":
		return v.validateNumber(schema, value, loc)
	case "boolean":
		return v.validateBoolean(value, loc)
	case "null":
		if value != nil {
			return []fastAPIValidationError{{Type: "none_required", Loc: loc, Msg: "Input should be null", Input: value}}
		}
		return nil
	default:
		return nil
	}
}

func schemaBranches(schema map[string]any, key string) []map[string]any {
	raw, ok := schema[key].([]any)
	if !ok {
		return nil
	}
	branches := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		branch, ok := item.(map[string]any)
		if ok {
			branches = append(branches, branch)
		}
	}
	return branches
}

func (v *bodySchemaValidator) validateAnyOf(branches []map[string]any, value any, loc []any) ([]fastAPIValidationError, bool) {
	candidates := make([]map[string]any, 0, len(branches))
	for _, branch := range branches {
		resolved := v.resolve(branch)
		if branchType, _ := resolved["type"].(string); branchType == "null" {
			if value == nil {
				return nil, true
			}
			continue
		}
		candidates = append(candidates, branch)
	}
	if len(candidates) == 0 {
		if value == nil {
			return nil, true
		}
		return nil, false
	}
	var firstErrors []fastAPIValidationError
	var matchingErrors []fastAPIValidationError
	matchingFound := false
	for _, candidate := range candidates {
		errs := v.validateValue(candidate, value, loc)
		if len(errs) == 0 {
			return nil, true
		}
		if firstErrors == nil {
			firstErrors = errs
		}
		resolved := v.resolve(candidate)
		if !matchingFound && schemaTypeMatchesValue(resolved, value) {
			matchingErrors = errs
			matchingFound = true
		}
	}
	if matchingFound {
		return matchingErrors, true
	}
	return firstErrors, true
}

func schemaTypeMatchesValue(schema map[string]any, value any) bool {
	expected, _ := schema["type"].(string)
	switch expected {
	case "string":
		_, ok := value.(string)
		return ok
	case "integer":
		_, ok := numericValue(value)
		return ok
	case "number":
		_, ok := numericValue(value)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	}
	return false
}

func (v *bodySchemaValidator) validateObject(schema map[string]any, value any, loc []any) []fastAPIValidationError {
	object, ok := value.(map[string]any)
	if !ok {
		typeName := "model_attributes_type"
		message := "Input should be a valid dictionary or object to extract fields from"
		if isPlainDictSchema(schema) {
			typeName = "dict_type"
			message = "Input should be a valid dictionary"
		}
		return []fastAPIValidationError{{Type: typeName, Loc: loc, Msg: message, Input: value}}
	}
	properties, _ := schema["properties"].(map[string]any)
	required := schemaStringSet(schema, "required")
	errs := make([]fastAPIValidationError, 0)
	for _, name := range requestBodyFieldOrder(schema) {
		rawProperty := properties[name]
		property, ok := rawProperty.(map[string]any)
		if !ok {
			continue
		}
		fieldValue, present := object[name]
		if !present {
			if required[name] {
				errs = append(errs, fastAPIValidationError{
					Type: "missing", Loc: appendLoc(loc, name), Msg: "Field required", Input: object,
				})
			}
			continue
		}
		errs = append(errs, v.validateValue(property, fieldValue, appendLoc(loc, name))...)
	}
	if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
		extraNames := make([]string, 0)
		for name := range object {
			if _, exists := properties[name]; !exists {
				extraNames = append(extraNames, name)
			}
		}
		sort.Strings(extraNames)
		for _, name := range extraNames {
			errs = append(errs, fastAPIValidationError{
				Type: "extra_forbidden", Loc: appendLoc(loc, name), Msg: "Extra inputs are not permitted", Input: object[name],
			})
		}
	}
	return errs
}

func (v *bodySchemaValidator) validateArray(schema map[string]any, value any, loc []any) []fastAPIValidationError {
	items, ok := value.([]any)
	if !ok {
		return []fastAPIValidationError{{Type: "list_type", Loc: loc, Msg: "Input should be a valid list", Input: value}}
	}
	errs := make([]fastAPIValidationError, 0)
	if minimum, ok := schemaInt(schema, "minItems"); ok && len(items) < minimum {
		errs = append(errs, fastAPIValidationError{
			Type: "too_short", Loc: loc,
			Msg:   fmt.Sprintf("List should have at least %d items after validation, not %d", minimum, len(items)),
			Input: value, Ctx: map[string]any{"min_length": minimum, "actual_length": len(items)},
		})
	}
	if maximum, ok := schemaInt(schema, "maxItems"); ok && len(items) > maximum {
		errs = append(errs, fastAPIValidationError{
			Type: "too_long", Loc: loc,
			Msg:   fmt.Sprintf("List should have at most %d items after validation, not %d", maximum, len(items)),
			Input: value, Ctx: map[string]any{"max_length": maximum, "actual_length": len(items)},
		})
	}
	if rawItems, ok := schema["items"].(map[string]any); ok {
		for index, item := range items {
			errs = append(errs, v.validateValue(rawItems, item, appendLoc(loc, index))...)
		}
	}
	return errs
}

func (v *bodySchemaValidator) validateString(schema map[string]any, value any, loc []any) []fastAPIValidationError {
	text, ok := value.(string)
	if !ok {
		return []fastAPIValidationError{{Type: "string_type", Loc: loc, Msg: "Input should be a valid string", Input: value}}
	}
	length := utf8.RuneCountInString(text)
	errs := make([]fastAPIValidationError, 0)
	if minimum, ok := schemaInt(schema, "minLength"); ok && length < minimum {
		errs = append(errs, fastAPIValidationError{
			Type: "string_too_short", Loc: loc, Msg: stringLengthMessage("at least", minimum), Input: value,
			Ctx: map[string]any{"min_length": minimum},
		})
	}
	if maximum, ok := schemaInt(schema, "maxLength"); ok && length > maximum {
		errs = append(errs, fastAPIValidationError{
			Type: "string_too_long", Loc: loc, Msg: stringLengthMessage("at most", maximum), Input: value,
			Ctx: map[string]any{"max_length": maximum},
		})
	}
	if pattern, ok := schema["pattern"].(string); ok && pattern != "" {
		if compiled, err := regexp.Compile(pattern); err == nil && !compiled.MatchString(text) {
			errs = append(errs, fastAPIValidationError{
				Type: "string_pattern_mismatch", Loc: loc, Msg: "String should match pattern '" + pattern + "'", Input: value,
				Ctx: map[string]any{"pattern": pattern},
			})
		}
	}
	return errs
}

func stringLengthMessage(bound string, count int) string {
	unit := "characters"
	if count == 1 {
		unit = "character"
	}
	return fmt.Sprintf("String should have %s %d %s", bound, count, unit)
}

func (v *bodySchemaValidator) validateInteger(schema map[string]any, value any, loc []any) []fastAPIValidationError {
	numeric, ok := integerNumericValue(value)
	if !ok {
		switch typed := value.(type) {
		case string:
			if _, err := strconv.ParseInt(typed, 10, 64); err != nil {
				if _, floatErr := strconv.ParseFloat(typed, 64); floatErr != nil {
					return []fastAPIValidationError{{Type: "int_parsing", Loc: loc, Msg: "Input should be a valid integer, unable to parse string as an integer", Input: value}}
				}
				return nil
			}
		case json.Number:
			return []fastAPIValidationError{{Type: "int_from_float", Loc: loc, Msg: "Input should be a valid integer, got a number with a fractional part", Input: value}}
		case float64:
			if math.Trunc(typed) != typed {
				return []fastAPIValidationError{{Type: "int_from_float", Loc: loc, Msg: "Input should be a valid integer, got a number with a fractional part", Input: value}}
			}
		case int, int64, int32:
		default:
			return []fastAPIValidationError{{Type: "int_type", Loc: loc, Msg: "Input should be a valid integer", Input: value}}
		}
		return nil
	}
	return numberBoundErrors(schema, value, numeric, loc)
}

// integerNumericValue 把整数输入折算为数值用于范围比较，字符串先按整数解析
func integerNumericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		numeric, ok := numericValue(typed)
		if !ok || math.Trunc(numeric) != numeric {
			return 0, false
		}
		return numeric, true
	case string:
		if numeric, err := strconv.ParseInt(typed, 10, 64); err == nil {
			return float64(numeric), true
		}
		return 0, false
	case float64:
		if math.Trunc(typed) != typed {
			return 0, false
		}
		return typed, true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	default:
		return 0, false
	}
}

func (v *bodySchemaValidator) validateNumber(schema map[string]any, value any, loc []any) []fastAPIValidationError {
	numeric, ok := numericValue(value)
	if !ok {
		if _, isString := value.(string); isString {
			return []fastAPIValidationError{{Type: "float_parsing", Loc: loc, Msg: "Input should be a valid number, unable to parse string as a number", Input: value}}
		}
		return []fastAPIValidationError{{Type: "float_type", Loc: loc, Msg: "Input should be a valid number", Input: value}}
	}
	return numberBoundErrors(schema, value, numeric, loc)
}

// numberBoundErrors 按 JSON Schema 的上下界生成 Pydantic 范围错误
func numberBoundErrors(schema map[string]any, value any, numeric float64, loc []any) []fastAPIValidationError {
	errs := make([]fastAPIValidationError, 0)
	if minimum, ok := schemaFloat(schema, "minimum"); ok && numeric < minimum {
		errs = append(errs, fastAPIValidationError{
			Type: "greater_than_equal", Loc: loc, Msg: "Input should be greater than or equal to " + formatNumber(minimum), Input: value,
			Ctx: map[string]any{"ge": minimum},
		})
	}
	if maximum, ok := schemaFloat(schema, "maximum"); ok && numeric > maximum {
		errs = append(errs, fastAPIValidationError{
			Type: "less_than_equal", Loc: loc, Msg: "Input should be less than or equal to " + formatNumber(maximum), Input: value,
			Ctx: map[string]any{"le": maximum},
		})
	}
	if minimum, ok := schemaFloat(schema, "exclusiveMinimum"); ok && numeric <= minimum {
		errs = append(errs, fastAPIValidationError{
			Type: "greater_than", Loc: loc, Msg: "Input should be greater than " + formatNumber(minimum), Input: value,
			Ctx: map[string]any{"gt": minimum},
		})
	}
	if maximum, ok := schemaFloat(schema, "exclusiveMaximum"); ok && numeric >= maximum {
		errs = append(errs, fastAPIValidationError{
			Type: "less_than", Loc: loc, Msg: "Input should be less than " + formatNumber(maximum), Input: value,
			Ctx: map[string]any{"lt": maximum},
		})
	}
	return errs
}

func (v *bodySchemaValidator) validateBoolean(value any, loc []any) []fastAPIValidationError {
	switch typed := value.(type) {
	case bool:
		return nil
	case json.Number:
		if typed.String() == "0" || typed.String() == "1" {
			return nil
		}
		return []fastAPIValidationError{{Type: "bool_parsing", Loc: loc, Msg: "Input should be a valid boolean, unable to interpret input", Input: value}}
	case string:
		switch strings.ToLower(typed) {
		case "0", "1", "true", "false", "yes", "no", "on", "off", "y", "n", "t", "f":
			return nil
		}
		return []fastAPIValidationError{{Type: "bool_parsing", Loc: loc, Msg: "Input should be a valid boolean, unable to interpret input", Input: value}}
	default:
		return []fastAPIValidationError{{Type: "bool_type", Loc: loc, Msg: "Input should be a valid boolean", Input: value}}
	}
}

func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case string:
		if strings.TrimSpace(typed) != typed {
			return 0, false
		}
		number, err := strconv.ParseFloat(typed, 64)
		return number, err == nil
	}
	return 0, false
}

func schemaInt(schema map[string]any, key string) (int, bool) {
	switch typed := schema[key].(type) {
	case float64:
		return int(typed), true
	case json.Number:
		value, err := typed.Int64()
		return int(value), err == nil
	case int:
		return typed, true
	case int64:
		return int(typed), true
	}
	return 0, false
}

func schemaFloat(schema map[string]any, key string) (float64, bool) {
	switch typed := schema[key].(type) {
	case float64:
		return typed, true
	case json.Number:
		value, err := typed.Float64()
		return value, err == nil
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	}
	return 0, false
}

func schemaStringSet(schema map[string]any, key string) map[string]bool {
	result := make(map[string]bool)
	raw, ok := schema[key].([]any)
	if !ok {
		return result
	}
	for _, item := range raw {
		if text, ok := item.(string); ok {
			result[text] = true
		}
	}
	return result
}

func enumContains(enum []any, value any) bool {
	for _, item := range enum {
		if enumValuesEqual(item, value) {
			return true
		}
	}
	return false
}

func enumValuesEqual(expected any, actual any) bool {
	if expected == nil || actual == nil {
		return expected == nil && actual == nil
	}
	if expectedNumber, ok := numericValue(expected); ok {
		if actualNumber, ok := numericValue(actual); ok {
			return expectedNumber == actualNumber
		}
		return false
	}
	return fmt.Sprint(expected) == fmt.Sprint(actual)
}

func pydanticLiteralExpected(enum []any) string {
	parts := make([]string, 0, len(enum))
	for _, item := range enum {
		parts = append(parts, pydanticLiteralRepr(item))
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " or " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " or " + parts[len(parts)-1]
	}
}

func pydanticLiteralRepr(value any) string {
	switch typed := value.(type) {
	case string:
		return "'" + typed + "'"
	case nil:
		return "None"
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case json.Number:
		return typed.String()
	case float64:
		return formatNumber(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func formatNumber(value float64) string {
	if math.Trunc(value) == value && math.Abs(value) < 1e15 {
		return strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func appendLoc(loc []any, item any) []any {
	next := make([]any, 0, len(loc)+1)
	next = append(next, loc...)
	return append(next, item)
}

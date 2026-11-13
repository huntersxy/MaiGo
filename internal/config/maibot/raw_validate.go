package maibot

// 原始 TOML 字段约束校验：对齐 official_configs.py / model_configs.py 中
// Field(ge/le/gt/lt/min_length/max_length/pattern) 与 Literal 的加载期行为。
// 约束表由 scripts/gen_maibot_config_constraints.py 生成。

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type configConstraint struct {
	Path      string
	Kind      string
	Enum      []string
	Ge        *float64
	Le        *float64
	Gt        *float64
	Lt        *float64
	MinLength *int
	MaxLength *int
	Pattern   string
}

func num(value float64) *float64 { return &value }

func integer(value int) *int { return &value }

// ValidateRawBotConstraints 在反序列化后按 Python 字段约束校验 bot_config
func ValidateRawBotConstraints(raw []byte) error {
	return validateRawConstraints(raw, botConfigConstraints)
}

// ValidateRawModelConstraints 在反序列化后按 Python 字段约束校验 model_config
func ValidateRawModelConstraints(raw []byte) error {
	return validateRawConstraints(raw, modelConfigConstraints)
}

func validateRawConstraints(raw []byte, constraints []configConstraint) error {
	if len(constraints) == 0 {
		return nil
	}
	var document map[string]any
	if err := toml.Unmarshal(raw, &document); err != nil {
		return err
	}
	for _, constraint := range constraints {
		values := lookupConstraintValues(document, strings.Split(constraint.Path, "."))
		for _, value := range values {
			if err := checkConstraintValue(constraint, value); err != nil {
				return fmt.Errorf("配置项 %s %w", constraint.Path, err)
			}
		}
	}
	return nil
}

// lookupConstraintValues 按 TOML 路径取候选值；`[]` 表示数组元素展开
func lookupConstraintValues(current any, segments []string) []any {
	if len(segments) == 0 {
		return []any{current}
	}
	segment := segments[0]
	list := strings.HasSuffix(segment, "[]")
	key := strings.TrimSuffix(segment, "[]")
	mapping, ok := current.(map[string]any)
	if !ok {
		return nil
	}
	value, exists := mapping[key]
	if !exists {
		return nil
	}
	if !list {
		return lookupConstraintValues(value, segments[1:])
	}
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	values := make([]any, 0, len(items))
	for _, item := range items {
		values = append(values, lookupConstraintValues(item, segments[1:])...)
	}
	return values
}

func checkConstraintValue(constraint configConstraint, value any) error {
	if len(constraint.Enum) > 0 {
		text, ok := value.(string)
		if ok && !stringInConstraintEnum(text, constraint.Enum) {
			return fmt.Errorf("必须为 %s 之一，当前值 %q", formatConstraintEnum(constraint.Enum), text)
		}
	}
	if constraint.Pattern != "" {
		if text, ok := value.(string); ok {
			pattern, err := regexp.Compile(constraint.Pattern)
			if err == nil && !pattern.MatchString(text) {
				return fmt.Errorf("必须匹配正则 %q，当前值 %q", constraint.Pattern, text)
			}
		}
	}
	if text, ok := value.(string); ok {
		length := len([]rune(text))
		if constraint.MinLength != nil && length < *constraint.MinLength {
			return fmt.Errorf("长度必须至少为 %d，当前长度 %d", *constraint.MinLength, length)
		}
		if constraint.MaxLength != nil && length > *constraint.MaxLength {
			return fmt.Errorf("长度必须至多为 %d，当前长度 %d", *constraint.MaxLength, length)
		}
	}
	if constraint.Kind == "list" {
		if items, ok := value.([]any); ok {
			if constraint.MinLength != nil && len(items) < *constraint.MinLength {
				return fmt.Errorf("元素数量必须至少为 %d，当前 %d", *constraint.MinLength, len(items))
			}
			if constraint.MaxLength != nil && len(items) > *constraint.MaxLength {
				return fmt.Errorf("元素数量必须至多为 %d，当前 %d", *constraint.MaxLength, len(items))
			}
		}
	}
	if number, ok := constraintNumber(value); ok {
		if constraint.Ge != nil && number < *constraint.Ge {
			return fmt.Errorf("必须大于等于 %s，当前值 %s", formatConstraintNumber(*constraint.Ge), formatConstraintNumber(number))
		}
		if constraint.Le != nil && number > *constraint.Le {
			return fmt.Errorf("必须小于等于 %s，当前值 %s", formatConstraintNumber(*constraint.Le), formatConstraintNumber(number))
		}
		if constraint.Gt != nil && number <= *constraint.Gt {
			return fmt.Errorf("必须大于 %s，当前值 %s", formatConstraintNumber(*constraint.Gt), formatConstraintNumber(number))
		}
		if constraint.Lt != nil && number >= *constraint.Lt {
			return fmt.Errorf("必须小于 %s，当前值 %s", formatConstraintNumber(*constraint.Lt), formatConstraintNumber(number))
		}
	}
	return nil
}

func constraintNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	default:
		return 0, false
	}
}

func stringInConstraintEnum(value string, allowed []string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func formatConstraintEnum(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, strconv.Quote(value))
	}
	return strings.Join(quoted, "、")
}

func formatConstraintNumber(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

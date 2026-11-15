package host

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

// WebUI 协议常量，对齐 webui_schema.py
const (
	MaxWebUIDeclarationBytes  = 131072
	MaxWebUICustomDocBytes    = 2 * 1024 * 1024
	MaxWebUIDocumentsBytes    = 8 * 1024 * 1024
	MaxWebUIRegistrationBytes = 524288
	MaxWebUIArgumentBytes     = 65536
	MaxWebUIDeclarationDepth  = 32
	MaxWebUIArgumentDepth     = 12
)

var (
	webUIIdentifierPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)
	webUIAPINamePattern    = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]{0,127}$`)
	webUIFieldPattern      = regexp.MustCompile(`^(?:[a-zA-Z0-9_-]+(?:\.[a-zA-Z0-9_-]+)*)?$`)
	webUIHTTPPattern       = regexp.MustCompile(`^(?:https?)://`)
)

// WebUIRequiredCapabilities 当前协议支持的扩展能力
var WebUIRequiredCapabilities = []string{"declarative_basic", "custom_pages", "image_upload", "structured_arguments"}

// ValidateWebUIJSON 限制 JSON 深度、元素数与 UTF-8 体积
func ValidateWebUIJSON(value any, maxBytes, maxDepth int) error {
	if maxDepth <= 0 {
		maxDepth = MaxWebUIArgumentDepth
	}
	type item struct {
		value any
		depth int
	}
	pending := []item{{value: value}}
	count := 0
	for len(pending) > 0 {
		last := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		count++
		if last.depth > maxDepth || count > 20000 {
			return fmt.Errorf("JSON 超过 %d 层或 20000 个元素", maxDepth)
		}
		switch typed := last.value.(type) {
		case map[string]any:
			if count+len(pending)+len(typed) > 20000 {
				return fmt.Errorf("JSON 超过 20000 个元素")
			}
			for key, child := range typed {
				if strings.TrimSpace(key) == "" {
					return fmt.Errorf("JSON 对象的键必须为字符串")
				}
				pending = append(pending, item{child, last.depth + 1})
			}
		case []any:
			if count+len(pending)+len(typed) > 20000 {
				return fmt.Errorf("JSON 超过 20000 个元素")
			}
			for _, child := range typed {
				pending = append(pending, item{child, last.depth + 1})
			}
		case nil, string, bool, time.Time:
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		case []byte:
			// msgpack 会把 bytes 解成 []byte；按 base64 走 JSON 与 Python 侧一致
		default:
			return fmt.Errorf("仅接受 JSON 数据")
		}
	}
	raw, err := sonic.ConfigStd.Marshal(value)
	if err != nil {
		return fmt.Errorf("仅接受 JSON 数据")
	}
	if len(raw) > maxBytes {
		return fmt.Errorf("JSON 超过 %d KiB", maxBytes/1024)
	}
	return nil
}

// WebUIDataReference 数据上下文引用
type WebUIDataReference struct {
	Scope  string `json:"scope"`
	Source string `json:"source"`
	Field  string `json:"field"`
}

// WebUIParameter 声明式参数
type WebUIParameter struct {
	Type       string
	Required   bool
	MaxLength  int
	Minimum    *float64
	Maximum    *float64
	Choices    []any
	Items      *WebUIParameter
	Properties map[string]*WebUIParameter
	MaxItems   int
}

// Accepts 严格检查嵌套参数
func (p *WebUIParameter) Accepts(value any) bool {
	if p == nil {
		return false
	}
	switch p.Type {
	case "array":
		items, ok := value.([]any)
		if !ok || len(items) > p.MaxItems || p.Items == nil {
			return false
		}
		for _, itemValue := range items {
			if !p.Items.Accepts(itemValue) {
				return false
			}
		}
		return true
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		for key := range object {
			if _, declared := p.Properties[key]; !declared {
				return false
			}
		}
		for name, parameter := range p.Properties {
			child, present := object[name]
			if present {
				if !parameter.Accepts(child) {
					return false
				}
			} else if parameter.Required {
				return false
			}
		}
		return true
	}
	if len(p.Choices) > 0 {
		matched := false
		for _, choice := range p.Choices {
			if valuesEqual(choice, value) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	switch p.Type {
	case "string":
		text, ok := value.(string)
		return ok && len([]rune(text)) <= p.MaxLength
	case "integer":
		number, ok := numericValue(value)
		return ok && math.Trunc(number) == number && p.withinRange(number)
	case "number":
		number, ok := numericValue(value)
		return ok && p.withinRange(number)
	case "boolean":
		_, ok := value.(bool)
		return ok
	}
	return false
}

func (p *WebUIParameter) withinRange(value float64) bool {
	if p.Minimum != nil && value < *p.Minimum {
		return false
	}
	if p.Maximum != nil && value > *p.Maximum {
		return false
	}
	return true
}

func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case float32:
		number := float64(typed)
		return number, !math.IsNaN(number) && !math.IsInf(number, 0)
	}
	return 0, false
}

func valuesEqual(left, right any) bool {
	if leftNumber, ok := numericValue(left); ok {
		if rightNumber, rightOK := numericValue(right); rightOK {
			return leftNumber == rightNumber
		}
	}
	return fmt.Sprint(left) == fmt.Sprint(right) && fmt.Sprintf("%T", left) == fmt.Sprintf("%T", right)
}

func parseWebUIParameter(raw any, where string) (*WebUIParameter, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s 必须是对象", where)
	}
	parameter := &WebUIParameter{MaxLength: 4000, MaxItems: 1000, Properties: map[string]*WebUIParameter{}}
	parameterType, _ := object["type"].(string)
	switch parameterType {
	case "string", "integer", "number", "boolean", "array", "object":
		parameter.Type = parameterType
	default:
		return nil, fmt.Errorf("%s.type 非法", where)
	}
	if rawRequired, exists := object["required"]; exists {
		required, ok := rawRequired.(bool)
		if !ok {
			return nil, fmt.Errorf("%s.required 必须是布尔值", where)
		}
		parameter.Required = required
	}
	if rawMax, exists := object["max_length"]; exists {
		number, ok := webUIInt(rawMax)
		if !ok || number < 1 || number > 65536 {
			return nil, fmt.Errorf("%s.max_length 必须在 1 到 65536 之间", where)
		}
		parameter.MaxLength = number
	}
	if rawMin, exists := object["minimum"]; exists && rawMin != nil {
		number, ok := numericValue(rawMin)
		if !ok {
			return nil, fmt.Errorf("%s.minimum 必须是数字", where)
		}
		parameter.Minimum = &number
	}
	if rawMax, exists := object["maximum"]; exists && rawMax != nil {
		number, ok := numericValue(rawMax)
		if !ok {
			return nil, fmt.Errorf("%s.maximum 必须是数字", where)
		}
		parameter.Maximum = &number
	}
	if parameter.Minimum != nil && parameter.Maximum != nil && *parameter.Minimum > *parameter.Maximum {
		return nil, fmt.Errorf("参数 minimum 不能大于 maximum")
	}
	if rawItems, exists := object["items"]; exists && rawItems != nil {
		items, err := parseWebUIParameter(rawItems, where+".items")
		if err != nil {
			return nil, err
		}
		parameter.Items = items
	}
	if (parameter.Type == "array") != (parameter.Items != nil) {
		return nil, fmt.Errorf("array 参数必须声明 items；其它类型不能声明 items")
	}
	if rawProperties, exists := object["properties"]; exists && rawProperties != nil {
		properties, ok := rawProperties.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s.properties 必须是对象", where)
		}
		if len(properties) > 30 {
			return nil, fmt.Errorf("%s.properties 最多 30 个", where)
		}
		for name, child := range properties {
			if !webUIIdentifierPattern.MatchString(name) {
				return nil, fmt.Errorf("%s.properties 名称非法: %s", where, name)
			}
			parsed, err := parseWebUIParameter(child, where+".properties."+name)
			if err != nil {
				return nil, err
			}
			parameter.Properties[name] = parsed
		}
	}
	if parameter.Type != "object" && len(parameter.Properties) > 0 {
		return nil, fmt.Errorf("仅 object 参数可以声明 properties")
	}
	if rawMaxItems, exists := object["max_items"]; exists {
		number, ok := webUIInt(rawMaxItems)
		if !ok || number < 1 || number > 1000 {
			return nil, fmt.Errorf("%s.max_items 必须在 1 到 1000 之间", where)
		}
		parameter.MaxItems = number
	}
	if rawChoices, exists := object["choices"]; exists && rawChoices != nil {
		choices, ok := rawChoices.([]any)
		if !ok || len(choices) > 100 {
			return nil, fmt.Errorf("%s.choices 必须是不超过 100 项的列表", where)
		}
		if parameter.Type == "array" || parameter.Type == "object" {
			return nil, fmt.Errorf("结构化参数不能使用标量 choices")
		}
		parameter.Choices = choices
		for _, choice := range choices {
			if !parameter.Accepts(choice) {
				return nil, fmt.Errorf("参数 choices 必须符合声明类型和范围")
			}
		}
	}
	return parameter, nil
}

func webUIInt(raw any) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		if math.Trunc(value) == value {
			return int(value), true
		}
	}
	return 0, false
}

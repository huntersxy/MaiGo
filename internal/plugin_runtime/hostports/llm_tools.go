package hostports

import (
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

// parsePluginToolOptions 将插件侧工具定义转换为 Eino ToolInfo
// 支持 {name,description,parameters}、{name,description,parameters_schema}、
// OpenAI {type:"function",function:{...}} 与纯属性映射
func parsePluginToolOptions(raw []any) ([]*schema.ToolInfo, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	infos := make([]*schema.ToolInfo, 0, len(raw))
	for index, item := range raw {
		definition, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tools[%d] 必须是对象", index)
		}
		if toolType := strings.ToLower(strings.TrimSpace(asString(definition["type"]))); toolType == "function" {
			if function, ok := definition["function"].(map[string]any); ok {
				definition = function
			}
		}
		name := strings.TrimSpace(asString(definition["name"]))
		if name == "" {
			return nil, fmt.Errorf("tools[%d] 缺少 name", index)
		}
		description := strings.TrimSpace(asString(definition["description"]))
		if description == "" {
			description = "插件工具 " + name
		}
		parameters := firstNonNilValue(definition["parameters"], definition["parameters_schema"], definition["params"], definition["input_schema"])
		parameterSchema, err := normalizeToolParameterSchema(parameters)
		if err != nil {
			return nil, fmt.Errorf("工具 %s 参数结构不合法: %w", name, err)
		}
		infos = append(infos, &schema.ToolInfo{
			Name:        name,
			Desc:        description,
			ParamsOneOf: schema.NewParamsOneOfByJSONSchema(parameterSchema),
		})
	}
	return infos, nil
}

func firstNonNilValue(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func normalizeToolParameterSchema(raw any) (*jsonschema.Schema, error) {
	parameters := map[string]any{"type": "object", "properties": map[string]any{}, "required": []any{}}
	switch value := raw.(type) {
	case nil:
	case map[string]any:
		if _, hasType := value["type"]; hasType {
			parameters = value
		} else if _, hasProperties := value["properties"]; hasProperties {
			parameters = value
			parameters["type"] = "object"
		} else {
			parameters = map[string]any{"type": "object", "properties": value, "required": []any{}}
		}
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			break
		}
		var decoded map[string]any
		if err := sonic.UnmarshalString(trimmed, &decoded); err != nil {
			return nil, fmt.Errorf("参数 JSON 解析失败: %w", err)
		}
		parameters = decoded
	default:
		return nil, fmt.Errorf("参数必须是对象")
	}
	if parameters["type"] == nil {
		parameters["type"] = "object"
	}
	if parameters["properties"] == nil && asString(parameters["type"]) == "object" {
		parameters["properties"] = map[string]any{}
	}
	if parameters["required"] == nil && asString(parameters["type"]) == "object" {
		parameters["required"] = []any{}
	}
	encoded, err := sonic.Marshal(parameters)
	if err != nil {
		return nil, err
	}
	parsed := &jsonschema.Schema{}
	if err := sonic.Unmarshal(encoded, parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

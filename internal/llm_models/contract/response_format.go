package contract

// 结构化输出（ResponseFormat）
// 对等 MaiBot payload_content/resp_format.py 与各客户端的 _convert_response_format：
//   - nil → 不发送
//   - text → OpenAI 系不发送；Gemini 用 responseMimeType=text/plain
//   - json_object → {"type":"json_object"}；Gemini 用 responseMimeType=application/json
//   - json_schema → {"type":"json_schema","json_schema":{name,description?,schema,strict?}}；
//     Gemini 用 responseMimeType=application/json + responseJsonSchema=<schema>
// text 与 nil 在快照中必须可区分（RespFormat.to_dict 对 TEXT 输出 format_type=text）
//
// Go 侧没有 ResponseRequest，eino Options 也没有 response_format 字段：客户端按
// context 注入（WithResponseFormat，任务级优先）或 ModelConfig.ResponseFormat 读取。

import (
	"fmt"
	"strings"
)

type ResponseFormatSpec struct {
	// Kind 结构化输出类型：json_object / json_schema / text
	Kind string
	// Schema json_schema 模式下的 JSON Schema
	Schema map[string]any
}

// ParseResponseFormat 解析配置值；接受字符串简写或 MaiBot JsonSchema 包装对象
func ParseResponseFormat(value any) (ResponseFormatSpec, error) {
	switch typed := value.(type) {
	case nil:
		return ResponseFormatSpec{}, nil
	case string:
		return ParseResponseFormatKind(strings.TrimSpace(typed), nil)
	case map[string]any:
		kind := strings.ToLower(strings.TrimSpace(ChatScalarString(typed["type"])))
		if kind == "" {
			if _, ok := typed["json_schema"]; ok {
				kind = "json_schema"
			} else if _, ok := typed["schema"]; ok {
				kind = "json_schema"
			} else if _, ok := typed["name"]; ok {
				// 带 name 的包装对象按 json_schema 校验，便于报出缺少 schema 的错误
				kind = "json_schema"
			}
		}
		if kind == "json_schema" {
			// 已是 OpenAI 结构
			if raw, ok := typed["json_schema"].(map[string]any); ok {
				return ValidateResponseJSONSchema(raw)
			}
			return ValidateResponseJSONSchema(typed)
		}
		return ParseResponseFormatKind(kind, nil)
	default:
		return ResponseFormatSpec{}, fmt.Errorf("response_format 类型不支持: %T", value)
	}
}

func ParseResponseFormatKind(kind string, schema map[string]any) (ResponseFormatSpec, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "":
		return ResponseFormatSpec{}, nil
	case "text":
		return ResponseFormatSpec{Kind: "text"}, nil
	case "json_object":
		return ResponseFormatSpec{Kind: "json_object"}, nil
	case "json_schema":
		if schema == nil {
			return ResponseFormatSpec{}, fmt.Errorf("response_format=json_schema 时必须提供 schema 包装对象")
		}
		return ValidateResponseJSONSchema(schema)
	default:
		return ResponseFormatSpec{}, fmt.Errorf("不支持的响应格式: %s", kind)
	}
}

func ValidateResponseJSONSchema(wrapper map[string]any) (ResponseFormatSpec, error) {
	if wrapper == nil {
		return ResponseFormatSpec{}, fmt.Errorf("response_format=json_schema 时必须提供 schema 包装对象")
	}
	name := strings.TrimSpace(ChatScalarString(wrapper["name"]))
	if name == "" {
		return ResponseFormatSpec{}, fmt.Errorf("schema必须包含非空的 name 字段")
	}
	schema, ok := wrapper["schema"].(map[string]any)
	if !ok || schema == nil {
		return ResponseFormatSpec{}, fmt.Errorf("schema必须包含 schema 字段且为对象")
	}
	normalized := map[string]any{"name": name, "schema": schema}
	if description, ok := wrapper["description"].(string); ok && strings.TrimSpace(description) != "" {
		normalized["description"] = description
	}
	if strict, ok := wrapper["strict"].(bool); ok {
		normalized["strict"] = strict
	}
	return ResponseFormatSpec{Kind: "json_schema", Schema: normalized}, nil
}

// ChatResponseFormat 转换为 Chat Completions 的 response_format；nil 表示不发送
func ChatResponseFormat(spec ResponseFormatSpec) any {
	switch spec.Kind {
	case "json_object":
		return map[string]any{"type": "json_object"}
	case "json_schema":
		return map[string]any{"type": "json_schema", "json_schema": spec.Schema}
	default:
		return nil
	}
}

// ApplyGeminiResponseFormat 把结构化输出写入 generationConfig，对齐 _build_generation_config
func ApplyGeminiResponseFormat(generationConfig map[string]any, spec ResponseFormatSpec) {
	switch spec.Kind {
	case "text":
		generationConfig["responseMimeType"] = "text/plain"
	case "json_object":
		generationConfig["responseMimeType"] = "application/json"
	case "json_schema":
		generationConfig["responseMimeType"] = "application/json"
		if schema, ok := spec.Schema["schema"].(map[string]any); ok {
			generationConfig["responseJsonSchema"] = schema
		}
	}
}

// ChatScalarString 把任意值转成标量字符串
//
// 结构化输出里的类型字段可能是 string/数字/布尔，OpenAI 与 Gemini
// 都要求字符串形态；nil 返回空串。
func ChatScalarString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

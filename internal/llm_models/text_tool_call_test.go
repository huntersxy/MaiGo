package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func textToolTestSchemas() map[string]map[string]any {
	return map[string]map[string]any{
		"echo": {
			"type": "object",
			"properties": map[string]any{
				"text":  map[string]any{"type": "string"},
				"count": map[string]any{"type": "integer"},
				"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			"required": []any{"text"},
		},
	}
}

func textToolTestNumber(t *testing.T, value any) string {
	t.Helper()
	number, ok := value.(json.Number)
	if !ok {
		t.Fatalf("数值类型应为 json.Number，实际为 %T", value)
	}
	return number.String()
}

func TestParseTextToolCallsJSONContainer(t *testing.T) {
	text := "开头\n<tool_call>{\"name\":\"echo\",\"arguments\":{\"text\":\"hi\",\"count\":2}}</tool_call>\n结尾"
	parsed, err := ParseTextToolCalls(text, textToolTestSchemas(), true)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if parsed.Text != "开头\n\n结尾" {
		t.Fatalf("正文不符: %q", parsed.Text)
	}
	if len(parsed.Calls) != 1 {
		t.Fatalf("调用数量不符: %d", len(parsed.Calls))
	}
	call := parsed.Calls[0]
	if call.Name != "echo" || call.CallID != nil {
		t.Fatalf("调用信息不符: %#v", call)
	}
	if call.Arguments["text"] != "hi" || textToolTestNumber(t, call.Arguments["count"]) != "2" {
		t.Fatalf("参数不符: %#v", call.Arguments)
	}
}

func TestParseTextToolCallsJSONListAndFunctionWrapper(t *testing.T) {
	text := `<tool_call>[{"id":"call_a","function":{"name":"echo","arguments":"{\"text\":\"a\"}"}},{"name":"echo","input":{"text":"b"}}]</tool_call>`
	parsed, err := ParseTextToolCalls(text, textToolTestSchemas(), true)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if parsed.Text != "" {
		t.Fatalf("调用块应被移除: %q", parsed.Text)
	}
	if len(parsed.Calls) != 2 {
		t.Fatalf("调用数量不符: %d", len(parsed.Calls))
	}
	if parsed.Calls[0].CallID == nil || *parsed.Calls[0].CallID != "call_a" {
		t.Fatalf("显式调用 ID 未保留: %#v", parsed.Calls[0])
	}
	if parsed.Calls[1].CallID != nil || parsed.Calls[1].Arguments["text"] != "b" {
		t.Fatalf("第二调用不符: %#v", parsed.Calls[1])
	}
}

func TestParseTextToolCallsInvokeParameter(t *testing.T) {
	text := `<invoke name="echo"><parameter name="text">hi</parameter><parameter name="count">2</parameter></invoke>`
	parsed, err := ParseTextToolCalls(text, textToolTestSchemas(), true)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if parsed.Text != "" || len(parsed.Calls) != 1 {
		t.Fatalf("解析结果不符: %#v", parsed)
	}
	call := parsed.Calls[0]
	if call.Arguments["text"] != "hi" || textToolTestNumber(t, call.Arguments["count"]) != "2" {
		t.Fatalf("参数不符: %#v", call.Arguments)
	}
}

func TestParseTextToolCallsDirectTag(t *testing.T) {
	text := `<echo><text>hello</text><tags><item>a</item><item>b</item></tags></echo>`
	parsed, err := ParseTextToolCalls(text, textToolTestSchemas(), true)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(parsed.Calls) != 1 {
		t.Fatalf("调用数量不符: %d", len(parsed.Calls))
	}
	tags, ok := parsed.Calls[0].Arguments["tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "a" || tags[1] != "b" {
		t.Fatalf("数组参数不符: %#v", parsed.Calls[0].Arguments["tags"])
	}

	raw, err := ParseTextToolCalls(text, textToolTestSchemas(), false)
	if err != nil {
		t.Fatalf("禁止直接标签时不应报错: %v", err)
	}
	if len(raw.Calls) != 0 || raw.Text != text {
		t.Fatalf("禁止直接标签时应原样保留: %#v", raw)
	}
}

func TestParseTextToolCallsDSMLAndFunctionEquals(t *testing.T) {
	cases := []string{
		`<|DSML|tool_call><|DSML|invoke name="echo"><|DSML|parameter name="text">hi</|DSML|parameter></|DSML|invoke></|DSML|tool_call>`,
		`<tool_call><function=echo><parameter=text>hi</parameter></function></tool_call>`,
	}
	for _, text := range cases {
		parsed, err := ParseTextToolCalls(text, textToolTestSchemas(), true)
		if err != nil {
			t.Fatalf("解析失败 %q: %v", text, err)
		}
		if parsed.Text != "" || len(parsed.Calls) != 1 || parsed.Calls[0].Arguments["text"] != "hi" {
			t.Fatalf("解析结果不符: %#v", parsed)
		}
	}
}

func TestParseTextToolCallsSkipsCode(t *testing.T) {
	text := "```xml\n<tool_call>{\"name\":\"echo\",\"arguments\":{\"text\":\"fake\"}}</tool_call>\n```\n" +
		"<tool_call>{\"name\":\"echo\",\"arguments\":{\"text\":\"real\"}}</tool_call>"
	parsed, err := ParseTextToolCalls(text, textToolTestSchemas(), true)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(parsed.Calls) != 1 || parsed.Calls[0].Arguments["text"] != "real" {
		t.Fatalf("代码块外的调用未正确解析: %#v", parsed)
	}
	if !strings.Contains(parsed.Text, "fake") || strings.Contains(parsed.Text, "real") {
		t.Fatalf("代码块处理不符: %q", parsed.Text)
	}

	inline := "示例 `<tool_call>{\"name\":\"echo\",\"arguments\":{\"text\":\"x\"}}</tool_call>` 结束"
	raw, err := ParseTextToolCalls(inline, textToolTestSchemas(), true)
	if err != nil {
		t.Fatalf("内联代码不应报错: %v", err)
	}
	if len(raw.Calls) != 0 || raw.Text != inline {
		t.Fatalf("内联代码中的调用应被忽略: %#v", raw)
	}
}

func TestParseTextToolCallsRejectsBadArguments(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{
			name: "未定义参数",
			text: `<invoke name="echo"><parameter name="text">hi</parameter><parameter name="extra">1</parameter></invoke>`,
			want: "参数未定义: extra",
		},
		{
			name: "参数类型错误",
			text: `<invoke name="echo"><parameter name="text">hi</parameter><parameter name="count">bad</parameter></invoke>`,
			want: "文本工具调用解析失败",
		},
		{
			name: "未知工具",
			text: `<tool_call><function=missing><parameter=text>x</parameter></function></tool_call>`,
			want: "本次请求未提供工具: missing",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := ParseTextToolCalls(testCase.text, textToolTestSchemas(), true)
			if err == nil {
				t.Fatal("应返回解析错误")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("错误信息不符: %v", err)
			}
			if _, ok := err.(*TextToolCallError); !ok {
				t.Fatalf("错误类型不符: %T", err)
			}
		})
	}
}

func TestParseTextToolCallsKeepsUnknownDirectTags(t *testing.T) {
	text := `前文 <unknown-tool>内容</unknown-tool> 后文`
	parsed, err := ParseTextToolCalls(text, textToolTestSchemas(), true)
	if err != nil {
		t.Fatalf("普通未知标签不应报错: %v", err)
	}
	if len(parsed.Calls) != 0 || parsed.Text != text {
		t.Fatalf("普通未知标签应原样保留: %#v", parsed)
	}
}

func textToolTestToolInfo() []*schema.ToolInfo {
	return []*schema.ToolInfo{{
		Name: "echo",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"text": {Type: schema.String, Required: true},
		}),
	}}
}

func TestNormalizeTextToolResponseRecoversMessage(t *testing.T) {
	message := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.AssistantGenText{Text: `前文<tool_call>{"name":"echo","arguments":{"text":"hi"}}</tool_call>后文`}),
		},
	}
	normalized, err := NormalizeTextToolResponse(message, textToolTestToolInfo(), "", nil)
	if err != nil {
		t.Fatalf("恢复文本调用失败: %v", err)
	}
	var text string
	var call *schema.FunctionToolCall
	for _, block := range normalized.ContentBlocks {
		if block.AssistantGenText != nil {
			text += block.AssistantGenText.Text
		}
		if block.FunctionToolCall != nil {
			call = block.FunctionToolCall
		}
	}
	if text != "前文后文" {
		t.Fatalf("调用块未从正文移除: %q", text)
	}
	if call == nil || call.Name != "echo" || !strings.HasPrefix(call.CallID, "call_") {
		t.Fatalf("调用恢复不符: %#v", call)
	}
	if !strings.Contains(call.Arguments, `"hi"`) {
		t.Fatalf("调用参数不符: %s", call.Arguments)
	}

	_, err = NormalizeTextToolResponse(message, textToolTestToolInfo(), "none", nil)
	if err == nil || !strings.Contains(err.Error(), "禁止调用工具") {
		t.Fatalf("禁止工具时未报错: %v", err)
	}
}

func TestNormalizeTextToolResponseKeepsNativeCall(t *testing.T) {
	message := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.FunctionToolCall{CallID: "native-1", Name: "echo", Arguments: `{"text":"hi"}`}),
			schema.NewContentBlock(&schema.AssistantGenText{Text: `<tool_call>{"name":"echo","arguments":{"text":"hi"}}</tool_call>`}),
		},
	}
	normalized, err := NormalizeTextToolResponse(message, textToolTestToolInfo(), "", nil)
	if err != nil {
		t.Fatalf("原生调用一致时不应报错: %v", err)
	}
	if len(normalized.ContentBlocks) != 1 || normalized.ContentBlocks[0].FunctionToolCall == nil {
		t.Fatalf("重复文本调用应仅清理而不执行: %#v", normalized.ContentBlocks)
	}
}

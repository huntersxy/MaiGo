package llm

// 模型文本工具调用兼容层。
//
// 对齐 MaiBot src/llm_models/text_tool_call.py：支持 tool_call/tool_use/function_call/calls
// JSON 容器、invoke/parameter、function= 与 DSML，只处理代码引用以外的完整调用块。
// 未知工具、坏参数与不完整调用直接报错；调用块之外的正文原样保留。

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// TextToolCallError 文本已包含调用意图，但结构或参数不合法
type TextToolCallError struct {
	Message string
	Err     error
}

func (e *TextToolCallError) Error() string { return e.Message }

func (e *TextToolCallError) Unwrap() error { return e.Err }

// ParsedTextToolCall 尚未绑定上下文的调用；CallID 仅在模型明确给出时存在
type ParsedTextToolCall struct {
	Name      string
	Arguments map[string]any
	CallID    *string
}

// ParsedTextToolCalls 移除调用块后的正文和按出现顺序排列的调用
type ParsedTextToolCalls struct {
	Text  string
	Calls []ParsedTextToolCall
}

var (
	textToolCallContainers = map[string]struct{}{
		"tool_call": {}, "tool_use": {}, "function_call": {}, "calls": {}, "function_calls": {},
	}
	textToolCallWrappers = map[string]struct{}{
		"tool_call": {}, "tool_use": {}, "function_call": {}, "calls": {}, "function_calls": {},
		"invoke": {}, "function": {},
	}
)

// textToolCallTagPattern 对齐 _TAG：标签名后的 attrs 支持 `name="x"` 与 `=value`
var textToolCallTagPattern = regexp.MustCompile(
	`<(?P<close>/)?(?:[|｜]DSML[|｜]>?\s*)?(?P<name>[A-Za-z_][\p{L}\p{N}_.:-]*)(?P<attrs>(?:\s[^<>]*?|=[A-Za-z0-9_.:-]+)?)(?P<empty>/)?>`,
)

// textInlineCodePattern 对齐 _CODE 的内联代码分支
var textInlineCodePattern = regexp.MustCompile("`+[^`\\n]*`+")

type textTagMatch struct {
	Start int
	End   int
	Close bool
	Name  string
	Attrs string
	Empty bool
}

func textTagMatchFromIndexes(text string, indexes []int, offset int) textTagMatch {
	group := func(index int) string {
		start, end := indexes[2*index], indexes[2*index+1]
		if start < 0 {
			return ""
		}
		return text[start:end]
	}
	return textTagMatch{
		Start: offset + indexes[0],
		End:   offset + indexes[1],
		Close: indexes[2] >= 0,
		Name:  group(2),
		Attrs: group(3),
		Empty: indexes[8] >= 0,
	}
}

func textFindTag(text string, from int) *textTagMatch {
	if from >= len(text) {
		return nil
	}
	indexes := textToolCallTagPattern.FindStringSubmatchIndex(text[from:])
	if indexes == nil {
		return nil
	}
	match := textTagMatchFromIndexes(text[from:], indexes, from)
	return &match
}

func textMatchTagAt(text string, at int) *textTagMatch {
	if at < 0 || at >= len(text) {
		return nil
	}
	indexes := textToolCallTagPattern.FindStringSubmatchIndex(text[at:])
	if indexes == nil || indexes[0] != 0 {
		return nil
	}
	match := textTagMatchFromIndexes(text[at:], indexes, at)
	return &match
}

// normalizeTextToolTag 只规范化标签语法，不替换参数正文中的普通文本
func normalizeTextToolTag(match textTagMatch) string {
	name := match.Name
	lower := strings.ToLower(name)
	if _, ok := textToolCallWrappers[lower]; ok || lower == "parameter" {
		name = lower
	}
	attrs := match.Attrs
	if strings.HasPrefix(attrs, "=") && (name == "function" || name == "parameter") {
		attrs = ` name="` + attrs[1:] + `"`
	}
	close := ""
	if match.Close {
		close = "/"
	}
	empty := ""
	if match.Empty {
		empty = "/"
	}
	return "<" + close + name + attrs + empty + ">"
}

func textNormalizeRawTag(raw string) string {
	indexes := textToolCallTagPattern.FindStringSubmatchIndex(raw)
	if indexes == nil || indexes[0] != 0 {
		return raw
	}
	return normalizeTextToolTag(textTagMatchFromIndexes(raw, indexes, 0))
}

func textSkipSpace(text string, at int) int {
	for at < len(text) {
		r, size := utf8.DecodeRuneInString(text[at:])
		if !unicode.IsSpace(r) {
			break
		}
		at += size
	}
	return at
}

// textCodeRanges 对齐 _CODE：围栏代码块优先于内联代码，未闭合围栏延伸到文本末尾
func textCodeRanges(text string) [][2]int {
	ranges := make([][2]int, 0)
	position := 0
	for position < len(text) {
		fenceStart, fenceEnd, hasFence := textNextFence(text, position)
		inlineStart, inlineEnd := -1, -1
		if local := textInlineCodePattern.FindStringIndex(text[position:]); local != nil {
			inlineStart, inlineEnd = position+local[0], position+local[1]
		}
		if !hasFence && inlineStart < 0 {
			break
		}
		if hasFence && (inlineStart < 0 || fenceStart <= inlineStart) {
			ranges = append(ranges, [2]int{fenceStart, fenceEnd})
			position = fenceEnd
			continue
		}
		ranges = append(ranges, [2]int{inlineStart, inlineEnd})
		position = inlineEnd
	}
	return ranges
}

// textNextFence 从 from 起寻找下一个围栏代码块
func textNextFence(text string, from int) (int, int, bool) {
	lineStart := from
	if from > 0 && text[from-1] != '\n' {
		next := strings.IndexByte(text[from:], '\n')
		if next < 0 {
			return 0, 0, false
		}
		lineStart = from + next + 1
	}
	for lineStart < len(text) {
		cursor := lineStart
		for cursor < len(text) && (text[cursor] == ' ' || text[cursor] == '\t') {
			cursor++
		}
		if cursor < len(text) && (text[cursor] == '`' || text[cursor] == '~') {
			delimiterChar := text[cursor]
			delimiterEnd := cursor
			for delimiterEnd < len(text) && text[delimiterEnd] == delimiterChar {
				delimiterEnd++
			}
			if delimiterEnd-cursor >= 3 {
				newline := strings.IndexByte(text[delimiterEnd:], '\n')
				if newline < 0 {
					return 0, 0, false
				}
				delimiter := text[cursor:delimiterEnd]
				bodyStart := delimiterEnd + newline + 1
				bodyLine := bodyStart
				for bodyLine <= len(text) {
					closingStart := bodyLine
					for closingStart < len(text) && (text[closingStart] == ' ' || text[closingStart] == '\t') {
						closingStart++
					}
					if strings.HasPrefix(text[closingStart:], delimiter) {
						closingEnd := closingStart + len(delimiter)
						trailing := closingEnd
						for trailing < len(text) && (text[trailing] == ' ' || text[trailing] == '\t') {
							trailing++
						}
						if trailing == len(text) || text[trailing] == '\n' {
							return lineStart, trailing, true
						}
					}
					next := strings.IndexByte(text[bodyLine:], '\n')
					if next < 0 {
						break
					}
					bodyLine += next + 1
				}
				return lineStart, len(text), true
			}
		}
		next := strings.IndexByte(text[lineStart:], '\n')
		if next < 0 {
			break
		}
		lineStart += next + 1
	}
	return 0, 0, false
}

func textInCodeRanges(position int, ranges [][2]int) bool {
	for _, codeRange := range ranges {
		if codeRange[0] <= position && position < codeRange[1] {
			return true
		}
	}
	return false
}

// textXMLNode ElementTree 的等价节点：Text 是首个子标签前的文本，Tail 是父标签下后续文本
type textXMLNode struct {
	Tag      string
	Attrs    map[string]string
	Text     string
	Tail     string
	Children []*textXMLNode
}

func textXMLName(name xml.Name) string {
	if name.Space != "" {
		return name.Space + ":" + name.Local
	}
	return name.Local
}

// parseTextXML 用 RawToken 保留标签前缀，语义对齐 ElementTree.fromstring
func parseTextXML(block string) (*textXMLNode, error) {
	decoder := xml.NewDecoder(strings.NewReader(block))
	var (
		root      *textXMLNode
		stack     []*textXMLNode
		lastChild []*textXMLNode
	)
	for {
		token, err := decoder.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			node := &textXMLNode{Tag: textXMLName(value.Name), Attrs: make(map[string]string, len(value.Attr))}
			for _, attr := range value.Attr {
				name := textXMLName(attr.Name)
				if _, exists := node.Attrs[name]; exists {
					return nil, fmt.Errorf("属性重复: %s", name)
				}
				node.Attrs[name] = attr.Value
			}
			if len(stack) == 0 {
				if root != nil {
					return nil, fmt.Errorf("根元素后存在多余内容")
				}
				root = node
			} else {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, node)
				lastChild[len(lastChild)-1] = node
			}
			stack = append(stack, node)
			lastChild = append(lastChild, nil)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, fmt.Errorf("意外的结束标签: %s", textXMLName(value.Name))
			}
			if stack[len(stack)-1].Tag != textXMLName(value.Name) {
				return nil, fmt.Errorf("结束标签不匹配: %s", textXMLName(value.Name))
			}
			stack = stack[:len(stack)-1]
			lastChild = lastChild[:len(lastChild)-1]
		case xml.CharData:
			text := string(value)
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				if child := lastChild[len(lastChild)-1]; child != nil {
					child.Tail += text
				} else {
					top.Text += text
				}
			} else if root != nil {
				root.Tail += text
			} else if strings.TrimSpace(text) != "" {
				return nil, fmt.Errorf("根元素前存在文本")
			}
		case xml.Comment, xml.Directive, xml.ProcInst:
			return nil, fmt.Errorf("工具调用 XML 不允许声明或实体定义")
		}
	}
	if root == nil || len(stack) != 0 {
		return nil, fmt.Errorf("工具调用 XML 不完整")
	}
	if strings.TrimSpace(root.Tail) != "" {
		return nil, fmt.Errorf("根元素后存在多余内容")
	}
	return root, nil
}

func textReadCallBlock(text string, opening textTagMatch) (*textXMLNode, int, error) {
	if opening.Empty {
		node, err := parseTextXML(normalizeTextToolTag(opening))
		return node, opening.End, err
	}
	name := opening.Name
	_, wrapper := textToolCallWrappers[strings.ToLower(name)]
	sameName := func(tag textTagMatch) bool {
		if wrapper {
			return strings.EqualFold(tag.Name, name)
		}
		return tag.Name == name
	}

	bodyStart := textSkipSpace(text, opening.End)
	if bodyStart < len(text) && (text[bodyStart] == '{' || text[bodyStart] == '[') {
		bodyEnd, err := textJSONValueEnd(text, bodyStart)
		if err != nil {
			return nil, 0, err
		}
		closingStart := textSkipSpace(text, bodyEnd)
		closing := textMatchTagAt(text, closingStart)
		if closing == nil || !closing.Close || !sameName(*closing) {
			return nil, 0, fmt.Errorf("JSON 调用后缺少闭合标签: %s", opening.Name)
		}
		normalized := normalizeTextToolTag(opening)
		node, err := parseTextXML(normalized[:len(normalized)-1] + "/>")
		if err != nil {
			return nil, 0, err
		}
		node.Text = text[bodyStart:bodyEnd]
		return node, closing.End, nil
	}

	depth := 1
	for search := opening.End; search < len(text); {
		tag := textFindTag(text, search)
		if tag == nil {
			break
		}
		search = tag.End
		if sameName(*tag) {
			if tag.Close {
				depth--
			} else if !tag.Empty {
				depth++
			}
		}
		if depth != 0 {
			continue
		}
		block := text[opening.Start:tag.End]
		if strings.Contains(block, "<!") || strings.Contains(block, "<?") {
			return nil, 0, fmt.Errorf("工具调用 XML 不允许声明或实体定义")
		}
		node, err := parseTextXML(textToolCallTagPattern.ReplaceAllStringFunc(block, textNormalizeRawTag))
		if err != nil {
			return nil, 0, err
		}
		return node, tag.End, nil
	}
	return nil, 0, fmt.Errorf("工具调用标签未闭合: %s", opening.Name)
}

// textToolCallParser 缓存 Schema 编译结果；解析阶段只做一次
type textToolCallParser struct {
	schemas  map[string]map[string]any
	compiled map[string]*jsonschema.Schema
}

func newTextToolCallParser(schemas map[string]map[string]any) *textToolCallParser {
	return &textToolCallParser{schemas: schemas, compiled: map[string]*jsonschema.Schema{}}
}

func (p *textToolCallParser) validate(name string, arguments map[string]any) error {
	compiled, ok := p.compiled[name]
	if !ok {
		document := p.schemas[name]
		if document == nil {
			document = map[string]any{}
		}
		compiler := jsonschema.NewCompiler()
		resource := fmt.Sprintf("urn:maigo:text-tool-call:%d", len(p.compiled))
		if err := compiler.AddResource(resource, document); err != nil {
			return err
		}
		schemaValue, err := compiler.Compile(resource)
		if err != nil {
			return err
		}
		p.compiled[name] = schemaValue
		compiled = schemaValue
	}
	return compiled.Validate(arguments)
}

func textDecodeJSONValue(raw string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if strings.TrimSpace(raw[decoder.InputOffset():]) != "" {
		return nil, fmt.Errorf("JSON 值后存在多余内容")
	}
	return value, nil
}

func textJSONValueEnd(text string, start int) (int, error) {
	decoder := json.NewDecoder(strings.NewReader(text[start:]))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return 0, err
	}
	return start + int(decoder.InputOffset()), nil
}

func textSplitCallID(attributes map[string]string) *string {
	if value, ok := attributes["id"]; ok {
		return &value
	}
	if value, ok := attributes["call_id"]; ok {
		return &value
	}
	return nil
}

func textHasUnknownAttributes(attributes map[string]string, allowed ...string) string {
	for name := range attributes {
		found := false
		for _, candidate := range allowed {
			if name == candidate {
				found = true
				break
			}
		}
		if !found {
			return name
		}
	}
	return ""
}

func textAllChildrenTag(node *textXMLNode, tag string) bool {
	for _, child := range node.Children {
		if child.Tag != tag {
			return false
		}
	}
	return true
}

func textHasTextOutsideChildren(node *textXMLNode) bool {
	if strings.TrimSpace(node.Text) != "" {
		return true
	}
	for _, child := range node.Children {
		if strings.TrimSpace(child.Tail) != "" {
			return true
		}
	}
	return false
}

func textParseArgument(node *textXMLNode, schemaValue map[string]any, parser *textToolCallParser) (any, error) {
	text := node.Text
	if len(node.Children) > 0 {
		if textHasTextOutsideChildren(node) {
			return nil, fmt.Errorf("参数 %s 的子标签间不能混入正文", node.Tag)
		}
		typeName, _ := schemaValue["type"].(string)
		switch typeName {
		case "object":
			return textParseParameters(node, schemaValue, false, parser)
		case "array":
			for _, child := range node.Children {
				if child.Tag != "item" || len(child.Attrs) > 0 {
					return nil, fmt.Errorf("数组参数 %s 必须使用 item 子标签", node.Tag)
				}
			}
			itemsSchema, _ := schemaValue["items"].(map[string]any)
			if itemsSchema == nil {
				itemsSchema = map[string]any{}
			}
			items := make([]any, 0, len(node.Children))
			for _, child := range node.Children {
				item, err := textParseArgument(child, itemsSchema, parser)
				if err != nil {
					return nil, err
				}
				items = append(items, item)
			}
			return items, nil
		default:
			return nil, fmt.Errorf("参数 %s 不允许嵌套标签", node.Tag)
		}
	}
	if schemaValue["type"] == "string" || node.Attrs["string"] == "true" {
		return text, nil
	}
	value, err := textDecodeJSONValue(text)
	if err != nil {
		// 未指定类型或允许字符串的 Schema 可以接收普通文本，最终统一校验。
		return text, nil
	}
	return value, nil
}

func textParseParameters(node *textXMLNode, schemaValue map[string]any, namedParameters bool, parser *textToolCallParser) (map[string]any, error) {
	properties, _ := schemaValue["properties"].(map[string]any)
	arguments := make(map[string]any)
	for _, child := range node.Children {
		var name string
		if namedParameters {
			if child.Tag != "parameter" || textHasUnknownAttributes(child.Attrs, "name", "string") != "" {
				return nil, fmt.Errorf("invoke/function 必须使用 parameter name 标签")
			}
			name = child.Attrs["name"]
		} else {
			if len(child.Attrs) > 0 {
				return nil, fmt.Errorf("参数 %s 不允许属性", child.Tag)
			}
			name = child.Tag
		}
		if name == "" {
			return nil, fmt.Errorf("参数名缺失或重复: %s", name)
		}
		if _, exists := arguments[name]; exists {
			return nil, fmt.Errorf("参数名缺失或重复: %s", name)
		}

		var parameterSchema map[string]any
		if property, ok := properties[name]; ok {
			parameterSchema, _ = property.(map[string]any)
			if parameterSchema == nil {
				parameterSchema = map[string]any{}
			}
		} else {
			additional, exists := schemaValue["additionalProperties"]
			if !exists {
				additional = false
			}
			switch value := additional.(type) {
			case bool:
				if !value {
					return nil, fmt.Errorf("参数未定义: %s", name)
				}
				parameterSchema = map[string]any{}
			case map[string]any:
				if len(value) == 0 {
					return nil, fmt.Errorf("参数未定义: %s", name)
				}
				parameterSchema = value
			default:
				return nil, fmt.Errorf("参数未定义: %s", name)
			}
		}
		if strings.TrimSpace(child.Tail) != "" {
			return nil, fmt.Errorf("参数 %s 后存在未归属的正文", name)
		}
		value, err := textParseArgument(child, parameterSchema, parser)
		if err != nil {
			return nil, err
		}
		arguments[name] = value
	}
	return arguments, nil
}

func textCheckedCall(name any, arguments any, callID *string, parser *textToolCallParser) (ParsedTextToolCall, error) {
	nameValue, ok := name.(string)
	if !ok {
		return ParsedTextToolCall{}, fmt.Errorf("本次请求未提供工具: %v", name)
	}
	if _, ok := parser.schemas[nameValue]; !ok {
		return ParsedTextToolCall{}, fmt.Errorf("本次请求未提供工具: %s", nameValue)
	}
	if text, ok := arguments.(string); ok {
		value, err := textDecodeJSONValue(text)
		if err != nil {
			return ParsedTextToolCall{}, err
		}
		arguments = value
	}
	argumentsValue, ok := arguments.(map[string]any)
	if !ok {
		return ParsedTextToolCall{}, fmt.Errorf("工具 %s 的参数必须为对象", nameValue)
	}
	if callID != nil && strings.TrimSpace(*callID) == "" {
		return ParsedTextToolCall{}, fmt.Errorf("工具 %s 的调用 ID 必须为非空字符串", nameValue)
	}
	if err := parser.validate(nameValue, argumentsValue); err != nil {
		return ParsedTextToolCall{}, err
	}
	return ParsedTextToolCall{Name: nameValue, Arguments: argumentsValue, CallID: callID}, nil
}

func textJSONKey(payload map[string]any, key string) (any, bool) {
	value, ok := payload[key]
	return value, ok
}

func textParseJSONCall(payload any, parser *textToolCallParser) (ParsedTextToolCall, error) {
	object, ok := payload.(map[string]any)
	if !ok {
		return ParsedTextToolCall{}, fmt.Errorf("JSON 工具调用必须为对象")
	}
	functionValue, ok := object["function"]
	if !ok {
		functionValue = object
	}
	function, ok := functionValue.(map[string]any)
	if !ok {
		return ParsedTextToolCall{}, fmt.Errorf("function 必须为对象")
	}
	arguments, hasArguments := textJSONKey(function, "arguments")
	if !hasArguments {
		arguments, hasArguments = textJSONKey(function, "input")
	}
	if !hasArguments {
		arguments, hasArguments = textJSONKey(function, "parameters")
	}
	if !hasArguments {
		arguments = map[string]any{}
	}
	callIDValue, hasID := textJSONKey(object, "id")
	if !hasID {
		callIDValue, hasID = textJSONKey(object, "call_id")
	}
	var callID *string
	if hasID && callIDValue != nil {
		if text, ok := callIDValue.(string); ok {
			callID = &text
		} else {
			return ParsedTextToolCall{}, fmt.Errorf("工具调用 ID 必须为非空字符串")
		}
	}
	return textCheckedCall(function["name"], arguments, callID, parser)
}

func textParseXMLCall(node *textXMLNode, parser *textToolCallParser) ([]ParsedTextToolCall, error) {
	text := strings.TrimSpace(node.Text)
	if _, ok := textToolCallContainers[node.Tag]; ok {
		if unknown := textHasUnknownAttributes(node.Attrs, "id", "call_id"); unknown != "" {
			return nil, fmt.Errorf("%s 存在未知属性", node.Tag)
		}
		var calls []ParsedTextToolCall
		if len(node.Children) == 0 {
			payload, err := textDecodeJSONValue(text)
			if err != nil {
				return nil, err
			}
			payloads, ok := payload.([]any)
			if !ok {
				payloads = []any{payload}
			}
			if len(payloads) == 0 {
				return nil, fmt.Errorf("工具调用列表不能为空")
			}
			for _, item := range payloads {
				call, err := textParseJSONCall(item, parser)
				if err != nil {
					return nil, err
				}
				calls = append(calls, call)
			}
		} else if text != "" && textAllChildrenTag(node, "parameter") {
			if _, ok := parser.schemas[text]; !ok {
				return nil, fmt.Errorf("本次请求未提供工具: %s", text)
			}
			arguments, err := textParseParameters(node, parser.schemas[text], true, parser)
			if err != nil {
				return nil, err
			}
			call, err := textCheckedCall(text, arguments, nil, parser)
			if err != nil {
				return nil, err
			}
			calls = []ParsedTextToolCall{call}
		} else {
			if textHasTextOutsideChildren(node) {
				return nil, fmt.Errorf("%s 调用块中存在未归属的正文", node.Tag)
			}
			for _, child := range node.Children {
				nested, err := textParseXMLCall(child, parser)
				if err != nil {
					return nil, err
				}
				calls = append(calls, nested...)
			}
		}
		callID := textSplitCallID(node.Attrs)
		if callID != nil {
			if len(calls) != 1 || (calls[0].CallID != nil && *calls[0].CallID != *callID) {
				return nil, fmt.Errorf("外壳调用 ID 与内部调用不一致")
			}
			call, err := textCheckedCall(calls[0].Name, calls[0].Arguments, callID, parser)
			if err != nil {
				return nil, err
			}
			calls = []ParsedTextToolCall{call}
		}
		return calls, nil
	}

	namedParameters := node.Tag == "invoke" || node.Tag == "function"
	if namedParameters {
		if unknown := textHasUnknownAttributes(node.Attrs, "name", "id", "call_id"); unknown != "" {
			return nil, fmt.Errorf("%s 存在未知属性", node.Tag)
		}
	} else if unknown := textHasUnknownAttributes(node.Attrs, "id", "call_id"); unknown != "" {
		return nil, fmt.Errorf("%s 存在未知属性", node.Tag)
	}
	var name any = node.Tag
	if namedParameters {
		if value, ok := node.Attrs["name"]; ok {
			name = value
		} else {
			name = nil
		}
	}
	nameValue, ok := name.(string)
	if !ok {
		return nil, fmt.Errorf("本次请求未提供工具: %v", name)
	}
	schemaValue, ok := parser.schemas[nameValue]
	if !ok {
		return nil, fmt.Errorf("本次请求未提供工具: %s", nameValue)
	}
	var arguments any
	if len(node.Children) > 0 {
		if text != "" {
			return nil, fmt.Errorf("工具 %s 参数标签前存在正文", nameValue)
		}
		parsed, err := textParseParameters(node, schemaValue, namedParameters, parser)
		if err != nil {
			return nil, err
		}
		arguments = parsed
	} else if text != "" {
		value, err := textDecodeJSONValue(text)
		if err != nil {
			return nil, err
		}
		arguments = value
	} else {
		arguments = map[string]any{}
	}
	callID := textSplitCallID(node.Attrs)
	call, err := textCheckedCall(nameValue, arguments, callID, parser)
	if err != nil {
		return nil, err
	}
	return []ParsedTextToolCall{call}, nil
}

// ParseTextToolCalls 提取代码引用之外的完整调用块，保留其余正文；不生成或执行调用 ID。
//
// schemas 的键是本次可用工具名。显式调用外壳中的未知工具会报错，普通未知 XML 标签保持原样。
// 所有调用均解析并通过校验后才返回。
func ParseTextToolCalls(text string, schemas map[string]map[string]any, allowDirectTags bool) (ParsedTextToolCalls, error) {
	parsed, err := parseTextToolCalls(text, schemas, allowDirectTags)
	if err != nil {
		return ParsedTextToolCalls{Text: text}, &TextToolCallError{
			Message: "文本工具调用解析失败: " + err.Error(),
			Err:     err,
		}
	}
	return parsed, nil
}

func parseTextToolCalls(text string, schemas map[string]map[string]any, allowDirectTags bool) (ParsedTextToolCalls, error) {
	codeRanges := textCodeRanges(text)
	allowedNames := make(map[string]struct{}, len(textToolCallWrappers)+len(schemas))
	for name := range textToolCallWrappers {
		allowedNames[name] = struct{}{}
	}
	if allowDirectTags {
		for name := range schemas {
			allowedNames[name] = struct{}{}
		}
	}
	parser := newTextToolCallParser(schemas)
	calls := make([]ParsedTextToolCall, 0)
	parts := make([]string, 0)
	cursor := 0
	searchFrom := 0
	for searchFrom < len(text) {
		opening := textFindTag(text, searchFrom)
		if opening == nil {
			break
		}
		searchFrom = opening.End
		_, nameAllowed := allowedNames[opening.Name]
		_, wrapperName := textToolCallWrappers[strings.ToLower(opening.Name)]
		if opening.Close || (!nameAllowed && !wrapperName) || textInCodeRanges(opening.Start, codeRanges) {
			continue
		}
		node, end, err := textReadCallBlock(text, *opening)
		if err != nil {
			return ParsedTextToolCalls{}, err
		}
		parsedCalls, err := textParseXMLCall(node, parser)
		if err != nil {
			return ParsedTextToolCalls{}, err
		}
		calls = append(calls, parsedCalls...)
		parts = append(parts, text[cursor:opening.Start])
		cursor = end
		searchFrom = end
	}
	if len(calls) == 0 {
		return ParsedTextToolCalls{Text: text}, nil
	}
	parts = append(parts, text[cursor:])
	return ParsedTextToolCalls{Text: strings.TrimSpace(strings.Join(parts, "")), Calls: calls}, nil
}

// textToolCallSchemas 把 eino 工具定义转换成解析层使用的 JSON Schema
func textToolCallSchemas(tools []*schema.ToolInfo) (map[string]map[string]any, error) {
	schemas := make(map[string]map[string]any, len(tools))
	for _, tool := range tools {
		if tool == nil || strings.TrimSpace(tool.Name) == "" {
			continue
		}
		parameters := map[string]any{"type": "object", "properties": map[string]any{}}
		if tool.ParamsOneOf != nil {
			jsonSchema, err := tool.ParamsOneOf.ToJSONSchema()
			if err != nil {
				return nil, fmt.Errorf("工具 %q 参数 schema 无效: %w", tool.Name, err)
			}
			if jsonSchema != nil {
				encoded, err := json.Marshal(jsonSchema)
				if err != nil {
					return nil, err
				}
				var parsed map[string]any
				if err := json.Unmarshal(encoded, &parsed); err != nil {
					return nil, err
				}
				if len(parsed) > 0 {
					parameters = parsed
				}
			}
		}
		schemas[tool.Name] = parameters
	}
	return schemas, nil
}

const (
	textToolCallSourceExtraKey  = "tool_call_source"
	textToolCallSourceReasoning = "reasoning"
	textToolCallSourceResponse  = "response"
	textToolCallReplayExtraKey  = "provider_reasoning_replay"
	textToolCallIDPrefix        = "call_"
)

func textNativeCallArguments(raw string) (map[string]any, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}, true
	}
	value, err := textDecodeJSONValue(trimmed)
	if err != nil {
		return nil, false
	}
	arguments, ok := value.(map[string]any)
	return arguments, ok
}

func textNativeCallMatches(parsed ParsedTextToolCall, native *schema.FunctionToolCall) bool {
	if native == nil || parsed.Name != native.Name {
		return false
	}
	arguments, ok := textNativeCallArguments(native.Arguments)
	if !ok || !reflect.DeepEqual(parsed.Arguments, arguments) {
		return false
	}
	if parsed.CallID != nil && *parsed.CallID != native.CallID {
		return false
	}
	return true
}

func textRecoverCalls(
	parsed []ParsedTextToolCall,
	nativeCalls []*schema.FunctionToolCall,
	usedIDs map[string]struct{},
	toolChoice string,
	source string,
) ([]*schema.ContentBlock, error) {
	blocks := make([]*schema.ContentBlock, 0, len(parsed))
	for _, call := range parsed {
		if toolChoice == "none" {
			return nil, fmt.Errorf("本次请求禁止调用工具，但模型在文本中输出了工具调用")
		}
		if len(nativeCalls) > 0 {
			matched := false
			for _, native := range nativeCalls {
				if textNativeCallMatches(call, native) {
					matched = true
					break
				}
			}
			if !matched {
				return nil, fmt.Errorf("文本工具调用与原生工具调用不一致")
			}
			continue
		}
		callID := ""
		if call.CallID != nil {
			callID = *call.CallID
		} else {
			callID = textToolCallIDPrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
		if _, exists := usedIDs[callID]; exists {
			return nil, fmt.Errorf("工具调用 ID 重复: %s", callID)
		}
		usedIDs[callID] = struct{}{}
		encoded, err := json.Marshal(call.Arguments)
		if err != nil {
			return nil, err
		}
		block := schema.NewContentBlock(&schema.FunctionToolCall{
			CallID:    callID,
			Name:      call.Name,
			Arguments: string(encoded),
		})
		block.Extra = map[string]any{textToolCallSourceExtraKey: source}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

func textWithoutReplay(extra map[string]any) map[string]any {
	if extra == nil {
		return nil
	}
	if _, exists := extra[textToolCallReplayExtraKey]; !exists {
		return extra
	}
	copied := make(map[string]any, len(extra))
	for key, value := range extra {
		if key != textToolCallReplayExtraKey {
			copied[key] = value
		}
	}
	return copied
}

// RecoverTextToolCalls 将文本调用绑定为标准内容块；原生调用优先，重复文本仅清理而不执行。
//
// 不可用工具、禁止调用、非法参数及重复显式 ID 均返回 TextToolCallError。
func RecoverTextToolCalls(
	message *schema.AgenticMessage,
	tools []*schema.ToolInfo,
	toolChoice string,
	occupiedCallIDs []string,
) (*schema.AgenticMessage, error) {
	if message == nil {
		return nil, nil
	}
	schemas, err := textToolCallSchemas(tools)
	if err != nil {
		return nil, err
	}
	nativeCalls := make([]*schema.FunctionToolCall, 0)
	usedIDs := make(map[string]struct{}, len(occupiedCallIDs))
	for _, callID := range occupiedCallIDs {
		usedIDs[callID] = struct{}{}
	}
	for _, block := range message.ContentBlocks {
		if block == nil || block.FunctionToolCall == nil {
			continue
		}
		nativeCalls = append(nativeCalls, block.FunctionToolCall)
		if block.FunctionToolCall.CallID != "" {
			usedIDs[block.FunctionToolCall.CallID] = struct{}{}
		}
	}

	output := make([]*schema.ContentBlock, 0, len(message.ContentBlocks))
	changed := false
	for index := 0; index < len(message.ContentBlocks); {
		block := message.ContentBlocks[index]
		if block != nil && block.AssistantGenText != nil {
			end := index
			texts := make([]string, 0)
			for end < len(message.ContentBlocks) {
				next := message.ContentBlocks[end]
				if next == nil || next.AssistantGenText == nil {
					break
				}
				texts = append(texts, next.AssistantGenText.Text)
				end++
			}
			combined := strings.Join(texts, "")
			parsed, err := ParseTextToolCalls(combined, schemas, true)
			if err != nil {
				return nil, err
			}
			if len(parsed.Calls) == 0 {
				output = append(output, message.ContentBlocks[index:end]...)
				index = end
				continue
			}
			calls, err := textRecoverCalls(parsed.Calls, nativeCalls, usedIDs, toolChoice, textToolCallSourceResponse)
			if err != nil {
				return nil, &TextToolCallError{Message: err.Error(), Err: err}
			}
			changed = true
			if parsed.Text != "" {
				output = append(output, schema.NewContentBlock(&schema.AssistantGenText{Text: parsed.Text}))
			}
			output = append(output, calls...)
			index = end
			continue
		}
		if block != nil && block.Reasoning != nil && strings.TrimSpace(block.Reasoning.Text) != "" {
			parsed, err := ParseTextToolCalls(block.Reasoning.Text, schemas, false)
			if err != nil {
				return nil, err
			}
			if len(parsed.Calls) == 0 {
				output = append(output, block)
				index++
				continue
			}
			calls, err := textRecoverCalls(parsed.Calls, nativeCalls, usedIDs, toolChoice, textToolCallSourceReasoning)
			if err != nil {
				return nil, &TextToolCallError{Message: err.Error(), Err: err}
			}
			changed = true
			if parsed.Text != "" {
				copied := *block
				reasoning := *block.Reasoning
				reasoning.Text = parsed.Text
				copied.Reasoning = &reasoning
				copied.Extra = textWithoutReplay(block.Extra)
				output = append(output, &copied)
			}
			output = append(output, calls...)
			index++
			continue
		}
		output = append(output, block)
		index++
	}
	if !changed {
		return message, nil
	}
	copied := *message
	copied.ContentBlocks = output
	return &copied, nil
}

// NormalizeTextToolResponse 统一响应入口：恢复正文与推理中的文本工具调用
func NormalizeTextToolResponse(
	message *schema.AgenticMessage,
	tools []*schema.ToolInfo,
	toolChoice string,
	occupiedCallIDs []string,
) (*schema.AgenticMessage, error) {
	return RecoverTextToolCalls(message, tools, toolChoice, occupiedCallIDs)
}

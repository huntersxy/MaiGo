package host

import (
	"fmt"
	"strings"

	"MaiGo/internal/plugin_runtime/protocol"
)

// WebUIBinding API 绑定额，对齐 APIBinding
type WebUIBinding struct {
	API          string
	Version      string
	Parameters   map[string]*WebUIParameter
	Confirmation string
	Arguments    map[string]WebUIDataReference
}

// ValidateArgs 校验调用参数
func (b *WebUIBinding) ValidateArgs(args map[string]any) error {
	if b == nil {
		return fmt.Errorf("API 绑定不存在")
	}
	if err := ValidateWebUIJSON(args, MaxWebUIArgumentBytes, MaxWebUIArgumentDepth); err != nil {
		return err
	}
	for name := range args {
		if _, declared := b.Parameters[name]; !declared {
			return fmt.Errorf("未声明的参数: %s", name)
		}
	}
	for name, parameter := range b.Parameters {
		value, present := args[name]
		if !present {
			if parameter.Required {
				return fmt.Errorf("缺少参数: %s", name)
			}
			continue
		}
		if !parameter.Accepts(value) {
			return fmt.Errorf("参数不符合声明: %s", name)
		}
	}
	return nil
}

func parseWebUIBinding(raw any, where string, declarative bool) (*WebUIBinding, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s 必须是对象", where)
	}
	binding := &WebUIBinding{Version: "1", Parameters: map[string]*WebUIParameter{}, Arguments: map[string]WebUIDataReference{}}
	apiName, _ := object["api"].(string)
	if !webUIAPINamePattern.MatchString(apiName) {
		return nil, fmt.Errorf("%s.api 非法", where)
	}
	binding.API = apiName
	if rawVersion, exists := object["version"]; exists {
		version, ok := rawVersion.(string)
		if !ok || len(version) < 1 || len(version) > 40 {
			return nil, fmt.Errorf("%s.version 长度必须在 1 到 40 之间", where)
		}
		binding.Version = version
	}
	if rawParameters, exists := object["parameters"]; exists && rawParameters != nil {
		parameters, ok := rawParameters.(map[string]any)
		if !ok || len(parameters) > 30 {
			return nil, fmt.Errorf("%s.parameters 必须是不超过 30 项的对象", where)
		}
		for name, child := range parameters {
			if !webUIIdentifierPattern.MatchString(name) {
				return nil, fmt.Errorf("%s.parameters 名称非法: %s", where, name)
			}
			parsed, err := parseWebUIParameter(child, where+".parameters."+name)
			if err != nil {
				return nil, err
			}
			binding.Parameters[name] = parsed
		}
	}
	if rawConfirmation, exists := object["confirmation"]; exists && rawConfirmation != nil {
		confirmation, ok := rawConfirmation.(string)
		if !ok || confirmation == "" || len([]rune(confirmation)) > 80 {
			return nil, fmt.Errorf("%s.confirmation 长度必须在 1 到 80 之间", where)
		}
		binding.Confirmation = confirmation
	}
	if declarative {
		if rawArguments, exists := object["arguments"]; exists && rawArguments != nil {
			arguments, ok := rawArguments.(map[string]any)
			if !ok || len(arguments) > 30 {
				return nil, fmt.Errorf("%s.arguments 必须是不超过 30 项的对象", where)
			}
			for name, child := range arguments {
				if _, declared := binding.Parameters[name]; !declared {
					return nil, fmt.Errorf("arguments 必须对应已声明的参数")
				}
				reference, err := parseWebUIDataReference(child, where+".arguments."+name)
				if err != nil {
					return nil, err
				}
				binding.Arguments[name] = reference
			}
		}
		for _, parameter := range binding.Parameters {
			if parameter.Type == "array" || parameter.Type == "object" {
				return nil, fmt.Errorf("预置表单仅接受标量参数；结构化交互请使用 custom 页面")
			}
		}
	}
	return binding, nil
}

func parseWebUIDataReference(raw any, where string) (WebUIDataReference, error) {
	reference := WebUIDataReference{Scope: "query"}
	object, ok := raw.(map[string]any)
	if !ok {
		return reference, fmt.Errorf("%s 必须是对象", where)
	}
	if rawScope, exists := object["scope"]; exists {
		scope, ok := rawScope.(string)
		if !ok || (scope != "query" && scope != "selection") {
			return reference, fmt.Errorf("%s.scope 非法", where)
		}
		reference.Scope = scope
	}
	source, _ := object["source"].(string)
	if !webUIIdentifierPattern.MatchString(source) {
		return reference, fmt.Errorf("%s.source 非法", where)
	}
	reference.Source = source
	if rawField, exists := object["field"]; exists {
		field, ok := rawField.(string)
		if !ok || len(field) > 200 || !webUIFieldPattern.MatchString(field) {
			return reference, fmt.Errorf("%s.field 非法", where)
		}
		reference.Field = field
	}
	return reference, nil
}

// WebUIPage 校验后的页面声明
type WebUIPage struct {
	ID                 string
	Title              string
	Mode               string
	Placement          string
	Entry              string
	Document           string
	Queries            map[string]*WebUIBinding
	Actions            map[string]*WebUIBinding
	Uploads            map[string]string
	RequiredCapability []string
	Content            []any
}

// ValidateWebUIExtension 校验并规范化 WebUI 扩展声明
func ValidateWebUIExtension(raw map[string]any) (map[string]any, error) {
	if raw == nil {
		return nil, nil
	}
	if err := ValidateWebUIJSON(raw, MaxWebUIRegistrationBytes, MaxWebUIDeclarationDepth); err != nil {
		return nil, err
	}
	schemaVersion := 2
	if value, exists := raw["schema_version"]; exists {
		number, ok := webUIInt(value)
		if !ok || number != 2 {
			return nil, fmt.Errorf("WebUI 协议已升级为 2；请把插件 WebUI 声明的 schema_version 改为 2")
		}
		schemaVersion = number
	}
	capabilities := []string{}
	if rawCapabilities, exists := raw["required_capabilities"]; exists && rawCapabilities != nil {
		items, ok := rawCapabilities.([]any)
		if !ok || len(items) > 4 {
			return nil, fmt.Errorf("required_capabilities 必须是不超过 4 项的列表")
		}
		for _, item := range items {
			value, ok := item.(string)
			if !ok || !containsText(WebUIRequiredCapabilities, value) {
				return nil, fmt.Errorf("required_capabilities 含未知能力: %v", item)
			}
			capabilities = append(capabilities, value)
		}
	}
	pagesRaw, ok := raw["pages"].([]any)
	if !ok || len(pagesRaw) == 0 || len(pagesRaw) > 20 {
		return nil, fmt.Errorf("每插件需要 1 到 20 个页面")
	}
	workspaceTitle, _ := raw["workspace_title"].(string)
	seenIDs := map[string]struct{}{}
	totalDocuments := 0
	pages := make([]*WebUIPage, 0, len(pagesRaw))
	for index, rawPage := range pagesRaw {
		page, err := parseWebUIPage(rawPage, index)
		if err != nil {
			return nil, err
		}
		if _, exists := seenIDs[page.ID]; exists {
			return nil, fmt.Errorf("页面 ID 重复")
		}
		seenIDs[page.ID] = struct{}{}
		if page.Mode == "custom" {
			if !containsText(capabilities, "custom_pages") {
				return nil, fmt.Errorf("custom 页面需要声明 required_capabilities: custom_pages")
			}
			if len(page.Uploads) > 0 && !containsText(capabilities, "image_upload") {
				return nil, fmt.Errorf("图片上传需要声明 required_capabilities: image_upload")
			}
			totalDocuments += len(page.Document)
			if totalDocuments > MaxWebUIDocumentsBytes {
				return nil, fmt.Errorf("自定义页面产物总量超过 8 MiB")
			}
		}
		hasStructured := false
		for _, binding := range appendBindingMaps(page.Queries, page.Actions) {
			for _, parameter := range binding.Parameters {
				if parameter.Type == "array" || parameter.Type == "object" {
					hasStructured = true
				}
			}
		}
		if hasStructured && !containsText(capabilities, "structured_arguments") {
			return nil, fmt.Errorf("结构化参数需要声明 required_capabilities: structured_arguments")
		}
		pages = append(pages, page)
	}
	normalized := map[string]any{
		"schema_version":        schemaVersion,
		"required_capabilities": capabilities,
		"pages":                 pagesRaw,
	}
	if workspaceTitle != "" {
		normalized["workspace_title"] = workspaceTitle
	}
	return normalized, nil
}

// ValidateWebUIAPIBindings 校验 WebUI 页面只绑定本次注册中明确存在的静态 API
// 对齐 Python src/plugin_runtime/host/supervisor.py:1215-1226：
// declared_apis 只收 metadata.enabled 为真且非 dynamic 的 api 组件，键为 (name, str(version))
func ValidateWebUIAPIBindings(components []protocol.ComponentDeclaration, webui map[string]any) error {
	if webui == nil {
		return nil
	}
	declared := make(map[string]struct{})
	for _, component := range components {
		if !strings.EqualFold(strings.TrimSpace(component.ComponentType), "api") {
			continue
		}
		metadata := component.Metadata
		if enabled, exists := metadata["enabled"]; exists {
			if flag, ok := enabled.(bool); ok && !flag {
				continue
			}
		}
		if dynamic, exists := metadata["dynamic"]; exists {
			if flag, ok := dynamic.(bool); ok && flag {
				continue
			}
		}
		version := "1"
		if raw, exists := metadata["version"]; exists && raw != nil {
			version = fmt.Sprint(raw)
		}
		declared[strings.TrimSpace(component.Name)+"@"+version] = struct{}{}
	}
	// 只读取页面 id 与 queries/actions 的 api/version，不依赖完整页面结构校验
	pagesRaw, _ := webui["pages"].([]any)
	for _, rawPage := range pagesRaw {
		page, ok := rawPage.(map[string]any)
		if !ok {
			continue
		}
		pageID, _ := page["id"].(string)
		for _, scope := range []string{"queries", "actions"} {
			bindings, ok := page[scope].(map[string]any)
			if !ok {
				continue
			}
			for _, rawBinding := range bindings {
				binding, ok := rawBinding.(map[string]any)
				if !ok {
					continue
				}
				api, _ := binding["api"].(string)
				version := "1"
				if raw, exists := binding["version"]; exists && raw != nil {
					version = fmt.Sprint(raw)
				}
				key := strings.TrimSpace(api) + "@" + version
				if _, ok := declared[key]; !ok {
					return fmt.Errorf("WebUI 页面 %s 引用了未注册的静态 API: %s@%s", pageID, api, version)
				}
			}
		}
	}
	return nil
}

func appendBindingMaps(queries, actions map[string]*WebUIBinding) []*WebUIBinding {
	out := make([]*WebUIBinding, 0, len(queries)+len(actions))
	for _, binding := range queries {
		out = append(out, binding)
	}
	for _, binding := range actions {
		out = append(out, binding)
	}
	return out
}

func containsText(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

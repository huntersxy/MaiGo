package platformio

// NativeConfigSchema 构造与 maibot_sdk.generate_plugin_config_schema 同形的顶层 schema
func NativeConfigSchema(pluginID string, pluginInfo map[string]any, sections map[string]any) map[string]any {
	if pluginInfo == nil {
		pluginInfo = map[string]any{}
	}
	if sections == nil {
		sections = map[string]any{}
	}
	return map[string]any{
		"plugin_id":   pluginID,
		"plugin_info": pluginInfo,
		"sections":    sections,
		"layout": map[string]any{
			"type": "auto",
			"tabs": []any{},
		},
	}
}

// NativeSchemaSection 构造一个配置节
func NativeSchemaSection(name, title string, order int, fields map[string]any) map[string]any {
	if fields == nil {
		fields = map[string]any{}
	}
	return map[string]any{
		"name":        name,
		"title":       title,
		"description": "",
		"icon":        nil,
		"collapsed":   false,
		"order":       order,
		"fields":      fields,
	}
}

// NativeFieldOpts 字段可选元数据；零值表示未设置
type NativeFieldOpts struct {
	Label       string
	Description string
	Required    bool
	Hidden      bool
	Disabled    bool
	Widget      string
	InputType   string
	Choices     []any
	Min         any
	Max         any
	Step        any
	Placeholder string
	Hint        string
	ItemType    string
	Order       int
}

// NativeSchemaField 对齐 SDK _build_field_schema 的字段字典
func NativeSchemaField(name, fieldType string, defaultValue any, opts NativeFieldOpts) map[string]any {
	label := opts.Label
	if label == "" {
		label = name
	}
	uiType := opts.Widget
	if uiType == "" {
		switch fieldType {
		case "boolean":
			uiType = "switch"
		case "integer", "number":
			uiType = "number"
		case "array":
			uiType = "list"
		case "object":
			uiType = "json"
		default:
			if len(opts.Choices) > 0 {
				uiType = "select"
			} else {
				uiType = "input"
			}
		}
	}
	var choices any
	if opts.Choices == nil {
		choices = nil
	} else {
		choices = opts.Choices
	}
	var itemType any
	if opts.ItemType != "" {
		itemType = opts.ItemType
	}
	var inputType any
	if opts.InputType != "" {
		inputType = opts.InputType
	}
	var placeholder any
	if opts.Placeholder != "" {
		placeholder = opts.Placeholder
	}
	var hint any
	if opts.Hint != "" {
		hint = opts.Hint
	}
	return map[string]any{
		"name":         name,
		"type":         fieldType,
		"default":      defaultValue,
		"description":  opts.Description,
		"required":     opts.Required,
		"choices":      choices,
		"min":          opts.Min,
		"max":          opts.Max,
		"step":         opts.Step,
		"pattern":      nil,
		"max_length":   nil,
		"label":        label,
		"placeholder":  placeholder,
		"hint":         hint,
		"icon":         nil,
		"hidden":       opts.Hidden,
		"disabled":     opts.Disabled,
		"order":        opts.Order,
		"input_type":   inputType,
		"ui_type":      uiType,
		"rows":         3,
		"group":        nil,
		"depends_on":   nil,
		"depends_value": nil,
		"item_type":    itemType,
		"item_fields":  nil,
		"min_items":    nil,
		"max_items":    nil,
		"example":      nil,
	}
}

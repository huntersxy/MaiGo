package host

import (
	"fmt"
	"strings"
)

func parseWebUIPage(raw any, index int) (*WebUIPage, error) {
	where := fmt.Sprintf("pages[%d]", index)
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s 必须是对象", where)
	}
	page := &WebUIPage{Mode: "declarative", Placement: "sidebar", Queries: map[string]*WebUIBinding{}, Actions: map[string]*WebUIBinding{}, Uploads: map[string]string{}}
	page.ID, _ = object["id"].(string)
	if !webUIIdentifierPattern.MatchString(page.ID) {
		return nil, fmt.Errorf("%s.id 非法", where)
	}
	page.Title, _ = object["title"].(string)
	if len([]rune(page.Title)) < 1 || len([]rune(page.Title)) > 80 {
		return nil, fmt.Errorf("%s.title 长度必须在 1 到 80 之间", where)
	}
	if description, ok := object["description"].(string); ok && len([]rune(description)) > 4000 {
		return nil, fmt.Errorf("%s.description 超过 4000 字", where)
	}
	page.Mode, _ = object["mode"].(string)
	if page.Mode == "" {
		page.Mode = "declarative"
	}
	if page.Mode != "declarative" && page.Mode != "custom" {
		return nil, fmt.Errorf("%s.mode 非法", where)
	}
	if placement, ok := object["placement"].(string); ok && placement != "" {
		if placement != "sidebar" && placement != "workspace" {
			return nil, fmt.Errorf("%s.placement 非法", where)
		}
		// 旧插件的顶栏页面统一进入侧栏
		page.Placement = "sidebar"
	}
	for scope, target := range map[string]map[string]*WebUIBinding{"queries": page.Queries, "actions": page.Actions} {
		rawBindings, exists := object[scope]
		if !exists || rawBindings == nil {
			continue
		}
		bindings, ok := rawBindings.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s.%s 必须是对象", where, scope)
		}
		limit := 10
		if scope == "actions" {
			limit = 20
		}
		if len(bindings) > limit {
			return nil, fmt.Errorf("%s.%s 最多 %d 项", where, scope, limit)
		}
		for name, child := range bindings {
			if !webUIIdentifierPattern.MatchString(name) {
				return nil, fmt.Errorf("%s.%s 名称非法: %s", where, scope, name)
			}
			declarative := page.Mode == "declarative"
			binding, err := parseWebUIBinding(child, where+"."+scope+"."+name, declarative)
			if err != nil {
				return nil, err
			}
			target[name] = binding
		}
	}
	if page.Mode == "custom" {
		page.Entry, _ = object["entry"].(string)
		if page.Entry == "" || len(page.Entry) > 200 {
			return nil, fmt.Errorf("%s.entry 长度必须在 1 到 200 之间", where)
		}
		if strings.Contains(page.Entry, "..") || strings.HasPrefix(page.Entry, "/") || strings.HasPrefix(page.Entry, "\\") {
			return nil, fmt.Errorf("%s.entry 必须是插件内的相对 HTML 路径", where)
		}
		page.Document, _ = object["document"].(string)
		if strings.TrimSpace(page.Document) == "" || len(page.Document) > MaxWebUICustomDocBytes {
			return nil, fmt.Errorf("自定义页面必须非空且不超过 2 MiB")
		}
		for name, binding := range page.Queries {
			if binding.Confirmation != "" {
				return nil, fmt.Errorf("queries 不声明 confirmation")
			}
			_ = name
		}
		rawUploads, exists := object["uploads"]
		if exists && rawUploads != nil {
			uploads, ok := rawUploads.(map[string]any)
			if !ok || len(uploads) > 10 {
				return nil, fmt.Errorf("%s.uploads 最多 10 项", where)
			}
			for name, rawUpload := range uploads {
				if !webUIIdentifierPattern.MatchString(name) {
					return nil, fmt.Errorf("%s.uploads 名称非法: %s", where, name)
				}
				uploadObject, ok := rawUpload.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("%s.uploads.%s 必须是对象", where, name)
				}
				action, _ := uploadObject["action"].(string)
				if !webUIIdentifierPattern.MatchString(action) {
					return nil, fmt.Errorf("%s.uploads.%s.action 非法", where, name)
				}
				binding := page.Actions[action]
				parameter := (*WebUIParameter)(nil)
				if binding != nil {
					parameter = binding.Parameters["upload_id"]
				}
				if parameter == nil || parameter.Type != "string" || !parameter.Required {
					return nil, fmt.Errorf("图片上传必须绑定接受必填字符串 upload_id 的 action")
				}
				page.Uploads[name] = action
			}
		}
		return page, nil
	}
	pollInterval := 0
	if rawPoll, exists := object["poll_interval_seconds"]; exists {
		number, ok := webUIInt(rawPoll)
		if !ok || number < 0 || number > 60 {
			return nil, fmt.Errorf("%s.poll_interval_seconds 必须在 0 到 60 之间", where)
		}
		pollInterval = number
	}
	if pollInterval == 1 || pollInterval == 2 {
		return nil, fmt.Errorf("轮询间隔至少3秒；0表示关闭")
	}
	content, ok := object["content"].([]any)
	if !ok || len(content) == 0 || len(content) > 50 {
		return nil, fmt.Errorf("%s.content 需要 1 到 50 个组件", where)
	}
	page.Content = content
	validator := &webUIPageValidator{page: page, inputs: map[string]struct{}{}, selections: map[string]struct{}{}, dialogs: map[string]struct{}{}}
	for index, node := range content {
		if err := validator.walk(node, 1, true, index); err != nil {
			return nil, err
		}
	}
	if validator.count > 200 {
		return nil, fmt.Errorf("页面超过 8 层或 200 个组件")
	}
	return page, nil
}

type webUIPageValidator struct {
	page       *WebUIPage
	inputs     map[string]struct{}
	selections map[string]struct{}
	dialogs    map[string]struct{}
	count      int
}

func (v *webUIPageValidator) walk(raw any, depth int, topLevel bool, index int) error {
	object, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("页面组件必须是对象")
	}
	v.count++
	if depth > 8 || v.count > 200 {
		return fmt.Errorf("页面超过 8 层或 200 个组件")
	}
	nodeType, _ := object["type"].(string)
	switch nodeType {
	case "":
		return fmt.Errorf("页面组件缺少 type")
	}
	if label, ok := object["label"].(string); ok && len([]rune(label)) > 80 {
		return fmt.Errorf("组件 label 超过 80 字")
	}
	if rawWhen, exists := object["when"]; exists && rawWhen != nil {
		if err := v.checkCondition(rawWhen); err != nil {
			return err
		}
	}
	if rawDisabled, exists := object["disabled_when"]; exists && rawDisabled != nil {
		if err := v.checkCondition(rawDisabled); err != nil {
			return err
		}
	}
	children := object["children"].([]any)
	switch nodeType {
	case "stack", "card", "grid", "tabs", "collapsible", "dialog":
		if len(children) > 50 {
			return fmt.Errorf("children 最多 50 个")
		}
		if nodeType == "tabs" {
			if len(children) == 0 {
				return fmt.Errorf("tabs 子节点必须有 label")
			}
			for _, child := range children {
				childObject, ok := child.(map[string]any)
				if !ok || strings.TrimSpace(stringValue(childObject["label"])) == "" {
					return fmt.Errorf("tabs 子节点必须有 label")
				}
			}
		}
		if nodeType == "grid" {
			columns, _ := webUIInt(object["columns"])
			if columns < 1 || columns > 4 {
				return fmt.Errorf("grid.columns 必须在 1 到 4 之间")
			}
		}
		if nodeType == "dialog" {
			name, _ := object["name"].(string)
			if !topLevel || depth != 1 {
				return fmt.Errorf("dialog 必须位于 content 顶层且名称唯一")
			}
			if _, exists := v.dialogs[name]; exists || name == "" {
				return fmt.Errorf("dialog 必须位于 content 顶层且名称唯一")
			}
			v.dialogs[name] = struct{}{}
		}
		for childIndex, child := range children {
			if err := v.walk(child, depth+1, false, childIndex); err != nil {
				return err
			}
		}
		return nil
	case "text", "stat", "progress", "image", "table", "chart":
		if reference, ok := object["value"].(map[string]any); ok {
			parsed, err := parseWebUIDataReference(reference, "value")
			if err != nil {
				return err
			}
			if err := v.checkReference(parsed); err != nil {
				return err
			}
		}
		if nodeType == "table" {
			columns, ok := object["columns"].([]any)
			if !ok || len(columns) == 0 || len(columns) > 20 {
				return fmt.Errorf("table.columns 需要 1 到 20 列")
			}
			if selection, ok := object["selection"].(string); ok && selection != "" {
				if _, exists := v.selections[selection]; exists {
					return fmt.Errorf("行选择名称重复")
				}
				v.selections[selection] = struct{}{}
			}
			if detail, ok := object["detail"].(string); ok && detail != "" {
				if _, exists := v.dialogs[detail]; !exists {
					return fmt.Errorf("未声明的详情弹窗: %s", detail)
				}
				if _, hasSelection := object["selection"]; !hasSelection {
					return fmt.Errorf("table.detail 必须同时声明 selection")
				}
			}
		}
		if nodeType == "chart" {
			if _, ok := object["x"].(string); !ok {
				return fmt.Errorf("chart.x 必须是字符串")
			}
			if _, ok := object["y"].(string); !ok {
				return fmt.Errorf("chart.y 必须是字符串")
			}
		}
		return nil
	case "input", "date", "switch", "select", "pagination":
		name, _ := object["name"].(string)
		if !webUIIdentifierPattern.MatchString(name) {
			return fmt.Errorf("组件 name 非法: %s", name)
		}
		if _, exists := v.inputs[name]; exists {
			return fmt.Errorf("重复输入字段: %s", name)
		}
		v.inputs[name] = struct{}{}
		if nodeType == "select" {
			options, ok := object["options"].([]any)
			if !ok || len(options) == 0 || len(options) > 100 {
				return fmt.Errorf("select.options 需要 1 到 100 项")
			}
			if value, ok := object["value"].(string); ok && value != "" {
				matched := false
				for _, option := range options {
					if optionObject, ok := option.(map[string]any); ok && stringValue(optionObject["value"]) == value {
						matched = true
						break
					}
				}
				if !matched {
					return fmt.Errorf("select 默认值必须属于 options")
				}
			}
		}
		return nil
	case "button":
		action, _ := object["action"].(string)
		binding, declared := v.page.Actions[action]
		if !declared {
			return fmt.Errorf("未声明的操作: %s", action)
		}
		variant, _ := object["variant"].(string)
		if variant == "danger" && binding.Confirmation == "" {
			return fmt.Errorf("danger 操作必须声明 confirmation")
		}
		return nil
	default:
		return fmt.Errorf("未知组件类型: %s", nodeType)
	}
}

func (v *webUIPageValidator) checkCondition(raw any) error {
	object, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("when 必须是对象")
	}
	reference, err := parseWebUIDataReference(object["reference"], "when.reference")
	if err != nil {
		return err
	}
	operator, _ := object["operator"].(string)
	if operator != "" && operator != "truthy" && operator != "empty" && operator != "not_empty" && operator != "equals" {
		return fmt.Errorf("when.operator 非法")
	}
	if operator != "equals" && object["expected"] != nil {
		return fmt.Errorf("只有 equals 条件可以声明 expected")
	}
	return v.checkReference(reference)
}

func (v *webUIPageValidator) checkReference(reference WebUIDataReference) error {
	var sources map[string]*WebUIBinding
	if reference.Scope == "selection" {
		if _, exists := v.selections[reference.Source]; !exists {
			return fmt.Errorf("未声明的数据上下文: %s.%s", reference.Scope, reference.Source)
		}
		return nil
	}
	sources = v.page.Queries
	if _, exists := sources[reference.Source]; !exists {
		return fmt.Errorf("未声明的数据上下文: %s.%s", reference.Scope, reference.Source)
	}
	return nil
}

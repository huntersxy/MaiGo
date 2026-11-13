package maibot

import toml "github.com/pelletier/go-toml/v2"

// applyLearningItemDefaults 对齐 Pydantic LearningItem 默认值：use/learn 缺省 true、type 缺省 group；
// use_expression / enable_learning 是 8.10.18–8.10.19 升级链的旧字段，use/learn 缺失时沿用。
//
// 不用 pelletier EnableUnmarshalerInterface 的 UnmarshalTOML([]byte) 钩子：该钩子对
// 数组内联表元素只递 "{" 首字节（上游缺陷，官方模板 learning_list = [{...}] 直接触发），
// 因此在解码后按原始解析树兜底。
func applyLearningItemDefaults(doc *botConfigDocument, raw []byte) {
	var tree map[string]any
	if err := toml.Unmarshal(raw, &tree); err != nil {
		return
	}
	patch := func(entries any, list *[]LearningItem) {
		arr, ok := entries.([]any)
		if !ok || list == nil {
			return
		}
		for i, e := range arr {
			if i >= len(*list) {
				break
			}
			entry, ok := e.(map[string]any)
			if !ok {
				continue
			}
			item := &(*list)[i]
			_, hasUse := entry["use"]
			if !hasUse {
				if useExpr, ok := entry["use_expression"].(bool); ok {
					item.Use = useExpr
				} else {
					item.Use = true
				}
			}
			_, hasLearn := entry["learn"]
			if !hasLearn {
				if enabled, ok := entry["enable_learning"].(bool); ok {
					item.Learn = enabled
				} else {
					item.Learn = true
				}
			}
			if _, hasType := entry["type"]; !hasType {
				item.Type = "group"
			}
		}
	}
	if expr, ok := tree["expression"].(map[string]any); ok {
		patch(expr["learning_list"], &doc.Expression.LearningList)
	}
	if jargon, ok := tree["jargon"].(map[string]any); ok {
		patch(jargon["learning_list"], &doc.Jargon.LearningList)
	}
}

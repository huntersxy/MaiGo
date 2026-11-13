package hooks

// 本文件汇总 MaiBot 全部内置命名 Hook 规格，参数模型逐字段对齐 build_object_schema 输出，未做简化

// RegisterBuiltinHookSpecs 注册 MaiBot 内置命名 Hook 规格
func RegisterBuiltinHookSpecs(reg *SpecRegistry) []HookSpec {
	specs := make([]HookSpec, 0, 29)
	specs = append(specs, chatHookSpecs()...)
	specs = append(specs, emojiHookSpecs()...)
	specs = append(specs, expressionHookSpecs()...)
	specs = append(specs, jargonHookSpecs()...)
	specs = append(specs, maisakaHookSpecs()...)
	specs = append(specs, sendServiceHookSpecs()...)
	specs = append(specs, desktopPetHookSpecs()...)
	return reg.RegisterHookSpecs(specs...)
}

// hookSchema 构建对象级 Hook 参数模型，对齐 plugin_runtime/hook_schema_utils.build_object_schema
func hookSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		names := make([]any, 0, len(required))
		for _, name := range required {
			names = append(names, name)
		}
		schema["required"] = names
	}
	return schema
}

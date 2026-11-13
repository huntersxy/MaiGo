package hooks

// desktopPetHookSpecs 对齐 MaiBot src/desktop_pet/hooks.py register_desktop_pet_hook_specs
// 桌宠事件全部为观察型：不允许 blocking、不允许 abort、不允许改写 kwargs
func desktopPetHookSpecs() []HookSpec {
	return []HookSpec{
		desktopPetSpec("ready", "桌宠窗口已就绪", map[string]any{}),
		desktopPetSpec("closed", "桌宠窗口已退出", map[string]any{
			"reason": map[string]any{"type": "string"},
		}, "reason"),
		desktopPetSpec("clicked", "用户点击桌宠", map[string]any{}),
		desktopPetSpec("drag_started", "用户开始实际拖动桌宠", map[string]any{
			"gesture_id": map[string]any{"type": "string"},
			"offset_x":   map[string]any{"type": "integer"},
			"offset_y":   map[string]any{"type": "integer"},
			"scale":      map[string]any{"type": "number"},
		}, "gesture_id", "offset_x", "offset_y", "scale"),
		desktopPetSpec("drag_ended", "用户结束或取消拖动桌宠", map[string]any{
			"gesture_id": map[string]any{"type": "string"},
		}, "gesture_id"),
		desktopPetSpec("visibility_changed", "桌宠可见状态改变", map[string]any{
			"visible": map[string]any{"type": "boolean"},
		}, "visible"),
		desktopPetSpec("skin_changed", "桌宠皮肤已切换", map[string]any{
			"skin_id": map[string]any{"type": "string"},
		}, "skin_id"),
	}
}

// desktopPetSpec 构造单个桌宠事件规格，固定 5s 超时与观察型限制
func desktopPetSpec(name, description string, properties map[string]any, required ...string) HookSpec {
	return HookSpec{
		Name:               "desktop_pet." + name,
		Description:        description,
		ParametersSchema:   hookSchema(properties, required...),
		DefaultTimeoutMS:   5000,
		DenyBlocking:       true,
		DenyAbort:          true,
		DenyKwargsMutation: true,
	}
}

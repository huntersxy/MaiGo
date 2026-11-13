package memruntime

// 原生装配状态上报：宿主通过该回调拿到降级原因与缺失服务
// 回调由 native 包装配注册，运行期按需读取，避免在 Kernel 中反向依赖 native

// SetStatusReporter 注册状态快照回调，nil 表示不上报
func (k *Kernel) SetStatusReporter(reporter func() map[string]any) {
	if k == nil {
		return
	}
	k.statusReporter = reporter
}

// HostStatusReport 返回原生装配状态快照；未注册时返回空 map
func (k *Kernel) HostStatusReport() map[string]any {
	if k == nil || k.statusReporter == nil {
		return map[string]any{}
	}
	return copyAnyMap(k.statusReporter())
}

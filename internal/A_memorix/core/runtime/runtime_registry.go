package memruntime

// A_memorix 全局运行时内核注册表，对齐 A_memorix/runtime_registry.py
// 说明：Python 版把内核放在模块级单例，Go 版用读写锁保护同一语义

import "sync"

// runtimeRegistry 保存全局运行时内核
type runtimeRegistry struct {
	mu     sync.RWMutex
	kernel *Kernel
}

// globalRuntimeRegistry 是进程级单例
var globalRuntimeRegistry = &runtimeRegistry{}

// SetRuntimeKernel 注册全局运行时内核，对齐 set_runtime_kernel
func SetRuntimeKernel(kernel *Kernel) {
	globalRuntimeRegistry.mu.Lock()
	defer globalRuntimeRegistry.mu.Unlock()
	globalRuntimeRegistry.kernel = kernel
}

// GetRuntimeKernel 返回全局运行时内核，未注册时为 nil，对齐 get_runtime_kernel
func GetRuntimeKernel() *Kernel {
	globalRuntimeRegistry.mu.RLock()
	defer globalRuntimeRegistry.mu.RUnlock()
	return globalRuntimeRegistry.kernel
}

// GetRuntimeComponents 返回全局运行时组件快照，对齐 get_runtime_components
// 无内核时返回空字典；vector_pools_ready 探测内核的双池开关
func GetRuntimeComponents() map[string]any {
	kernel := GetRuntimeKernel()
	if kernel == nil {
		return map[string]any{}
	}
	return map[string]any{
		"vector_store":           kernel.VectorStore(),
		"paragraph_vector_store": kernel.ParagraphVectorStore(),
		"graph_vector_store":     kernel.GraphVectorStore(),
		"graph_store":            kernel.GraphStore(),
		"metadata_store":         kernel.MetadataStore(),
		"embedding_manager":      kernel.EmbeddingManager(),
		"sparse_index":           kernel.SparseIndex(),
		"vector_pools_ready":     kernel.DualVectorPoolsEnabled(),
	}
}

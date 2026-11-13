// Package memorix 是 MaiBot A_Memorix 长期记忆在 Go 侧的宿主门面。
//
// Python 对照：src/A_memorix/host_service.py（AMemorixHostService.invoke）与
// src/services/memory_service.py。内核实现在 src/A_memorix/core/，本包不复制。
//
// 后端二选一：AMemorixHostService（原生内核）或 RemoteHost（MAIBOT_MEMORIX_REMOTE_URL
// 指向的 Python 侧车）。未完成面与差异见 agents/MODULE_ALIGNMENT.md。
package memorix

// 对齐 Python A_memorix/__init__.py 的包元数据
const (
	// Version 对应 __version__
	Version = "2.0.0"
	// Author 对应 __author__
	Author = "A_Dawn"
)

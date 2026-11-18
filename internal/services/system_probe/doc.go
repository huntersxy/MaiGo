// Package system_probe 提供只读的系统资源诊断快照。
//
// 本包是 Go 平台扩展：Python MaiBot 没有对应的资源探针端点，因此响应结构不追求与
// Python 字段对齐，仅作为诊断补充留档。探针只依赖标准库，不 import bootstrap、
// 数据库实现或任何业务包。
//
// 采集边界：
//   - 仅 Linux 采集 /proc、cgroup、statfs 与 rlimit；其它平台返回空 section 并写入
//     errors[]，runtime/app/meta 照常可用。
//   - 所有读取都有字节上限，进程与设备列表有数量上限；超时只影响单个 section。
//   - cgroup v1 按 /proc/self/cgroup 的 controller 相对路径定位文件；无法定位时不回落到
//     宿主挂载根，available=false、partial=true。
//   - 进程按 uid==euid 过滤，只能看到当前用户的进程；self 与调用方注入的子进程始终直接读取，
//     不受 ScanLimit 影响。cmdline 输出前对常见凭据参数做掩码。
//   - statfs 限流器容量为 2，槽位占满时按 ctx 排队而非立即丢弃；goroutine 一旦永久阻塞，
//     槽位不会回收，该进程后续 Snapshot 的 mounts 会持续缺失，直到进程重启。
//   - 外部 provider 与 runtime 采集各有 500ms 独立上限，因此 elapsed_ms 可能超过 timeout_ms。
//   - 探针不启动常驻后台循环或定时器；statfs/provider 使用受 ctx 约束的短生命周期 goroutine。
//
// 暴露面：端点挂在 requireAuthMiddleware 之后，但 HostManaged 模式下 authenticated
// 恒真（webui/routers/handlers_core.go），其暴露面与 /local-cache 相同。
package system_probe

// Package filesvc 对齐 MaiBot src/files：附件元数据、按 file_ref 落盘、配额预留、
// 流式传输、文本解析、限时授权下载、插件 files.* 能力与 files_* 工具定义。
//
// 文件对应：config.go↔config.py、models.go↔models.py、network.go↔network.py、
// service.go↔service.py、store.go↔store.py、http.go↔http.py、
// provider.go↔provider.py、capability.go↔capability.py。
//
// 落盘布局与 Python 一致：{root}/{file_ref}.blob 与传输中的 .part，不做内容寻址去重，
// sha256 仅作校验值存列。远程附件经 RemoteURLResolver 取地址，network.go 在每次
// 重定向与实际 DNS 连接时校验公网地址，未注入解析器时返回真实错误。
package filesvc

// Package runtime 对齐 MaiBot src/runtime.py 与 src/common/process_runner.py 的 Runner/Worker 分层
//
// 默认仍为单进程 Worker（不设置 MAIBOT_RUNTIME_MODE 时直接跑 bot 逻辑）。
// 设置 MAIBOT_RUNTIME_MODE=runner 时，当前进程监督子进程 Worker，并在子进程以退出码 42 退出时重启。
//
// 自更新：WebUI POST /api/webui/system/request-restart 写入 pending 并令 Worker 以 42 退出；
// Runner 在重启前调用 ApplyPendingUpdate，读取 MAIBOT_UPDATE_PENDING_PATH 或 data/self_update/pending_binary.json 指向的新二进制并替换当前 exe；成功后以 43 结束当前监督循环，由受监督的新 Runner 接手（未受监督时由当前进程启动新 Runner）。
package runtime

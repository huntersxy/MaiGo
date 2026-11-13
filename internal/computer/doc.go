// Package computer 对齐 MaiBot src/computer：Windows Sandbox 共享沙盒与电脑工具。
//
// Python 对照：service.py（ComputerService 生命周期与 pause/接管）、bridge.py
// （wsb.exe 通信与心跳）、provider.py（desktop/observe/poll/cancel）、actions.py
// （动作 schema 与整批校验）。
//
// 模型输入始终送入 Windows Sandbox；仅在 MAIBOT_COMPUTER_LOCAL=1 且沙盒未启用时
// 允许本机 PowerShell 执行（受 MAIBOT_COMPUTER_LOCAL_ALLOWLIST 与超时约束）。
package computer

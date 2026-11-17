# 宿主平台的必要差异

只记录 Go 单二进制无法逐行照搬的 Python 运行时机制；行为契约保持一致。

- **启动与更新**：Python 由 `launcher.py` 常驻并分阶段执行 `bootstrap.py`、`updater.py`。Go 在宿主退出后直接调用 `self_update.ApplyPending`，成功构建新二进制后释放 `host/run.lock` 并以同一参数启动新进程。
- **终端输入**：宿主命令与 IPC 路径一致；Go 使用标准输入逐行读取，不提供 prompt_toolkit 的历史浏览和 Tab 补全。
- **Windows 进程回收**：Python 使用 Job Object；Go 使用进程树信号与 `taskkill /T /F`，语义同为停止 Worker 及其子进程。
- **插件依赖分析**：Python 在 Bot 目录启动 `requirements_report` 子进程；Go 直接读取插件 Manifest，并调用同一套 PEP 440 与依赖流水线代码。
- **打包安装自更新**：Python 支持 uv tool 包替换；Go 仅对 git 源码部署执行快进、构建和回滚，非 git 部署在检查阶段报告 `not_git_repo`。
- **配置文件回写**：Go 的 TOML 编码器不保留原文件注释与列对齐；Python 使用 tomlkit 保留。字段值、校验与事务语义一致。
- **Dashboard 版本检查**：宿主启动仍解析原版 dashboard 构建目录；Go 不读取 Python 包元数据做前后端版本兼容提示。

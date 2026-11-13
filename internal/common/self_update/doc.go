// Package selfupdate 对齐 ../MaiBot/src/common/self_update.py 与 package_update.py
//
// 负责检查 Git 源码部署与 PyPI 包部署的可用更新、登记待执行请求，并在 Runner 重启间隙执行
// 快进合并、依赖同步、新二进制构建或 uv 包安装；失败时按 Python 语义回滚并写入 last_result.json
package selfupdate

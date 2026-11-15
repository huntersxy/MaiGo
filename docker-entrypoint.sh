#!/bin/sh
set -eu

# 只读根目录下 config/data/logs/plugins/temp 由命名卷或 tmpfs 提供；
# media/stickers 可能没有挂卷，此时目录属于镜像层且不可写，chown 失败不能拖垮启动（镜像构建期已设好属主）
for dir in /app/config /app/data /app/logs /app/plugins /app/temp /app/media /app/stickers /app/plugin-runtime; do
    mkdir -p "$dir" 2>/dev/null || true
    chown maibot:maibot "$dir" 2>/dev/null || true
done

# 数据库是 SQLite（/app/data/MaiBot.db），随 data 卷备份/恢复，无需 postgresql 客户端

# 插件依赖安装写进 Runner venv，只读镜像层无法写入：
# 命名卷首次挂载为空时从镜像副本播种
if [ -d /opt/plugin-runtime ] && [ -z "$(ls -A /app/plugin-runtime 2>/dev/null)" ]; then
    cp -a /opt/plugin-runtime/. /app/plugin-runtime/
    chown -R maibot:maibot /app/plugin-runtime 2>/dev/null || true
fi

exec su-exec maibot:maibot "$@"

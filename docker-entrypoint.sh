#!/bin/sh
set -eu

# 只读根目录下 config/data/logs/plugins/temp 由命名卷或 tmpfs 提供；
# media/stickers 可能没有挂卷，此时目录属于镜像层且不可写，chown 失败不能拖垮启动（镜像构建期已设好属主）
for dir in /app/config /app/data /app/host /app/bots /app/logs /app/plugins /app/temp /app/media /app/stickers /app/plugin-runtime; do
    mkdir -p "$dir" 2>/dev/null || true
    chown maibot:maibot "$dir" 2>/dev/null || true
done

# 数据库是 SQLite（/app/data/MaiBot.db），随 data 卷备份/恢复，无需 postgresql 客户端

# 模型与 MCP 密钥由 MAIBOT_SECRET_KEY 加密；首次启动自动生成并随 data 卷保存
secret_file=/app/data/.secret_key
if [ -z "${MAIBOT_SECRET_KEY:-}" ]; then
    if [ -s "$secret_file" ]; then
        MAIBOT_SECRET_KEY=$(cat "$secret_file")
    else
        MAIBOT_SECRET_KEY=$(head -c 32 /dev/urandom | base64 | tr -d '\n')
        (umask 077; printf '%s' "$MAIBOT_SECRET_KEY" > "$secret_file")
        chown maibot:maibot "$secret_file" 2>/dev/null || true
    fi
    export MAIBOT_SECRET_KEY
fi

# 插件依赖安装写进 Runner venv，只读镜像层无法写入：
# 命名卷首次挂载为空时从镜像副本播种
if [ -d /opt/plugin-runtime ] && [ -z "$(ls -A /app/plugin-runtime 2>/dev/null)" ]; then
    cp -a /opt/plugin-runtime/. /app/plugin-runtime/
    chown -R maibot:maibot /app/plugin-runtime 2>/dev/null || true
fi

# 安装版 Runner 从 site-packages 读取内置插件与 src.plugin_runtime；
# 未显式指定时按虚拟环境实际 Python 版本定位
if [ -z "${MAIBOT_PLUGIN_PROJECT_ROOT:-}" ]; then
    for candidate in /app/plugin-runtime/lib/python*/site-packages; do
        if [ -f "$candidate/src/plugin_runtime/runner/runner_main.py" ]; then
            export MAIBOT_PLUGIN_PROJECT_ROOT="$candidate"
            break
        fi
    done
fi

# 错别字注入与表达向量聚类跑 vendored 原版 Python 实现，随主进程在同一容器内启动。
# 只用 /app/pycompat-src 下的两个模块，不挂载或打包整套 MaiBot 源码。
compat_python=/app/pycompat/bin/python
if [ -x "$compat_python" ] && [ -f /app/pycompat_sidecar.py ]; then
    export MAIBOT_INSTANCE_DIR="${MAIBOT_INSTANCE_DIR:-/app}"
    export MAIBOT_SOURCE_ROOT="${MAIBOT_SOURCE_ROOT:-/app/pycompat-src}"
    export MAIBOT_COMPAT_SIDECAR_PORT="${MAIBOT_COMPAT_SIDECAR_PORT:-8767}"
    export MAIBOT_COMPAT_SIDECAR_TOKEN="${MAIBOT_COMPAT_SIDECAR_TOKEN:-${MAIBOT_COMPAT_TOKEN:-}}"
    export PYTHONDONTWRITEBYTECODE=1
    mkdir -p /app/depends-data 2>/dev/null || true
    chown maibot:maibot /app/depends-data 2>/dev/null || true
    cd /app
    su-exec maibot:maibot "$compat_python" /app/pycompat_sidecar.py &
    compat_pid=$!
    # 侧车未就绪时 Go 侧会回落到本地实现；短暂等待只是避免首批请求走回落
    for _ in $(seq 1 40); do
        if "$compat_python" -c "import socket; socket.create_connection(('127.0.0.1', int('$MAIBOT_COMPAT_SIDECAR_PORT')), 0.2).close()" 2>/dev/null; then
            break
        fi
        if ! kill -0 "$compat_pid" 2>/dev/null; then
            break
        fi
        sleep 0.25
    done
    export MAIBOT_COMPAT_URL="${MAIBOT_COMPAT_URL:-http://127.0.0.1:$MAIBOT_COMPAT_SIDECAR_PORT}"
fi

exec su-exec maibot:maibot "$@"

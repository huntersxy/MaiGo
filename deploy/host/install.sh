#!/usr/bin/env bash
# 首次安装：建用户与目录、建 Python Runner venv、装 systemd 单元。
# 只在服务器上跑一次；后续发布用 deploy.sh。
set -euo pipefail

ROOT="${1:-/opt/maigo}"
SERVICE_USER=maibot
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PIP_INDEX="${MAIGO_PIP_INDEX_URL:-https://pypi.tuna.tsinghua.edu.cn/simple}"
PYTHON_BIN="${MAIGO_PYTHON:-python3.12}"

log() { printf '[install] %s\n' "$*"; }

if [ "$(id -u)" -ne 0 ]; then
    echo "需要 root 运行" >&2
    exit 1
fi

if ! command -v "$PYTHON_BIN" >/dev/null 2>&1; then
    echo "缺少 $PYTHON_BIN；Runner venv 与运行镜像同为 Python 3.12" >&2
    exit 1
fi

if ! id "$SERVICE_USER" >/dev/null 2>&1; then
    log "创建系统用户 $SERVICE_USER"
    useradd --system --home-dir "$ROOT" --shell /usr/sbin/nologin "$SERVICE_USER"
fi

log "创建目录 $ROOT"
mkdir -p "$ROOT"/{app,releases,runtime,deploy,incoming}
install -m 755 "$SCRIPT_DIR/deploy.sh" "$ROOT/deploy/deploy.sh"
install -m 755 "$SCRIPT_DIR/update.sh" "$ROOT/deploy/update.sh"
install -m 755 "$SCRIPT_DIR/migrate-from-docker.sh" "$ROOT/deploy/migrate-from-docker.sh"
install -m 644 "$SCRIPT_DIR/requirements-plugin-runtime.txt" "$ROOT/deploy/"

if [ ! -f "$ROOT/maigo.env" ]; then
    log "生成 maigo.env"
    install -m 600 "$SCRIPT_DIR/maigo.env.example" "$ROOT/maigo.env"
fi

build_runner_venv() {
    local venv="$ROOT/runtime/plugin-runtime"
    if [ -x "$venv/bin/python" ]; then
        log "plugin-runtime venv 已存在，跳过"
        return
    fi
    log "创建 plugin-runtime venv"
    "$PYTHON_BIN" -m venv "$venv"
    "$venv/bin/pip" install --no-cache-dir --no-input --upgrade pip >/dev/null
    "$venv/bin/pip" install --no-cache-dir --no-input --no-deps \
        --index-url "$PIP_INDEX" "maibot==1.4.5"
    "$venv/bin/pip" install --no-cache-dir --no-input --index-url "$PIP_INDEX" \
        -r "$ROOT/deploy/requirements-plugin-runtime.txt"
}

build_runner_venv

# Runner 从 site-packages 加载 src.plugin_runtime，路径随 Python 版本变化
site_packages="$(ls -d "$ROOT"/runtime/plugin-runtime/lib/python*/site-packages 2>/dev/null | head -1)"
if [ -z "$site_packages" ]; then
    echo "未找到 plugin-runtime 的 site-packages" >&2
    exit 1
fi
if grep -q '^MAIBOT_PLUGIN_PROJECT_ROOT=' "$ROOT/maigo.env"; then
    sed -i "s#^MAIBOT_PLUGIN_PROJECT_ROOT=.*#MAIBOT_PLUGIN_PROJECT_ROOT=$site_packages#" "$ROOT/maigo.env"
fi

install -m 644 "$SCRIPT_DIR/maigo.service" /etc/systemd/system/maigo.service
install -m 644 "$SCRIPT_DIR/maigo-update.service" /etc/systemd/system/maigo-update.service
install -m 644 "$SCRIPT_DIR/maigo-update.timer" /etc/systemd/system/maigo-update.timer
systemctl daemon-reload
systemctl enable --now maigo-update.timer

chown -R "$SERVICE_USER:$SERVICE_USER" "$ROOT/app" "$ROOT/runtime" "$ROOT/releases" "$ROOT/incoming"
chown root:"$SERVICE_USER" "$ROOT/maigo.env"
chmod 640 "$ROOT/maigo.env"

log "安装完成"
log "下一步：在 $ROOT/maigo.env 填 MAIBOT_SECRET_KEY，然后 systemctl enable --now maigo"
log "已有 Docker 数据时先跑 $ROOT/deploy/migrate-from-docker.sh $ROOT"
log "自动更新：maigo-update.timer 每 15 分钟检查 releases/latest；手动跑 $ROOT/deploy/update.sh"

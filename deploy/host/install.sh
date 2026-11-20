#!/usr/bin/env bash
# 首次安装：建用户与目录、建两个 Python venv、装 systemd 单元与推送入口。
# 只在服务器上跑一次；后续发布由 CI 推送或 update.sh 拉取。
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
for script in deploy.sh update.sh ci-trigger.sh migrate-from-docker.sh; do
    install -m 755 "$SCRIPT_DIR/$script" "$ROOT/deploy/$script"
done
install -m 644 "$SCRIPT_DIR/requirements-plugin-runtime.txt" "$ROOT/deploy/"
install -m 644 "$SCRIPT_DIR/requirements-pycompat.txt" "$ROOT/deploy/"

if [ ! -f "$ROOT/maigo.env" ]; then
    log "生成 maigo.env"
    install -m 600 "$SCRIPT_DIR/maigo.env.example" "$ROOT/maigo.env"
fi

build_venv() {
    local name="$1" requirements="$2"
    local venv="$ROOT/runtime/$name"
    if [ -x "$venv/bin/python" ]; then
        log "$name venv 已存在，跳过"
        return
    fi
    log "创建 $name venv"
    "$PYTHON_BIN" -m venv "$venv"
    "$venv/bin/pip" install --no-cache-dir --no-input --upgrade pip >/dev/null
    if [ "$name" = "plugin-runtime" ]; then
        "$venv/bin/pip" install --no-cache-dir --no-input --no-deps \
            --index-url "$PIP_INDEX" "maibot==1.4.5"
    fi
    "$venv/bin/pip" install --no-cache-dir --no-input --index-url "$PIP_INDEX" -r "$requirements"
}

build_venv plugin-runtime "$ROOT/deploy/requirements-plugin-runtime.txt"
build_venv pycompat "$ROOT/deploy/requirements-pycompat.txt"

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
systemctl daemon-reload

chown -R "$SERVICE_USER:$SERVICE_USER" "$ROOT/app" "$ROOT/runtime" "$ROOT/releases" "$ROOT/incoming"
chown root:"$SERVICE_USER" "$ROOT/maigo.env"
chmod 640 "$ROOT/maigo.env"

log "安装完成"
log "下一步：在 $ROOT/maigo.env 填 MAIBOT_SECRET_KEY，然后 systemctl enable --now maigo"
log "已有 Docker 数据时先跑 $ROOT/deploy/migrate-from-docker.sh $ROOT"
log "发布：CI 用受限密钥调 $ROOT/deploy/ci-trigger.sh 传提交号，服务器自行从镜像拉包部署"

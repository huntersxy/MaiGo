#!/usr/bin/env bash
# 一次性迁移：把 Docker 命名卷里的用户数据搬到 <root>/app，卷与镜像都保留以便回退。
# 用法：migrate-from-docker.sh [--root /opt/maigo] [--skip-stop]
set -euo pipefail

ROOT="/opt/maigo"
SKIP_STOP=0
while [ $# -gt 0 ]; do
    case "$1" in
        --root) ROOT="$2"; shift 2 ;;
        --skip-stop) SKIP_STOP=1; shift ;;
        *) echo "未知参数: $1" >&2; exit 2 ;;
    esac
done

SERVICE_USER=maibot
PROJECT_DIR="${MAIGO_COMPOSE_DIR:-$ROOT}"
log() { printf '[migrate] %s\n' "$*"; }
die() { printf '[migrate] 错误: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "需要 root 运行"
command -v docker >/dev/null 2>&1 || die "需要 docker 来读取命名卷"

project_name="$(sed -n 's/^name:[[:space:]]*//p' "$PROJECT_DIR/docker-compose.yml" 2>/dev/null | head -1)"
project_name="${project_name:-$(basename "$PROJECT_DIR" | tr '[:upper:]' '[:lower:]')}"

volume_path() {
    local name="$1"
    local path="/var/lib/docker/volumes/${project_name}_${name}/_data"
    [ -d "$path" ] || return 1
    printf '%s' "$path"
}

if [ "$SKIP_STOP" -eq 0 ]; then
    log "停止 Docker 部署（保留卷与镜像）"
    if [ -f "$PROJECT_DIR/docker-compose.yml" ]; then
        (cd "$PROJECT_DIR" && docker compose down) || log "compose down 失败，继续（可能已停止）"
    fi
    # 容器停止后 cgroup 与进程都消失，等 SQLite 的 WAL 落盘
    sleep 3
fi

mkdir -p "$ROOT/app"

migrate_dir() {
    local target="$1"
    shift
    local source="" volume=""
    for volume in "$@"; do
        if source="$(volume_path "$volume")"; then
            break
        fi
        source=""
    done
    if [ -z "$source" ]; then
        log "卷 $* 不存在，跳过"
        return
    fi
    local dest="$ROOT/app/$target"
    mkdir -p "$dest"
    log "同步 ${project_name}_${volume} -> app/$target"
    rsync -a --delete --info=stats2 "$source"/ "$dest"/
}

migrate_dir config maibot-config
migrate_dir data maibot-data
migrate_dir host maibot-host
migrate_dir bots maibot-bots
migrate_dir logs maibot-logs
migrate_dir plugins maibot-plugins
migrate_dir media media maibot-media
migrate_dir stickers stickers maibot-stickers

log "清理陈旧锁文件"
rm -f "$ROOT/app/temp/instance.lock" \
      "$ROOT/app/host/run.lock" \
      "$ROOT/app/data/a-memorix/.a_memorix_runtime_writer.lock"

# 密钥沿用容器内生成的 data/.secret_key
secret=""
if [ -s "$ROOT/app/data/.secret_key" ]; then
    secret="$(cat "$ROOT/app/data/.secret_key")"
fi
if [ -n "$secret" ] && [ -f "$ROOT/maigo.env" ]; then
    if grep -q '^MAIBOT_SECRET_KEY=' "$ROOT/maigo.env"; then
        sed -i "s#^MAIBOT_SECRET_KEY=.*#MAIBOT_SECRET_KEY=${secret}#" "$ROOT/maigo.env"
    else
        printf 'MAIBOT_SECRET_KEY=%s\n' "$secret" >> "$ROOT/maigo.env"
    fi
    log "已把 data/.secret_key 写入 maigo.env"
fi

chown -R "$SERVICE_USER:$SERVICE_USER" "$ROOT/app"
log "数据迁移完成：$(du -sh "$ROOT/app" | cut -f1)"
log "命名卷保留未动；回退时 cd $PROJECT_DIR && docker compose up -d"

#!/usr/bin/env bash
# 发布：解包到 releases/<ver>/，切 current 软链，重启并等待健康检查；失败自动回滚。
# 用法：deploy.sh <发布包.tar.gz> [--no-restart]
#       deploy.sh --rollback
set -euo pipefail

ROOT="${MAIGO_ROOT:-/opt/maigo}"
SERVICE=maigo
KEEP_RELEASES=3
HEALTH_TIMEOUT="${MAIGO_HEALTH_TIMEOUT:-90}"

log() { printf '[deploy] %s\n' "$*"; }
die() { printf '[deploy] 错误: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "需要 root 运行"
[ -f "$ROOT/maigo.env" ] || die "缺少 $ROOT/maigo.env"

health_port() {
    local port
    port="$(sed -n 's/^MAIBOT_HOST_PORT=//p' "$ROOT/maigo.env" | tail -1)"
    printf '%s' "${port:-7468}"
}

current_version() {
    if [ -L "$ROOT/current" ]; then
        basename "$(readlink -f "$ROOT/current")"
    fi
}

previous_release() {
    local active
    active="$(readlink -f "$ROOT/current" 2>/dev/null || true)"
    find "$ROOT/releases" -mindepth 1 -maxdepth 1 -type d ! -path "$active" -printf '%T@ %p\n' 2>/dev/null \
        | sort -rn | head -1 | cut -d' ' -f2-
}

switch_to() {
    local dir="$1"
    ln -sfn "$dir" "$ROOT/current.tmp"
    mv -Tf "$ROOT/current.tmp" "$ROOT/current"
}

wait_healthy() {
    local port="$1" deadline=$((SECONDS + HEALTH_TIMEOUT))
    while [ "$SECONDS" -lt "$deadline" ]; do
        if ! systemctl is-active --quiet "$SERVICE"; then
            return 1
        fi
        if curl -fsS --max-time 3 "http://127.0.0.1:$port/health" >/dev/null 2>&1; then
            return 0
        fi
        sleep 2
    done
    return 1
}

restart_and_check() {
    systemctl restart "$SERVICE"
    if wait_healthy "$(health_port)"; then
        return 0
    fi
    return 1
}

prune_old_releases() {
    local active
    active="$(readlink -f "$ROOT/current" 2>/dev/null || true)"
    local index=0
    while read -r dir; do
        [ -n "$dir" ] || continue
        if [ "$(readlink -f "$dir")" = "$active" ]; then
            continue
        fi
        index=$((index + 1))
        if [ "$index" -ge "$KEEP_RELEASES" ]; then
            log "清理旧版本 $(basename "$dir")"
            rm -rf "$dir"
        fi
    done < <(find "$ROOT/releases" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\n' | sort -rn | cut -d' ' -f2-)
}

rollback() {
    local target
    target="$(previous_release)"
    [ -n "$target" ] || die "没有可回滚的历史版本"
    log "回滚到 $(basename "$target")"
    switch_to "$target"
    restart_and_check || die "回滚版本同样未通过健康检查，请查看 journalctl -u $SERVICE"
    log "回滚完成：$(basename "$target")"
}

if [ "${1:-}" = "--rollback" ]; then
    rollback
    exit 0
fi

package="${1:-}"
restart="${2:-}"
[ -n "$package" ] || die "用法: deploy.sh <发布包.tar.gz> [--no-restart]"
[ -f "$package" ] || die "发布包不存在: $package"

if [ -f "$package.sha256" ]; then
    log "校验 sha256"
    (cd "$(dirname "$package")" && sha256sum -c "$(basename "$package").sha256")
fi

if pgrep -f "maigo (run|worker)" >/dev/null 2>&1 && [ "$restart" != "--no-restart" ]; then
    log "检测到运行中的实例，发布完成后会重启"
fi

mkdir -p "$ROOT/releases" "$ROOT/incoming"
staging="$(mktemp -d "$ROOT/incoming/unpack-XXXXXX")"
trap 'rm -rf "$staging"' EXIT

log "解包 $(basename "$package")"
tar -xzf "$package" -C "$staging"
[ -x "$staging/maigo" ] || die "发布包缺少可执行文件 maigo"
[ -f "$staging/VERSION" ] || die "发布包缺少 VERSION"

version="$(tr -d '[:space:]' < "$staging/VERSION")"
[ -n "$version" ] || die "VERSION 为空"
commit="$(tr -d '[:space:]' < "$staging/COMMIT" 2>/dev/null || true)"
# 目录名带上提交短号：滚动构建的 VERSION 固定为 dev，靠提交号区分
release_id="$version"
if [ "$version" = "dev" ] && [ -n "$commit" ]; then
    release_id="dev-$(printf '%s' "$commit" | cut -c1-7)"
fi
# 记录包指纹：update.sh 用它判断同名版本是否需要重新部署
sha256sum "$package" | cut -d' ' -f1 > "$staging/.package.sha256"

target="$ROOT/releases/$release_id"
if [ -d "$target" ]; then
    log "版本 $release_id 已存在，原地替换"
    rm -rf "$target.old"
    mv "$target" "$target.old"
    trap 'rm -rf "$staging" "$target.old"' EXIT
fi
mv "$staging" "$target"
chown -R maibot:maibot "$target"
trap 'rm -rf "$target.old"' EXIT

active_before="$(current_version)"
log "切换 current -> releases/$release_id"
switch_to "$target"

if [ "$restart" = "--no-restart" ]; then
    log "已切换，未重启（--no-restart）"
    exit 0
fi

if restart_and_check; then
    rm -rf "$target.old"
    prune_old_releases
    log "发布完成：$release_id（原版本 ${active_before:-无}）"
    exit 0
fi

log "健康检查失败，回滚"
journalctl -u "$SERVICE" -n 40 --no-pager || true
if [ -n "$active_before" ] && [ -d "$ROOT/releases/$active_before" ]; then
    switch_to "$ROOT/releases/$active_before"
    restart_and_check || die "旧版本 ${active_before} 也未能恢复，请人工介入"
    log "已回滚到 $active_before"
else
    rollback
fi
exit 1

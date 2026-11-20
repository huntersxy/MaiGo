#!/usr/bin/env bash
# 服务器侧主动拉取：取仓库最新 Release 的发布包，交给 deploy.sh 切换版本。
# 由 maigo-update.timer 周期触发，也可手动执行。
# 用法：update.sh [--dry-run] [--force]
set -euo pipefail

ROOT="${MAIGO_ROOT:-/opt/maigo}"
REPO="${MAIGO_REPO:-huntersxy/MaiGo}"
DRY_RUN=0
FORCE=0
while [ $# -gt 0 ]; do
    case "$1" in
        --dry-run) DRY_RUN=1; shift ;;
        --force) FORCE=1; shift ;;
        *) echo "未知参数: $1" >&2; exit 2 ;;
    esac
done

log() { printf '[update] %s\n' "$*"; }
die() { printf '[update] 错误: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "需要 root 运行"

api="https://api.github.com/repos/${REPO}/releases/latest"
body="$(mktemp)"
trap 'rm -f "$body"' EXIT
code="$(curl -sS --max-time 30 -o "$body" -w '%{http_code}' "$api" || echo 000)"
case "$code" in
    200) release="$(cat "$body")" ;;
    404) log "仓库还没有 Release，跳过"; exit 0 ;;
    *) die "获取 Release 失败（HTTP ${code}）：${api}" ;;
esac
tag="$(printf '%s' "$release" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
[ -n "$tag" ] || die "Release 中没有 tag_name"
version="${tag#v}"
asset_url="$(printf '%s' "$release" \
    | tr ',' '\n' \
    | sed -n 's/.*"browser_download_url": *"\([^"]*maigo-[^"/]*-linux-amd64\.tar\.gz\)".*/\1/p' | head -1)"
[ -n "$asset_url" ] || die "Release ${tag} 中没有 linux-amd64 发布包"

active="$(basename "$(readlink -f "$ROOT/current" 2>/dev/null || echo "")")"
if [ "$active" = "$version" ] && [ "$FORCE" -eq 0 ]; then
    log "当前已是 ${version}，跳过"
    exit 0
fi

if [ "$DRY_RUN" -eq 1 ]; then
    log "可更新：${active:-无} -> ${version}（${asset_url}）"
    exit 0
fi

mkdir -p "$ROOT/incoming"
package="$ROOT/incoming/$(basename "$asset_url")"
log "下载 ${tag}"
curl -fsSL --max-time 300 -o "$package" "$asset_url" || die "下载失败"
curl -fsSL --max-time 60 -o "$package.sha256" "${asset_url}.sha256" \
    || log "未取到校验文件，deploy.sh 会跳过校验"

"$ROOT/deploy/deploy.sh" "$package"

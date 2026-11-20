#!/usr/bin/env bash
# 服务器侧主动拉取：取仓库最新 Release 的发布包，交给 deploy.sh 切换版本。
# 由 maigo-update.timer 周期触发，也可手动执行。
# 用法：update.sh [--dry-run] [--force]
set -euo pipefail

ROOT="${MAIGO_ROOT:-/opt/maigo}"
REPO="${MAIGO_REPO:-huntersxy/MaiGo}"
# 国内直连 GitHub 资产常被中断，按顺序回退；内容用 API 给出的 sha256 校验
MIRRORS="${MAIGO_GH_MIRRORS:-https://gh-proxy.com/ https://ghfast.top/ }"
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

# 默认跟随滚动预发布通道（tag=rolling）；MAIGO_RELEASE=latest 跟正式版本 Release
ROLLING="${MAIGO_RELEASE:-rolling}"
case "$ROLLING" in
    latest) api="https://api.github.com/repos/${REPO}/releases/latest" ;;
    *)      api="https://api.github.com/repos/${REPO}/releases/tags/${ROLLING}" ;;
esac
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
asset_name="$(basename "$asset_url")"
# GitHub 从 2025 起在资产对象里给出 sha256:<hex>，用它校验下载内容
expected_sha="$(printf '%s' "$release" | tr '{' '\n' \
    | sed -n "/\"name\": *\"${asset_name}\"/,/}/p" \
    | sed -n 's/.*"digest": *"sha256:\([0-9a-f]*\)".*/\1/p' | head -1)"

active="$(basename "$(readlink -f "$ROOT/current" 2>/dev/null || echo "")")"
active_sha="$(cat "$ROOT/current/.package.sha256" 2>/dev/null || true)"
if [ -n "$expected_sha" ] && [ "$active_sha" = "$expected_sha" ] && [ "$FORCE" -eq 0 ]; then
    log "当前内容已是最新（${active}），跳过"
    exit 0
fi

if [ "$DRY_RUN" -eq 1 ]; then
    log "可更新：${active:-无} -> ${version}（${asset_name}）"
    exit 0
fi

mkdir -p "$ROOT/incoming"
package="$ROOT/incoming/$asset_name"

download() {
    local url="$1"
    log "下载 ${tag}：${url}"
    curl -fsSL --retry 2 --retry-delay 2 --retry-all-errors -C - \
        --max-time 900 -o "$package" "$url"
}

ok=0
for prefix in $MIRRORS; do
    if download "${prefix}${asset_url}"; then
        ok=1
        break
    fi
    log "下载失败，换下一个源"
done
[ "$ok" -eq 1 ] || die "所有下载源均失败"
rm -f "$package.sha256"

if [ -n "$expected_sha" ]; then
    actual_sha="$(sha256sum "$package" | cut -d' ' -f1)"
    [ "$actual_sha" = "$expected_sha" ] \
        || die "校验和不匹配：期望 ${expected_sha}，实际 ${actual_sha}"
    log "sha256 校验通过"
else
    log "Release API 未提供 digest，跳过校验"
fi

# 写成 sha256sum -c 可校验的格式，deploy.sh 会再校验一次
printf '%s  %s\n' "${expected_sha:-$(sha256sum "$package" | cut -d' ' -f1)}" "$asset_name" \
    > "$package.sha256"

"$ROOT/deploy/deploy.sh" "$package"

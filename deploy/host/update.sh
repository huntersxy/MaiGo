#!/usr/bin/env bash
# 服务器侧主动拉取：取仓库 Release 的最新发布包，交给 deploy.sh 切换版本。
# 由 maigo-update.timer 周期触发，也可手动执行。
# 用法：update.sh [--dry-run] [--force]
set -euo pipefail

ROOT="${MAIGO_ROOT:-/opt/maigo}"
REPO="${MAIGO_REPO:-huntersxy/MaiGo}"
# 滚动通道（CI 每次 push main 重建）；MAIGO_RELEASE=latest 跟正式版本 Release
CHANNEL="${MAIGO_RELEASE:-rolling}"
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
command -v python3 >/dev/null || die "需要 python3 解析 Release JSON"

case "$CHANNEL" in
    latest) api="https://api.github.com/repos/${REPO}/releases/latest" ;;
    *)      api="https://api.github.com/repos/${REPO}/releases/tags/${CHANNEL}" ;;
esac

body="$(mktemp)"
trap 'rm -f "$body"' EXIT
code="$(curl -sS --max-time 30 -o "$body" -w '%{http_code}' "$api" || echo 000)"
case "$code" in
    200) ;;
    404) log "仓库还没有 ${CHANNEL} Release，跳过"; exit 0 ;;
    *) die "获取 Release 失败（HTTP ${code}）：${api}" ;;
esac

# 输出：tag / 资产名 / 资产地址 / 大小 / sha256，缺少发布包时直接退出
parsed="$(python3 - "$body" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    release = json.load(handle)
tag = release.get("tag_name") or ""
asset = next(
    (a for a in release.get("assets", [])
     if a.get("name", "").endswith("-linux-amd64.tar.gz")),
    None,
)
if not tag:
    sys.exit("tag_name 缺失")
if asset is None:
    sys.exit(f"{tag} 中没有 linux-amd64 发布包")
digest = (asset.get("digest") or "").removeprefix("sha256:")
print(tag)
print(asset["name"])
print(asset["browser_download_url"])
print(asset.get("size") or 0)
print(digest)
PY
)" || die "解析 Release 失败"

tag="$(printf '%s\n' "$parsed" | sed -n 1p)"
asset_name="$(printf '%s\n' "$parsed" | sed -n 2p)"
asset_url="$(printf '%s\n' "$parsed" | sed -n 3p)"
expected_size="$(printf '%s\n' "$parsed" | sed -n 4p)"
expected_sha="$(printf '%s\n' "$parsed" | sed -n 5p)"

active="$(basename "$(readlink -f "$ROOT/current" 2>/dev/null || echo "")")"
active_sha="$(cat "$ROOT/current/.package.sha256" 2>/dev/null || true)"
if [ -n "$expected_sha" ] && [ "$active_sha" = "$expected_sha" ] && [ "$FORCE" -eq 0 ]; then
    log "线上已是最新内容（${active}），跳过"
    exit 0
fi

if [ "$DRY_RUN" -eq 1 ]; then
    log "可更新：${active:-无} -> ${tag}（${asset_name}）"
    exit 0
fi

mkdir -p "$ROOT/incoming"
package="$ROOT/incoming/$asset_name"
local_size="$(stat -c %s "$package" 2>/dev/null || echo 0)"
sha_of() { sha256sum "$1" | cut -d' ' -f1; }

# 滚动通道文件名固定：本地残留可能是旧构建，尺寸或校验不符都要重下
skip_download=0
if [ "$local_size" -gt 0 ] && [ "$expected_size" -gt 0 ]; then
    if [ "$local_size" -gt "$expected_size" ]; then
        rm -f "$package"
    elif [ "$local_size" -eq "$expected_size" ]; then
        if [ -z "$expected_sha" ] || [ "$(sha_of "$package")" = "$expected_sha" ]; then
            log "复用本地已下载的 ${asset_name}"
            skip_download=1
        else
            rm -f "$package"
        fi
    fi
fi

download() {
    local url="$1" code
    log "下载 ${tag}：${url}"
    # 416 表示本地已续传到底，交给校验判断
    code="$(curl -sSL --retry 2 --retry-delay 2 --retry-all-errors -C - \
        --max-time 900 -o "$package" -w '%{http_code}' "$url")" || code="000"
    case "$code" in
        200|206|416) return 0 ;;
        *) log "HTTP ${code}"; return 1 ;;
    esac
}

ok=$skip_download
if [ "$ok" -eq 0 ]; then
    for prefix in $MIRRORS; do
        if download "${prefix}${asset_url}"; then
            if [ -z "$expected_sha" ] || [ "$(sha_of "$package")" = "$expected_sha" ]; then
                ok=1
                break
            fi
            log "校验不符（可能是旧构建残留），删除后重试"
            rm -f "$package"
        else
            log "下载失败，换下一个源"
        fi
    done
fi
[ "$ok" -eq 1 ] || die "所有下载源均失败"

if [ -n "$expected_sha" ]; then
    log "sha256 校验通过（${expected_sha:0:12}…）"
else
    log "Release API 未提供 digest，跳过校验"
fi
# 写成 sha256sum -c 可校验的格式，deploy.sh 会再校验一次
printf '%s  %s\n' "$(sha_of "$package")" "$asset_name" > "$package.sha256"

"$ROOT/deploy/deploy.sh" "$package"

#!/usr/bin/env bash
# CI 推送入口：从标准输入接收发布包并交给 deploy.sh。
# 绑在受限制的 authorized_keys 上（command= 强制命令），该密钥拿不到交互 shell。
set -euo pipefail

ROOT="${MAIGO_ROOT:-/opt/maigo}"
SERVICE_USER=maibot

log() { printf '[ci-deploy] %s\n' "$*"; }
die() { printf '[ci-deploy] 错误: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "需要 root 运行"

mkdir -p "$ROOT/incoming"
package="$(mktemp "$ROOT/incoming/ci-XXXXXX.tar.gz")"
cleanup() { rm -f "$package" "$package.sha256"; }
trap cleanup EXIT

log "接收发布包"
cat > "$package"
[ -s "$package" ] || die "标准输入没有内容"
tar -tzf "$package" >/dev/null 2>&1 || die "收到的不像 gzip tar 包"

chown "$SERVICE_USER:$SERVICE_USER" "$package" 2>/dev/null || true
exec "$ROOT/deploy/deploy.sh" "$package"

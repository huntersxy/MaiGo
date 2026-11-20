#!/usr/bin/env bash
# CI 触发入口：只接受一个提交号，随后自行从 Release 镜像源拉包部署。
# 绑在受限制的 authorized_keys 上（command= 强制命令），该密钥拿不到交互 shell。
set -euo pipefail

ROOT="${MAIGO_ROOT:-/opt/maigo}"

log() { printf '[ci-trigger] %s\n' "$*"; }
die() { printf '[ci-trigger] 错误: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "需要 root 运行"

# 强制命令下 SSH_ORIGINAL_COMMAND 才是 CI 传来的参数，其余输入一律不接受
commit="${SSH_ORIGINAL_COMMAND:-}"
commit="${commit%% *}"
case "$commit" in
    [0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]*) ;;
    *) die "需要提交号参数" ;;
esac
[ "${#commit}" -ge 7 ] && [ "${#commit}" -le 40 ] || die "提交号长度不合法"

log "收到触发：${commit:0:7}"
exec "$ROOT/deploy/update.sh" --expect-commit "$commit"

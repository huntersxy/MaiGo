# sync_bundled_plugins.sh
# 将发布包内 bundled-plugins 同步到实例 plugins/，保留已有 config.toml 与 data/
# 由 deploy.sh 在切换 current 后调用。
set -euo pipefail

ROOT="${MAIGO_ROOT:-/opt/maigo}"
RELEASE_DIR="${1:-}"
SERVICE_USER="${MAIGO_SERVICE_USER:-maibot}"

log() { printf '[bundled-plugins] %s\n' "$*"; }

if [ -z "$RELEASE_DIR" ] || [ ! -d "$RELEASE_DIR" ]; then
    log "跳过：发布目录无效"
    exit 0
fi

BUNDLE="$RELEASE_DIR/bundled-plugins"
if [ ! -d "$BUNDLE" ]; then
    log "跳过：本包未附带 bundled-plugins"
    exit 0
fi

PLUGINS_ROOT="$ROOT/app/plugins"
mkdir -p "$PLUGINS_ROOT"

sync_one() {
    local src="$1"
    local manifest="$src/_manifest.json"
    [ -f "$manifest" ] || return 0
    local plugin_id
    plugin_id="$(python3 - "$manifest" <<'PY'
import json, sys
m = json.load(open(sys.argv[1], encoding='utf-8'))
info = m.get('plugin_info') if isinstance(m.get('plugin_info'), dict) else {}
pid = m.get('id') or info.get('id') or info.get('plugin_id') or ''
print(pid.strip())
PY
)"
    [ -n "$plugin_id" ] || {
        log "跳过 $(basename "$src"): 无插件 ID"
        return 0
    }
    local dest_name="${plugin_id//./_}"
    local dest="$PLUGINS_ROOT/$dest_name"
    local staging
    staging="$(mktemp -d "$PLUGINS_ROOT/.bundled-sync-XXXXXX")"
    # 先完整复制到临时目录，再保留现网配置与数据后原子替换
    cp -a "$src"/. "$staging/"
    find "$staging" -type d -name '__pycache__' -prune -exec rm -rf {} +
    find "$staging" -type f -name '*.pyc' -delete
    if [ -d "$dest" ]; then
        if [ -f "$dest/config.toml" ]; then
            cp -a "$dest/config.toml" "$staging/config.toml"
        fi
        if [ -d "$dest/data" ]; then
            rm -rf "$staging/data"
            cp -a "$dest/data" "$staging/data"
        fi
    fi
    rm -rf "$dest"
    mv "$staging" "$dest"
    chown -R "$SERVICE_USER:$SERVICE_USER" "$dest"
    log "已同步 $plugin_id -> plugins/$dest_name"
}

shopt -s nullglob
for src in "$BUNDLE"/*; do
    [ -d "$src" ] || continue
    sync_one "$src"
done

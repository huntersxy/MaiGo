package host

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePluginConfigPathSeparatesBuiltinConfig(t *testing.T) {
	projectRoot := t.TempDir()
	builtinRoot := filepath.Join(projectRoot, "plugins", "built_in")
	if err := os.MkdirAll(filepath.Join(builtinRoot, "qq_official_adapter"), 0o755); err != nil {
		t.Fatalf("创建内置插件目录失败: %v", err)
	}
	instanceRoot := t.TempDir()
	t.Setenv(EnvPluginProjectRoot, projectRoot)
	t.Setenv("MAIBOT_INSTANCE_DIR", instanceRoot)

	builtinPlugin := filepath.Join(builtinRoot, "qq_official_adapter")
	wantBuiltin := filepath.Join(instanceRoot, "config", "builtin_plugins", "qq_official_adapter", "config.toml")
	if got := ResolvePluginConfigPath(builtinPlugin); got != wantBuiltin {
		t.Fatalf("内置插件配置路径 = %q, 期望 %q", got, wantBuiltin)
	}

	thirdParty := filepath.Join(t.TempDir(), "third_party_plugin")
	wantThirdParty := filepath.Join(thirdParty, "config.toml")
	if got := ResolvePluginConfigPath(thirdParty); got != wantThirdParty {
		t.Fatalf("第三方插件配置路径 = %q, 期望 %q", got, wantThirdParty)
	}
}

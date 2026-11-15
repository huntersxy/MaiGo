package test

import (
	"testing"

	pluginhost "MaiGo/internal/plugin_runtime/host"
)

// TestPluginConfigHasVersion 判定哪些配置需要触发「按默认配置重建」。
// 现场问题：插件市场里的旧模板只写 [plugin] enabled，Runner 因缺少
// config_version 拒绝加载。
func TestPluginConfigHasVersion(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"带版本", "[plugin]\nenabled = true\nconfig_version = \"0.4.2\"\n", true},
		{"无版本", "[plugin]\nenabled = true\n", false},
		{"空版本", "[plugin]\nconfig_version = \"  \"\n", false},
		{"无 plugin 段", "[qq_official]\napp_id = \"\"\n", false},
		{"空文件", "", false},
		{"非法 TOML", "[plugin\nenabled = true", false},
		{"版本是数字", "[plugin]\nconfig_version = 2\n", false},
	}
	for _, tc := range cases {
		if got := pluginhost.PluginConfigHasVersion([]byte(tc.raw)); got != tc.want {
			t.Fatalf("%s: 期望 %v，实际 %v", tc.name, tc.want, got)
		}
	}
}

package pluginmarket

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// tomlPluginHeaderPattern 匹配 [plugin] 段头，允许行尾注释
var tomlPluginHeaderPattern = regexp.MustCompile(`^\s*\[\s*plugin\s*\]\s*(?:#.*)?$`)

// tomlSectionHeaderPattern 判定表头行，用于界定 [plugin] 段的结束位置
var tomlSectionHeaderPattern = regexp.MustCompile(`^\s*\[`)

// tomlEnabledAssignment 匹配段内 enabled 赋值，保留行尾空白与注释
var tomlEnabledAssignment = regexp.MustCompile(`^(\s*enabled\s*=\s*)(\S+)(\s*(?:#.*)?)$`)

// setPluginEnabled 只改写 [plugin] 表中的 enabled，保留其余内容与注释
func setPluginEnabled(raw string, enabled bool) string {
	value := "false"
	if enabled {
		value = "true"
	}
	newline := "\n"
	if strings.Contains(raw, "\r\n") {
		newline = "\r\n"
	}
	lines := strings.Split(raw, "\n")
	header := -1
	for index, line := range lines {
		if tomlPluginHeaderPattern.MatchString(strings.TrimSuffix(line, "\r")) {
			header = index
			break
		}
	}
	if header < 0 {
		if raw != "" && !strings.HasSuffix(raw, newline) {
			raw += newline
		}
		if raw != "" && !strings.HasSuffix(raw, newline+newline) {
			raw += newline
		}
		return raw + "[plugin]" + newline + "enabled = " + value + newline
	}
	end := len(lines)
	for index := header + 1; index < len(lines); index++ {
		if tomlSectionHeaderPattern.MatchString(strings.TrimSuffix(lines[index], "\r")) {
			end = index
			break
		}
	}
	for index := header + 1; index < end; index++ {
		line := strings.TrimSuffix(lines[index], "\r")
		matches := tomlEnabledAssignment.FindStringSubmatch(line)
		if matches == nil {
			continue
		}
		updated := matches[1] + value + matches[3]
		if strings.HasSuffix(lines[index], "\r") {
			updated += "\r"
		}
		lines[index] = updated
		return strings.Join(lines, "\n")
	}
	insertAt := end
	carriageReturn := ""
	if insertAt > header+1 && strings.TrimSpace(lines[insertAt-1]) == "" {
		insertAt--
	}
	if insertAt < len(lines) && strings.HasSuffix(lines[insertAt], "\r") {
		carriageReturn = "\r"
	}
	lines = append(lines[:insertAt], append([]string{"enabled = " + value + carriageReturn}, lines[insertAt:]...)...)
	return strings.Join(lines, "\n")
}

// writePluginDisabledForUninstall 对齐 _write_plugin_disabled_for_uninstall：落盘 enabled=false 避免重启后再次加载
func writePluginDisabledForUninstall(pluginPath string) error {
	configPath, err := resolvePluginFilePath(pluginPath, "config.toml", true)
	if err != nil {
		return err
	}
	raw := ""
	if data, readErr := os.ReadFile(configPath); readErr == nil {
		raw = string(data)
	} else if !os.IsNotExist(readErr) {
		return readErr
	}
	updated := setPluginEnabled(raw, false)
	if updated == raw {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(configPath, []byte(updated), 0o644)
}

package host

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

const (
	builtinPluginManagementID          = "builtin.plugin-management"
	builtinPluginManagementDisplayName = "插件和组件管理 (Plugin and Component Management)"
)

const pluginManagementHelpAll = "管理命令帮助\n" +
	"/pm help 管理命令提示\n" +
	"/pm plugin 插件管理命令\n" +
	"/pm component 组件管理命令\n" +
	"/pm cmd 查看插件指令列表（支持 pic 图片）\n" +
	"/pm alias 管理命令别名\n" +
	"使用 /pm plugin help、/pm component help、/pm cmd help、/pm alias help 或 /pm config help 获取具体帮助"

const pluginManagementHelpCommand = "插件指令帮助\n" +
	"/pm cmd 按插件列出所有已注册指令\n" +
	"/pm cmd <插件> 列出指定插件的指令\n" +
	"/pm cmd pic 按插件生成指令列表图片\n" +
	"/pm cmd <插件> pic 生成指定插件的指令列表图片\n" +
	"<插件> 可用完整ID或最后一段唯一短名称，例如 builtin.plugin-management 可写 plugin-management。\n" +
	"短名称重名时请使用完整ID；pic、help 为保留字，同名插件请使用完整ID。\n" +
	"兼容旧命令 /pm command list [pic]、/pm command plugin <插件> [pic]。\n" +
	"列表包含已禁用指令。"

const pluginManagementHelpPlugin = "插件管理命令帮助\n" +
	"/pm plugin help 插件管理命令提示\n" +
	"/pm plugin list 列出所有注册的插件\n" +
	"/pm plugin list_enabled 列出所有加载（启用）的插件\n" +
	"/pm plugin load <plugin_name> 加载指定插件\n" +
	"/pm plugin unload <plugin_name> 卸载指定插件\n" +
	"/pm plugin reload <plugin_name> 重新加载指定插件"

const pluginManagementHelpConfig = "插件配置命令帮助\n" +
	"/pm config help 插件配置命令提示\n" +
	"/pm config get <plugin_name> [key] 读取指定插件配置或点分隔配置项\n" +
	"/pm config set <plugin_name> <key> <value_json> 修改指定插件配置项\n" +
	"  - <key> 使用点分隔路径，例如 plugin.enabled\n" +
	"  - <value_json> 优先按 JSON 解析，例如 true、123、[\"文本\"]；解析失败时按字符串保存\n" +
	"  - 指令别名建议使用 /pm alias add /别名 /pm plugin list"

const pluginManagementHelpComponent = "组件管理命令帮助\n" +
	"/pm component help 组件管理命令提示\n" +
	"/pm component list 列出所有注册的组件\n" +
	"/pm component list enabled <可选: type> 列出所有启用的组件\n" +
	"/pm component list disabled <可选: type> 列出所有禁用的组件\n" +
	"  - <type> 可选项: local，代表当前聊天中的；global，代表全局的\n" +
	"  - <type> 不填时为 global\n" +
	"/pm component list type <component_type> 列出已经注册的指定类型的组件\n" +
	"/pm component enable global <component_name> <component_type> 全局启用组件\n" +
	"/pm component enable local <component_name> <component_type> 本聊天启用组件\n" +
	"/pm component disable global <component_name> <component_type> 全局禁用组件\n" +
	"/pm component disable local <component_name> <component_type> 本聊天禁用组件\n" +
	"  - <component_type> 可选项: tool, command, event_handler"

const pluginManagementHelpAlias = "指令别名命令帮助\n" +
	"/pm alias list 查看已配置的指令别名\n" +
	"/pm alias add <alias> <pm_command> 添加别名，例如 /pm alias add /插件列表 /pm plugin list\n" +
	"/pm alias remove <alias> 删除别名，例如 /pm alias remove /插件列表\n" +
	"  - <alias> 必须以 / 开头，且不能是 /pm\n" +
	"  - <pm_command> 必须以 /pm 开头；如需给 /pm 本身起别名，可使用 /pm alias add /插件管理 /pm"

// pluginManagementState 宿主内置插件管理命令的进程内别名状态
//
// Python 内置插件把别名写入自身 config.toml；Go 本地兜底命令没有插件配置目录，
// 因此在进程内保存并在配置中明确提示未持久化。
type pluginManagementState struct {
	mu        sync.RWMutex
	prefixes  map[string]string
	shortcuts map[string]string
}

func newPluginManagementState() *pluginManagementState {
	return &pluginManagementState{
		prefixes:  map[string]string{"/插件管理": "/pm"},
		shortcuts: map[string]string{},
	}
}

func (s *pluginManagementState) snapshot() map[string]string {
	if s == nil {
		return map[string]string{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.prefixes)+len(s.shortcuts))
	for alias, target := range s.prefixes {
		out[alias] = target
	}
	for alias, target := range s.shortcuts {
		out[alias] = target
	}
	return out
}

// replaceAliases 用配置文件中的别名整体替换进程内映射
func (s *pluginManagementState) replaceAliases(prefixes, shortcuts map[string]string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prefixes = make(map[string]string, len(prefixes))
	for alias, target := range prefixes {
		s.prefixes[alias] = target
	}
	s.shortcuts = make(map[string]string, len(shortcuts))
	for alias, target := range shortcuts {
		s.shortcuts[alias] = target
	}
}

func (s *pluginManagementState) add(alias, target string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shortcuts[alias] = target
}

func (s *pluginManagementState) remove(alias string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.shortcuts[alias]; !ok {
		return false
	}
	delete(s.shortcuts, alias)
	return true
}

func (s *pluginManagementState) resolve(raw string) string {
	command := strings.TrimSpace(raw)
	if command == "" || isPluginManagementCommand(command) {
		return command
	}
	aliases := s.snapshot()
	keys := make([]string, 0, len(aliases))
	for alias := range aliases {
		keys = append(keys, alias)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) == len(keys[j]) {
			return keys[i] < keys[j]
		}
		return len(keys[i]) > len(keys[j])
	})
	for _, alias := range keys {
		if !commandStartsWithAlias(command, alias) {
			continue
		}
		suffix := strings.TrimSpace(command[len(alias):])
		return strings.TrimSpace(aliases[alias] + " " + suffix)
	}
	return command
}

// splitPluginCommand 近似 shlex.split，支持单双引号与反斜杠转义；引号不闭合时回退空白切分
func splitPluginCommand(raw string) []string {
	var args []string
	var token strings.Builder
	var quote rune
	escaped := false
	inToken := false
	for _, char := range raw {
		if escaped {
			token.WriteRune(char)
			escaped = false
			inToken = true
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
				continue
			}
			if char == '\\' && quote == '"' {
				escaped = true
				continue
			}
			token.WriteRune(char)
			inToken = true
			continue
		}
		switch char {
		case '\\':
			escaped = true
			inToken = true
		case '\'', '"':
			quote = char
			inToken = true
		case ' ', '\t', '\r', '\n':
			if inToken {
				args = append(args, token.String())
				token.Reset()
				inToken = false
			}
		default:
			token.WriteRune(char)
			inToken = true
		}
	}
	if quote != 0 {
		return strings.Fields(raw)
	}
	if escaped {
		token.WriteRune('\\')
		inToken = true
	}
	if inToken {
		args = append(args, token.String())
	}
	return args
}

func commandStartsWithAlias(command, alias string) bool {
	if alias == "" || !strings.HasPrefix(command, alias) {
		return false
	}
	if len(command) == len(alias) {
		return true
	}
	return isPluginCommandSpace(rune(command[len(alias)]))
}

func isPluginCommandSpace(char rune) bool {
	return char == ' ' || char == '\t' || char == '\r' || char == '\n'
}

func isPluginManagementCommand(command string) bool {
	return commandStartsWithAlias(strings.TrimSpace(command), "/pm")
}

func normalizePluginAliasTarget(raw string) string {
	return strings.Join(splitPluginCommand(strings.TrimSpace(raw)), " ")
}

func validatePluginAliasShortcut(alias, target string) string {
	if alias == "" {
		return "别名不能为空"
	}
	if !strings.HasPrefix(alias, "/") {
		return "别名必须以 / 开头"
	}
	if strings.IndexFunc(alias, func(char rune) bool { return char == ' ' || char == '\t' || char == '\r' || char == '\n' }) >= 0 {
		return "别名不能包含空格"
	}
	if alias == "/pm" {
		return "不能把 /pm 本身设置为别名"
	}
	if target == "" {
		return "目标命令不能为空"
	}
	if !isPluginManagementCommand(target) {
		return "目标命令必须以 /pm 开头"
	}
	return ""
}

func pluginManagementCommandError(format string, args ...any) string {
	return strings.TrimSpace(fmt.Sprintf(format, args...))
}

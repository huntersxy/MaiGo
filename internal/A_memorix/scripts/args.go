package scripts

// 最小 argparse 复刻，覆盖 scripts 用到的选项形态
// 差异说明：不支持 Python argparse 的长选项前缀缩写与 nargs，
// 也未实现互斥组；未覆盖的形态直接报错而不是猜测语义

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// ArgKind 选项取值类型
type ArgKind string

const (
	// ArgString 取一个字符串值
	ArgString ArgKind = "string"
	// ArgInt 取一个整数值
	ArgInt ArgKind = "int"
	// ArgBool store_true 开关
	ArgBool ArgKind = "bool"
)

// ArgSpec 描述一个命令行参数，对齐 argparse.add_argument 的常用子集
type ArgSpec struct {
	// Names 选项名，如 {"--data-dir"} 或 {"--type", "-t"}；位置参数为空
	Names []string
	// Key 取值键，取首个长选项去掉前导横线
	Key string
	// Help 帮助文本
	Help string
	// Kind 取值类型
	Kind ArgKind
	// Default 默认值，位置参数忽略
	Default any
	// Positional 是否为位置参数
	Positional bool
	// Repeat 是否可重复出现（对齐 argparse action="append"）
	Repeat bool
}

// primaryName 返回首选选项名，短选项优先，与 argparse 帮助输出一致
func (s *ArgSpec) primaryName() string {
	if len(s.Names) == 0 {
		return s.Key
	}
	return s.Names[0]
}

// metavar 返回帮助里的取值占位名，对齐 argparse 默认的 KEY 大写形态
func (s *ArgSpec) metavar() string {
	return strings.ToUpper(strings.ReplaceAll(s.Key, "-", "_"))
}

// display 返回帮助行左侧文本
func (s *ArgSpec) display() string {
	if s.Positional {
		return s.Key
	}
	parts := make([]string, 0, len(s.Names))
	for _, name := range s.Names {
		if s.Kind == ArgBool {
			parts = append(parts, name)
			continue
		}
		parts = append(parts, name+" "+s.metavar())
	}
	return strings.Join(parts, ", ")
}

// ArgParser 命令行解析器
type ArgParser struct {
	// Prog 程序名，用于 usage 与错误前缀
	Prog string
	// Description 帮助正文
	Description string
	// Stdout 帮助输出，未设置时写 os.Stdout
	Stdout io.Writer
	// Stderr 错误输出，未设置时写 os.Stderr
	Stderr      io.Writer
	specs       []*ArgSpec
	positionals []*ArgSpec
	byName      map[string]*ArgSpec
}

// NewArgParser 创建解析器
func NewArgParser(prog string, description string) *ArgParser {
	return &ArgParser{Prog: prog, Description: description, byName: map[string]*ArgSpec{}}
}

// AddString 注册带默认值的字符串选项
func (p *ArgParser) AddString(names []string, defaultValue string, help string) {
	p.add(&ArgSpec{Names: names, Key: optionKey(names), Help: help, Kind: ArgString, Default: defaultValue})
}

// AddStringList 注册可重复的字符串选项，对齐 action="append"
func (p *ArgParser) AddStringList(names []string, help string) {
	p.add(&ArgSpec{Names: names, Key: optionKey(names), Help: help, Kind: ArgString, Default: []string{}, Repeat: true})
}

// AddInt 注册带默认值的整数选项
func (p *ArgParser) AddInt(names []string, defaultValue int, help string) {
	p.add(&ArgSpec{Names: names, Key: optionKey(names), Help: help, Kind: ArgInt, Default: defaultValue})
}

// AddBool 注册 store_true 开关
func (p *ArgParser) AddBool(names []string, help string) {
	p.add(&ArgSpec{Names: names, Key: optionKey(names), Help: help, Kind: ArgBool})
}

// AddPositional 注册位置参数
func (p *ArgParser) AddPositional(name string, help string) {
	spec := &ArgSpec{Key: name, Help: help, Kind: ArgString, Positional: true}
	p.positionals = append(p.positionals, spec)
}

// add 注册选项并建立名称索引
func (p *ArgParser) add(spec *ArgSpec) {
	p.specs = append(p.specs, spec)
	for _, name := range spec.Names {
		p.byName[name] = spec
	}
}

// optionKey 从选项名推导取值键
func optionKey(names []string) string {
	for _, name := range names {
		if strings.HasPrefix(name, "--") {
			return strings.TrimPrefix(name, "--")
		}
	}
	if len(names) > 0 {
		return strings.TrimLeft(names[0], "-")
	}
	return ""
}

// ParsedArgs 解析结果
type ParsedArgs struct {
	values   map[string]any
	provided map[string]bool
}

// String 读取字符串值
func (a *ParsedArgs) String(key string) string {
	if a == nil {
		return ""
	}
	value, ok := a.values[key]
	if !ok || value == nil {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return fmt.Sprint(value)
	}
	return text
}

// Int 读取整数值
func (a *ParsedArgs) Int(key string) int {
	if a == nil {
		return 0
	}
	value, ok := a.values[key]
	if !ok || value == nil {
		return 0
	}
	number, ok := value.(int)
	if !ok {
		return 0
	}
	return number
}

// Bool 读取开关值
func (a *ParsedArgs) Bool(key string) bool {
	if a == nil {
		return false
	}
	value, ok := a.values[key]
	if !ok || value == nil {
		return false
	}
	flag, ok := value.(bool)
	return ok && flag
}

// All 读取可重复字符串选项的全部取值
func (a *ParsedArgs) All(key string) []string {
	if a == nil {
		return nil
	}
	value, ok := a.values[key]
	if !ok || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, fmt.Sprint(item))
		}
		return out
	default:
		return []string{fmt.Sprint(value)}
	}
}

// Provided 判断用户是否显式传入了该选项，用于区分 None 默认值与显式取值
func (a *ParsedArgs) Provided(key string) bool {
	if a == nil {
		return false
	}
	return a.provided[key]
}

// Has 判断键是否存在
func (a *ParsedArgs) Has(key string) bool {
	if a == nil {
		return false
	}
	_, ok := a.values[key]
	return ok
}

// Parse 解析参数
// 返回 (结果, 退出码, 是否已处理)；已处理为 true 时调用方应直接返回退出码
func (p *ArgParser) Parse(argv []string) (*ParsedArgs, int, bool) {
	for _, arg := range argv {
		if arg == "-h" || arg == "--help" {
			p.WriteHelp(p.stdout())
			return nil, 0, true
		}
	}

	parsed := &ParsedArgs{values: map[string]any{}, provided: map[string]bool{}}
	for _, spec := range p.specs {
		if spec.Kind == ArgBool {
			parsed.values[spec.Key] = false
			continue
		}
		parsed.values[spec.Key] = spec.Default
	}

	var positionals []string
	endOfOptions := false
	for index := 0; index < len(argv); index++ {
		arg := argv[index]
		if endOfOptions {
			positionals = append(positionals, arg)
			continue
		}
		if arg == "--" {
			endOfOptions = true
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, inline, hasInline := strings.Cut(arg, "=")
			spec, ok := p.byName[name]
			if !ok {
				return nil, p.fail("unrecognized arguments: %s", name), true
			}
			if spec.Kind == ArgBool {
				if hasInline {
					return nil, p.fail("argument %s: ignored explicit argument '%s'", name, inline), true
				}
				parsed.values[spec.Key] = true
				parsed.provided[spec.Key] = true
				continue
			}
			raw := inline
			if !hasInline {
				nextIndex, value, code, ok := p.takeValue(argv, index, name)
				if !ok {
					return nil, code, true
				}
				index = nextIndex
				raw = value
			}
			if code, failed := p.applyValue(parsed, spec, name, raw); failed {
				return nil, code, true
			}
			continue
		}
		if len(arg) > 1 && arg[0] == '-' && !isNegativeNumber(arg) {
			name := arg[:2]
			spec, ok := p.byName[name]
			if !ok {
				return nil, p.fail("unrecognized arguments: %s", arg), true
			}
			if spec.Kind == ArgBool {
				parsed.values[spec.Key] = true
				parsed.provided[spec.Key] = true
				continue
			}
			raw := arg[2:]
			if raw == "" {
				nextIndex, value, code, ok := p.takeValue(argv, index, name)
				if !ok {
					return nil, code, true
				}
				index = nextIndex
				raw = value
			}
			if code, failed := p.applyValue(parsed, spec, name, raw); failed {
				return nil, code, true
			}
			continue
		}
		positionals = append(positionals, arg)
	}

	if len(positionals) > len(p.positionals) {
		return nil, p.fail("unrecognized arguments: %s", strings.Join(positionals[len(p.positionals):], " ")), true
	}
	var missing []string
	for index, spec := range p.positionals {
		if index >= len(positionals) {
			missing = append(missing, spec.Key)
			continue
		}
		parsed.values[spec.Key] = positionals[index]
	}
	if len(missing) > 0 {
		return nil, p.fail("the following arguments are required: %s", strings.Join(missing, ", ")), true
	}
	return parsed, 0, false
}

// takeValue 读取选项值；下一个 token 缺失或像选项时报错
func (p *ArgParser) takeValue(argv []string, index int, name string) (int, string, int, bool) {
	if index+1 >= len(argv) || looksLikeOption(argv[index+1]) {
		return index, "", p.fail("argument %s: expected one argument", name), false
	}
	return index + 1, argv[index+1], 0, true
}

// applyValue 按类型写入取值
func (p *ArgParser) applyValue(parsed *ParsedArgs, spec *ArgSpec, name string, raw string) (int, bool) {
	parsed.provided[spec.Key] = true
	if spec.Kind == ArgInt {
		number, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return p.fail("argument %s: invalid int value: '%s'", name, raw), true
		}
		parsed.values[spec.Key] = number
		return 0, false
	}
	if spec.Repeat {
		existing, _ := parsed.values[spec.Key].([]string)
		parsed.values[spec.Key] = append(existing, raw)
		return 0, false
	}
	parsed.values[spec.Key] = raw
	return 0, false
}

// looksLikeOption 判断 token 是否像选项（负数为合法取值）
func looksLikeOption(token string) bool {
	if len(token) <= 1 || token[0] != '-' {
		return false
	}
	return !isNegativeNumber(token)
}

// isNegativeNumber 判断 token 是否为负数字面量
func isNegativeNumber(token string) bool {
	if len(token) < 2 || token[0] != '-' {
		return false
	}
	if _, err := strconv.ParseFloat(token, 64); err != nil {
		return false
	}
	return true
}

// WriteHelp 输出 usage 与选项帮助
func (p *ArgParser) WriteHelp(w io.Writer) {
	fmt.Fprintln(w, p.usage())
	if strings.TrimSpace(p.Description) != "" {
		fmt.Fprintf(w, "\n%s\n", p.Description)
	}
	if len(p.positionals) > 0 {
		fmt.Fprintln(w, "\npositional arguments:")
		width := 0
		for _, spec := range p.positionals {
			if candidate := len(spec.display()); candidate > width {
				width = candidate
			}
		}
		for _, spec := range p.positionals {
			writeHelpLine(w, width, spec)
		}
	}
	fmt.Fprintln(w, "\noptions:")
	entries := []*ArgSpec{{Names: []string{"-h", "--help"}, Key: "help", Kind: ArgBool, Help: "show this help message and exit"}}
	entries = append(entries, p.specs...)
	width := 0
	for _, spec := range entries {
		if candidate := len(spec.display()); candidate > width {
			width = candidate
		}
	}
	if width > 28 {
		width = 28
	}
	for _, spec := range entries {
		writeHelpLine(w, width, spec)
	}
}

// writeHelpLine 对齐 argparse：help 为空时不留行尾空白
func writeHelpLine(w io.Writer, width int, spec *ArgSpec) {
	if strings.TrimSpace(spec.Help) == "" {
		fmt.Fprintf(w, "  %s\n", spec.display())
		return
	}
	fmt.Fprintf(w, "  %-*s  %s\n", width, spec.display(), spec.Help)
}

// usage 生成类似 argparse 的 usage 行
func (p *ArgParser) usage() string {
	parts := []string{"usage:", p.Prog, "[-h]"}
	for _, spec := range p.specs {
		name := spec.primaryName()
		if spec.Kind == ArgBool {
			parts = append(parts, "["+name+"]")
			continue
		}
		parts = append(parts, "["+name+" "+spec.metavar()+"]")
	}
	for _, spec := range p.positionals {
		parts = append(parts, spec.Key)
	}
	return strings.Join(parts, " ")
}

// stdout 返回帮助输出目标
func (p *ArgParser) stdout() io.Writer {
	if p.Stdout != nil {
		return p.Stdout
	}
	return os.Stdout
}

// stderr 返回错误输出目标
func (p *ArgParser) stderr() io.Writer {
	if p.Stderr != nil {
		return p.Stderr
	}
	return os.Stderr
}

// fail 输出 usage 与错误行，返回 argparse 约定退出码 2
func (p *ArgParser) fail(format string, args ...any) int {
	fmt.Fprintln(p.stderr(), p.usage())
	fmt.Fprintf(p.stderr(), "%s: error: %s\n", p.Prog, fmt.Sprintf(format, args...))
	return 2
}

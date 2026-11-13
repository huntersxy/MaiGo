package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// Style 终端语义样式名，对齐 MaiBot src/cli/console.py 的 rich Theme
type Style string

const (
	StyleInfo     Style = "info"
	StyleSuccess  Style = "success"
	StyleWarning  Style = "warning"
	StyleError    Style = "error"
	StyleMuted    Style = "muted"
	StyleAccent   Style = "accent"
	StyleCyan     Style = "cyan"
	StyleMagenta  Style = "magenta"
	StyleBold     Style = "bold"
	StyleItalic   Style = "italic"
	StyleBoldCyan Style = "bold_cyan"
)

// Theme 语义样式到 ANSI 参数的映射
type Theme map[Style]string

// DefaultTheme 与 Python rich Theme 对齐
var DefaultTheme = Theme{
	StyleInfo:     "36",
	StyleSuccess:  "32",
	StyleWarning:  "33",
	StyleError:    "1;31",
	StyleMuted:    "2",
	StyleAccent:   "1;35",
	StyleCyan:     "36",
	StyleMagenta:  "35",
	StyleBold:     "1",
	StyleItalic:   "3",
	StyleBoldCyan: "1;36",
}

// BoxStyle 面板边框字符集
type BoxStyle struct {
	TopLeft     string
	TopRight    string
	BottomLeft  string
	BottomRight string
	Horizontal  string
	Vertical    string
}

// DoubleEdgeBox 双线边框，对齐 rich.box.DOUBLE_EDGE
var DoubleEdgeBox = BoxStyle{TopLeft: "╔", TopRight: "╗", BottomLeft: "╚", BottomRight: "╝", Horizontal: "═", Vertical: "║"}

// RoundedBox 圆角边框，对齐 rich 默认 Panel 边框
var RoundedBox = BoxStyle{TopLeft: "╭", TopRight: "╮", BottomLeft: "╰", BottomRight: "╯", Horizontal: "─", Vertical: "│"}

// PanelOptions 面板渲染参数，padding 对齐 rich 的 (vertical, horizontal)
type PanelOptions struct {
	Title             string
	BorderStyle       Style
	PaddingTop        int
	PaddingHorizontal int
	Box               BoxStyle
}

// Console 提供 rich 风格的终端输出；未启用颜色时只保留排版
type Console struct {
	Out   io.Writer
	Err   io.Writer
	Width int
	Color bool
	Theme Theme
}

// NewConsole 创建输出到指定 writer 的控制台，颜色根据 writer 是否为终端判断
func NewConsole(out, errOut io.Writer) *Console {
	if out == nil {
		out = io.Discard
	}
	if errOut == nil {
		errOut = out
	}
	return &Console{
		Out:   out,
		Err:   errOut,
		Color: SupportsColor(out),
		Theme: DefaultTheme,
	}
}

// NewStdConsole 创建标准输出控制台
func NewStdConsole() *Console {
	return NewConsole(os.Stdout, os.Stderr)
}

// DefaultConsole 全局终端输出
var DefaultConsole = NewStdConsole()

// SupportsColor 判断 writer 是否应输出 ANSI 颜色
func SupportsColor(writer io.Writer) bool {
	if strings.TrimSpace(os.Getenv("NO_COLOR")) != "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb") {
		return false
	}
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// styleCode 读取样式对应的 ANSI 参数
func (c *Console) styleCode(style Style) string {
	theme := c.Theme
	if theme == nil {
		theme = DefaultTheme
	}
	if code, ok := theme[style]; ok {
		return code
	}
	return ""
}

// decorate 按语义样式包裹文本，颜色关闭时原样返回
func (c *Console) decorate(style Style, text string) string {
	if text == "" || !c.Color {
		return text
	}
	code := c.styleCode(style)
	if code == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

// Write 实现 io.Writer，便于把控制台传给按 writer 输出的函数
func (c *Console) Write(data []byte) (int, error) {
	return c.Out.Write(data)
}

// Print 写入文本，不自动换行
func (c *Console) Print(values ...any) {
	_, _ = fmt.Fprint(c.Out, values...)
}

// Println 写入文本并换行
func (c *Console) Println(values ...any) {
	_, _ = fmt.Fprintln(c.Out, values...)
}

// Printf 格式化写入
func (c *Console) Printf(format string, values ...any) {
	_, _ = fmt.Fprintf(c.Out, format, values...)
}

// Errorf 格式化写入错误输出
func (c *Console) Errorf(format string, values ...any) {
	_, _ = fmt.Fprintf(c.Err, format, values...)
}

// PrintStyled 按语义样式写入一行
func (c *Console) PrintStyled(style Style, text string) {
	_, _ = fmt.Fprintln(c.Out, c.decorate(style, text))
}

// Prompt 输出输入提示，不换行
func (c *Console) Prompt(style Style, text string) {
	_, _ = fmt.Fprint(c.Out, c.decorate(style, text))
}

// width 返回面板总宽度
func (c *Console) width() int {
	if c.Width > 0 {
		return c.Width
	}
	if columns := envColumns(); columns > 0 {
		return columns
	}
	return 80
}

// envColumns 读取 COLUMNS，非法值返回 0
func envColumns() int {
	raw := strings.TrimSpace(os.Getenv("COLUMNS"))
	if raw == "" {
		return 0
	}
	var width int
	if _, err := fmt.Sscanf(raw, "%d", &width); err != nil {
		return 0
	}
	if width < 20 {
		return 20
	}
	return width
}

// Panel 渲染并输出面板
func (c *Console) Panel(content string, options PanelOptions) {
	_, _ = io.WriteString(c.Out, c.RenderPanel(content, options))
}

// RenderPanel 渲染面板文本，行为对齐 rich.panel.Panel 的基础排版
func (c *Console) RenderPanel(content string, options PanelOptions) string {
	box := options.Box
	if box.Horizontal == "" {
		box = RoundedBox
	}
	if options.PaddingTop < 0 {
		options.PaddingTop = 0
	}
	if options.PaddingHorizontal < 0 {
		options.PaddingHorizontal = 0
	}
	panelWidth := c.width()
	if panelWidth < 12 {
		panelWidth = 12
	}
	innerWidth := panelWidth - 2 - options.PaddingHorizontal*2
	if innerWidth < 4 {
		innerWidth = 4
	}

	content = strings.TrimSuffix(content, "\n")
	var contentLines []string
	if content == "" {
		contentLines = []string{""}
	} else {
		for _, line := range strings.Split(content, "\n") {
			contentLines = append(contentLines, wrapDisplay(line, innerWidth)...)
		}
	}

	var builder strings.Builder
	title := truncateDisplay(strings.TrimSpace(options.Title), panelWidth-6)
	builder.WriteString(c.decorate(options.BorderStyle, box.TopLeft))
	if title != "" {
		builder.WriteString(c.decorate(options.BorderStyle, box.Horizontal))
		builder.WriteString(" ")
		builder.WriteString(c.decorate(StyleBold, title))
		builder.WriteString(" ")
		remaining := panelWidth - 5 - displayWidth(title)
		if remaining < 1 {
			remaining = 1
		}
		builder.WriteString(c.decorate(options.BorderStyle, strings.Repeat(box.Horizontal, remaining)))
	} else {
		builder.WriteString(c.decorate(options.BorderStyle, strings.Repeat(box.Horizontal, panelWidth-2)))
	}
	builder.WriteString(c.decorate(options.BorderStyle, box.TopRight))
	builder.WriteString("\n")

	padding := strings.Repeat(" ", options.PaddingHorizontal)
	emptyLine := c.decorate(options.BorderStyle, box.Vertical) + strings.Repeat(" ", panelWidth-2) + c.decorate(options.BorderStyle, box.Vertical)
	for index := 0; index < options.PaddingTop; index++ {
		builder.WriteString(emptyLine)
		builder.WriteString("\n")
	}
	for _, line := range contentLines {
		builder.WriteString(c.decorate(options.BorderStyle, box.Vertical))
		builder.WriteString(padding)
		builder.WriteString(padDisplayRight(line, innerWidth))
		builder.WriteString(padding)
		builder.WriteString(c.decorate(options.BorderStyle, box.Vertical))
		builder.WriteString("\n")
	}
	for index := 0; index < options.PaddingTop; index++ {
		builder.WriteString(emptyLine)
		builder.WriteString("\n")
	}
	builder.WriteString(c.decorate(options.BorderStyle, box.BottomLeft))
	builder.WriteString(c.decorate(options.BorderStyle, strings.Repeat(box.Horizontal, panelWidth-2)))
	builder.WriteString(c.decorate(options.BorderStyle, box.BottomRight))
	builder.WriteString("\n")
	return builder.String()
}

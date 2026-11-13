package scripts

// 脚本输出上下文
// Python 脚本统一写 stdout/stderr，Go 版通过 context 注入输出目标，
// 使 CLI、WebUI 与测试都能在不改 RunX 签名的前提下捕获输出

import (
	"context"
	"fmt"
	"io"
	"os"
)

// scriptIOKey 脚本输出上下文键
type scriptIOKey struct{}

// scriptIO 携带脚本的 stdout/stderr
type scriptIO struct {
	stdout io.Writer
	stderr io.Writer
}

// WithScriptIO 使用指定输出执行脚本命令
func WithScriptIO(ctx context.Context, stdout io.Writer, stderr io.Writer) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	return context.WithValue(ctx, scriptIOKey{}, scriptIO{stdout: stdout, stderr: stderr})
}

// scriptOutput 读取当前输出目标，未注入时回退进程标准输出
func scriptOutput(ctx context.Context) scriptIO {
	if ctx != nil {
		if value, ok := ctx.Value(scriptIOKey{}).(scriptIO); ok {
			return value
		}
	}
	return scriptIO{stdout: os.Stdout, stderr: os.Stderr}
}

// ScriptStdout 返回当前脚本标准输出
func ScriptStdout(ctx context.Context) io.Writer { return scriptOutput(ctx).stdout }

// ScriptStderr 返回当前脚本标准错误
func ScriptStderr(ctx context.Context) io.Writer { return scriptOutput(ctx).stderr }

// newScriptParser 构造写当前输出的参数解析器
func newScriptParser(ctx context.Context, prog string, description string) *ArgParser {
	parser := NewArgParser(prog, description)
	parser.Stdout = ScriptStdout(ctx)
	parser.Stderr = ScriptStderr(ctx)
	return parser
}

// newScriptLogger 构造写当前标准错误的脚本日志器
func newScriptLogger(ctx context.Context, name string) *ScriptLogger {
	return NewScriptLoggerTo(name, ScriptStderr(ctx))
}

// scriptPrintf 写脚本标准输出
func scriptPrintf(ctx context.Context, format string, args ...any) {
	fmt.Fprintf(ScriptStdout(ctx), format, args...)
}

// scriptPrintln 写脚本标准输出并换行
func scriptPrintln(ctx context.Context, args ...any) {
	fmt.Fprintln(ScriptStdout(ctx), args...)
}

// scriptErrorf 写脚本标准错误
func scriptErrorf(ctx context.Context, format string, args ...any) {
	fmt.Fprintf(ScriptStderr(ctx), format, args...)
}

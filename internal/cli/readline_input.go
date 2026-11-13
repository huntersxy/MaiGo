package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/chzyer/readline"
)

// BotConsolePrompt 与 Python ANSI("\x1b[1;36m> \x1b[0m") 一致
const BotConsolePrompt = "\x1b[1;36m> \x1b[0m"

// ReadlineInputReader 用 readline 读取终端输入，提供 Tab 补全与内存历史
// 对齐 MaiBot src/cli/bot_console.py 的 PromptSession(complete_while_typing=False, InMemoryHistory)
type ReadlineInputReader struct {
	instance *readline.Instance

	mu      sync.Mutex
	started bool
	closed  bool
}

// NewReadlineInputReader 创建带补全的终端读取器；completions 每次按当前会话动态求值
func NewReadlineInputReader(completions func() []string, out, errOut io.Writer) (*ReadlineInputReader, error) {
	if completions == nil {
		completions = func() []string { return nil }
	}
	instance, err := readline.NewEx(&readline.Config{
		Prompt:       BotConsolePrompt,
		AutoComplete: consoleCompleter{completions: completions},
		// HistoryLimit -1：只在内存保留历史，不落盘
		HistoryLimit: -1,
		Stdin:        os.Stdin,
		Stdout:       out,
		Stderr:       errOut,
	})
	if err != nil {
		return nil, err
	}
	return &ReadlineInputReader{instance: instance}, nil
}

// OwnsPrompt 自带提示符，调用方不再单独打印
func (r *ReadlineInputReader) OwnsPrompt() bool { return true }

// Start 保持与 InputReader 相同的启动语义
func (r *ReadlineInputReader) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = true
	r.closed = false
}

// Next 阻塞读取一行；Ctrl-D 返回 io.EOF，Ctrl-C 按退出终端处理
func (r *ReadlineInputReader) Next(ctx context.Context) (string, error) {
	r.mu.Lock()
	started := r.started
	r.mu.Unlock()
	if !started {
		return "", ErrInputNotStarted
	}
	line, err := r.instance.Readline()
	if err != nil {
		if errors.Is(err, readline.ErrInterrupt) {
			return "", io.EOF
		}
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// Close 释放终端状态
func (r *ReadlineInputReader) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	r.started = false
	_ = r.instance.Close()
}

// consoleCompleter 把补全候选转成 readline 需要的后缀列表
//
// 对齐 Python WordCompleter(sentence=True, ignore_case=True)：按整行前缀匹配，
// 返回需要在光标处插入的后缀与替换长度。
type consoleCompleter struct {
	completions func() []string
}

func (c consoleCompleter) Do(line []rune, pos int) ([][]rune, int) {
	if pos > len(line) {
		pos = len(line)
	}
	prefix := string(line[:pos])
	lowerPrefix := strings.ToLower(prefix)
	candidates := make([][]rune, 0, 8)
	for _, candidate := range c.completions() {
		if candidate == prefix {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(candidate), lowerPrefix) {
			continue
		}
		candidates = append(candidates, []rune(candidate[len(prefix):]))
	}
	return candidates, len([]rune(prefix))
}

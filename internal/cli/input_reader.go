package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// ErrInputNotStarted 输入读取器尚未启动
var ErrInputNotStarted = errors.New("输入读取器尚未启动")

type inputLine struct {
	text string
	eof  bool
	err  error
}

// InputReader 后台读取标准输入，对齐 MaiBot src/cli/input_reader.py
type InputReader struct {
	reader io.Reader
	queue  chan inputLine
	stop   chan struct{}

	mu      sync.Mutex
	started bool
	closed  bool
}

// NewInputReader 使用指定 reader 创建输入读取器
func NewInputReader(reader io.Reader) *InputReader {
	if reader == nil {
		reader = os.Stdin
	}
	return &InputReader{
		reader: reader,
		queue:  make(chan inputLine, 64),
	}
}

// NewStdinInputReader 创建标准输入读取器
func NewStdinInputReader() *InputReader {
	return NewInputReader(os.Stdin)
}

// Start 启动后台读取；重复调用时忽略
func (r *InputReader) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return
	}
	r.started = true
	r.closed = false
	r.stop = make(chan struct{})
	go r.readLoop()
}

// readLoop 阻塞读取一行并投递到队列
func (r *InputReader) readLoop() {
	reader := bufio.NewReader(r.reader)
	for {
		select {
		case <-r.stop:
			return
		default:
		}
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			select {
			case r.queue <- inputLine{text: strings.TrimRight(line, "\r\n")}:
			case <-r.stop:
				return
			}
		}
		if err != nil {
			select {
			case r.queue <- inputLine{eof: true, err: err}:
			case <-r.stop:
			}
			return
		}
	}
}

// Next 阻塞读取一行；stdin 关闭返回 io.EOF
func (r *InputReader) Next(ctx context.Context) (string, error) {
	r.mu.Lock()
	started := r.started
	r.mu.Unlock()
	if !started {
		return "", ErrInputNotStarted
	}
	select {
	case item := <-r.queue:
		if item.eof {
			if item.err != nil && !errors.Is(item.err, io.EOF) {
				return "", item.err
			}
			return "", io.EOF
		}
		return item.text, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// NextWithTimeout 在超时内读取一行；超时返回 ok=false 且 err=nil，对齐 Python get_line
func (r *InputReader) NextWithTimeout(ctx context.Context, timeout time.Duration) (line string, ok bool, err error) {
	if timeout <= 0 {
		line, err = r.Next(ctx)
		return line, err == nil, err
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	line, err = r.Next(timeoutCtx)
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return line, true, nil
}

// Close 请求后台读取停止；已阻塞的底层 Read 无法在本层取消
func (r *InputReader) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || !r.started {
		return
	}
	r.closed = true
	close(r.stop)
	r.started = false
}

// IsInteractiveTerminal 判断文件是否为交互式终端
func IsInteractiveTerminal(file *os.File) bool {
	if file == nil {
		return false
	}
	return isTerminalFile(file)
}

// EnsureInteractiveStdin 校验标准输入可用于交互
func EnsureInteractiveStdin() error {
	if IsInteractiveTerminal(os.Stdin) {
		return nil
	}
	return fmt.Errorf("标准输入不是交互式终端，无法启动终端交互")
}

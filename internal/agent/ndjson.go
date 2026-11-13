package agent

import (
	"bufio"
	"bytes"
	"fmt"
	"io"

	"github.com/bytedance/sonic"
)

// WriteFrame 写入一行 NDJSON（JSON-RPC 2.0 帧），对齐 ACPConnection.send
func WriteFrame(w io.Writer, message map[string]any) error {
	line, err := sonic.ConfigStd.Marshal(message)
	if err != nil {
		return fmt.Errorf("acpagent: 序列化 ACP 帧失败: %w", err)
	}
	if _, err := w.Write(line); err != nil {
		return err
	}
	if _, err := w.Write([]byte{'\n'}); err != nil {
		return err
	}
	return nil
}

// ReadFrame 读取一行 NDJSON；空行跳过
func ReadFrame(r *bufio.Reader) (map[string]any, error) {
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var message map[string]any
		if err := sonic.ConfigStd.Unmarshal(line, &message); err != nil {
			return nil, fmt.Errorf("acpagent: stdout 包含非协议输出，请使用 ACP/stdio 模式并将日志写入 stderr: %w", err)
		}
		if message["jsonrpc"] != "2.0" {
			return nil, fmt.Errorf("acpagent: stdout 必须仅包含 JSON-RPC 2.0 消息")
		}
		return message, nil
	}
}

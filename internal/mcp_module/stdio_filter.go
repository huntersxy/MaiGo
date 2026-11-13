package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

const maxGarbagePreview = 200

// tolerantReader 过滤 MCP stdio 服务器写到 stdout 的协议违规噪声
//
// 对齐 MaiBot stdio_filter.py：仅接受 JSON 对象或数组开头的行，解析失败只记录告警并丢弃，
// 不向上层注入异常，从而兼容会把启动横幅打到 stdout 的第三方服务器
type tolerantReader struct {
	reader  *bufio.Reader
	buffer  bytes.Buffer
	dropped int
}

func newTolerantReader(raw io.Reader) *tolerantReader {
	return &tolerantReader{reader: bufio.NewReader(raw)}
}

// Dropped 返回已丢弃的噪声行数，供测试与诊断使用
func (t *tolerantReader) Dropped() int { return t.dropped }

func (t *tolerantReader) Read(p []byte) (int, error) {
	for t.buffer.Len() == 0 {
		line, err := t.reader.ReadString('\n')
		if err != nil {
			// 末尾残行没有换行符，按 Python 实现丢弃
			return 0, err
		}
		if accepted := t.accept(line); accepted != "" {
			t.buffer.WriteString(accepted)
		}
	}
	return t.buffer.Read(p)
}

func (t *tolerantReader) accept(line string) string {
	stripped := strings.TrimSpace(line)
	if stripped == "" {
		return ""
	}
	// JSON-RPC 2.0 消息总是 JSON 对象（'{'）或批量数组（'['）
	if stripped[0] != '{' && stripped[0] != '[' {
		t.dropped++
		zap.L().Warn("Dropped non-JSON line from MCP stdio server (violates MCP spec — stdout must carry JSON-RPC only): " + strconv.Quote(truncatePreview(stripped)))
		return ""
	}
	if !validJSONRPCPayload(stripped) {
		t.dropped++
		zap.L().Warn("Dropped malformed JSON-RPC line from MCP stdio server: " + strconv.Quote(truncatePreview(stripped)))
		return ""
	}
	return line
}

func truncatePreview(line string) string {
	if len(line) <= maxGarbagePreview {
		return line
	}
	return line[:maxGarbagePreview]
}

type jsonRPCEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

func validJSONRPCPayload(line string) bool {
	var envelope jsonRPCEnvelope
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		return false
	}
	if envelope.JSONRPC != "2.0" {
		return false
	}
	return envelope.Method != "" || len(envelope.Result) > 0 || len(envelope.Error) > 0
}

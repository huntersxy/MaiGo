package app

// 对齐 src/webui/middleware/compression.py 的 TextGZipMiddleware：
// 只压缩一次性返回的文本类响应；流式响应（handler 主动 Flush）原样透传，
// 避免 gzip 缓冲改变流式接口逐块到达的行为。

import (
	"bufio"
	"compress/gzip"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
)

const (
	// gzipMinCompressSize 小于该体积的响应压缩收益抵不上 gzip 头部开销
	gzipMinCompressSize = 1024
	// gzipBufferCap 缓冲上限；超出即放弃压缩整体透传，避免无界内存占用
	// （Python 侧单块消息本就整体驻留内存，这里再加一道硬上限）
	gzipBufferCap = 8 << 20
)

// gzipCompressibleContentTypes 可压缩的文本类 Content-Type（取主类型，忽略参数）
var gzipCompressibleContentTypes = map[string]struct{}{
	"application/javascript": {},
	"application/json":       {},
	"application/xml":        {},
	"image/svg+xml":          {},
	"text/css":               {},
	"text/html":              {},
	"text/javascript":        {},
	"text/plain":             {},
	"text/xml":               {},
}

// acceptsGzip 判断请求头 Accept-Encoding 是否允许 gzip
func acceptsGzip(acceptEncoding string) bool {
	return strings.Contains(strings.ToLower(acceptEncoding), "gzip")
}

// textGZipWriter 攒下 handler 的全部输出，处理结束时一次性决定压缩与否；
// handler 调用 Flush 即视为流式，切回透传模式。
type textGZipWriter struct {
	http.ResponseWriter
	status      int
	buf         []byte
	streaming   bool
	wroteHeader bool
}

func (w *textGZipWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	if status == http.StatusSwitchingProtocols {
		// 协议升级（WebSocket）必须立刻下发 101，扣下会让握手永远收不到响应
		w.ResponseWriter.WriteHeader(status)
		w.wroteHeader = true
		return
	}
	w.status = status
	// 头部先扣下，等处理结束确认可压缩后再改写（对齐 Python 扣下 http.response.start）
}

func (w *textGZipWriter) Write(p []byte) (int, error) {
	if w.streaming || w.wroteHeader {
		return w.ResponseWriter.Write(p)
	}
	if len(w.buf)+len(p) > gzipBufferCap {
		// 超出缓冲上限：放弃压缩，已攒内容整体透传
		w.streaming = true
		w.flushBufferPassthrough()
		return w.ResponseWriter.Write(p)
	}
	w.buf = append(w.buf, p...)
	return len(p), nil
}

// Flush 语义：handler 主动 Flush = 流式接口。把已攒内容按未压缩原样发出，后续写直通。
func (w *textGZipWriter) Flush() {
	if !w.streaming && !w.wroteHeader {
		w.streaming = true
		w.flushBufferPassthrough()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack 透传：websocket 升级需要底层连接
func (w *textGZipWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("textGZipWriter: 底层 ResponseWriter 不支持 Hijack")
	}
	return hj.Hijack()
}

func (w *textGZipWriter) flushBufferPassthrough() {
	// 不写 Content-Length：流式/超限场景总量未知，交给 net/http 走 chunked
	w.ResponseWriter.WriteHeader(w.status)
	w.wroteHeader = true
	if len(w.buf) > 0 {
		_, _ = w.ResponseWriter.Write(w.buf)
	}
	w.buf = nil
}

// finalize 在 handler 返回后调用：决定压缩或原样输出
func (w *textGZipWriter) finalize() {
	if w.streaming || w.wroteHeader {
		return
	}
	h := w.Header()
	contentType := strings.SplitN(h.Get("Content-Type"), ";", 2)[0]
	contentType = strings.TrimSpace(strings.ToLower(contentType))
	_, compressible := gzipCompressibleContentTypes[contentType]
	if !compressible || h.Get("Content-Encoding") != "" || len(w.buf) < gzipMinCompressSize {
		w.flushBufferPassthrough()
		return
	}
	var compressed strings.Builder
	zw, _ := gzip.NewWriterLevel(&compressed, gzip.DefaultCompression)
	_, _ = zw.Write(w.buf)
	_ = zw.Close()
	body := []byte(compressed.String())
	h.Set("Content-Encoding", "gzip")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	vary := h.Get("Vary")
	switch {
	case vary == "":
		h.Set("Vary", "Accept-Encoding")
	case !strings.Contains(vary, "Accept-Encoding"):
		h.Set("Vary", vary+", Accept-Encoding")
	}
	w.ResponseWriter.WriteHeader(w.status)
	w.wroteHeader = true
	_, _ = w.ResponseWriter.Write(body)
	w.buf = nil
}

// textGZipMiddleware 包装整个路由树；Accept-Encoding 不含 gzip 时零开销直通
func textGZipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		ww := &textGZipWriter{ResponseWriter: w, status: http.StatusOK}
		defer ww.finalize()
		next.ServeHTTP(ww, r)
	})
}

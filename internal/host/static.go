package host

// WebUI 前端静态资源：所有 Bot 共用宿主提供的同一份页面。
// 对齐 src/host/static.py：文本资源 gzip、构建产物长缓存、入口页每次校验。

import (
	"compress/gzip"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var gzipStaticSuffixes = map[string]struct{}{
	".css": {}, ".html": {}, ".js": {}, ".json": {}, ".map": {}, ".mjs": {}, ".svg": {}, ".ttf": {}, ".txt": {},
}

var staticMediaTypes = map[string]string{
	".css": "text/css", ".html": "text/html", ".js": "text/javascript",
	".mjs": "text/javascript", ".json": "application/json",
}

type cachedGzip struct {
	size    int64
	modTime time.Time
	content []byte
}

var staticGzipCache = struct {
	sync.Mutex
	entries map[string]cachedGzip
}{entries: map[string]cachedGzip{}}

// StaticHandler 提供 SPA 资源；未知的无后缀路径交给前端路由
type StaticHandler struct {
	root string
}

// NewStaticHandler 绑定 dashboard 构建目录
func NewStaticHandler(root string) *StaticHandler {
	return &StaticHandler{root: filepath.Clean(root)}
}

func (h *StaticHandler) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if h == nil || h.root == "" {
		http.Error(rw, "当前 WebUI 资源不可用，请安装新版资源或构建 dashboard", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(rw, r)
		return
	}
	requested := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if requested == "api" || strings.HasPrefix(requested, "api/") {
		http.NotFound(rw, r)
		return
	}
	file, relative, openErr := h.open(requested)
	if openErr != nil {
		if path.Ext(requested) != "" {
			http.NotFound(rw, r)
			return
		}
		file, relative, openErr = h.open("index.html")
		if openErr != nil {
			http.NotFound(rw, r)
			return
		}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(rw, r)
		return
	}
	suffix := strings.ToLower(path.Ext(relative))
	mediaType := staticMediaTypes[suffix]
	if mediaType == "" {
		mediaType = mime.TypeByExtension(suffix)
	}
	if mediaType == "" {
		mediaType = "text/plain"
	}
	if _, compressible := gzipStaticSuffixes[suffix]; compressible && acceptsGzip(r.Header.Get("Accept-Encoding")) {
		content, err := gzipContent(filepath.Join(h.root, filepath.FromSlash(relative)), info)
		if err == nil {
			rw.Header().Set("Content-Type", mediaType)
			rw.Header().Set("Content-Encoding", "gzip")
			rw.Header().Set("Vary", "Accept-Encoding")
			h.cacheHeaders(rw, relative, suffix)
			rw.Header().Set("Content-Length", strconv.Itoa(len(content)))
			if r.Method != http.MethodHead {
				_, _ = rw.Write(content)
			}
			return
		}
	}
	rw.Header().Set("Content-Type", mediaType)
	h.cacheHeaders(rw, relative, suffix)
	http.ServeContent(rw, r, info.Name(), info.ModTime(), file)
}

func (h *StaticHandler) open(relative string) (*os.File, string, error) {
	root, err := os.OpenRoot(h.root)
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	// os.Root 拒绝越界路径；Clean 后再打开保证分隔符一致
	cleaned := filepath.ToSlash(filepath.Clean(relative))
	if cleaned == "." || cleaned == "" {
		cleaned = "index.html"
	}
	file, err := root.Open(cleaned)
	if err != nil {
		return nil, "", err
	}
	return file, cleaned, nil
}

func (h *StaticHandler) cacheHeaders(rw http.ResponseWriter, relative, suffix string) {
	if strings.HasPrefix(relative, "assets/") {
		rw.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else if suffix == ".html" {
		rw.Header().Set("Cache-Control", "no-cache")
		rw.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	}
}

func gzipContent(fullPath string, info os.FileInfo) ([]byte, error) {
	staticGzipCache.Lock()
	cached, exists := staticGzipCache.entries[fullPath]
	staticGzipCache.Unlock()
	if exists && cached.size == info.Size() && cached.modTime.Equal(info.ModTime()) {
		return cached.content, nil
	}
	file, err := os.Open(fullPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var builder strings.Builder
	writer, err := gzip.NewWriterLevel(&stringWriter{builder: &builder}, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	writer.ModTime = time.Unix(0, 0)
	if _, err := io.Copy(writer, file); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	content := []byte(builder.String())
	staticGzipCache.Lock()
	if len(staticGzipCache.entries) > 256 {
		staticGzipCache.entries = map[string]cachedGzip{}
	}
	staticGzipCache.entries[fullPath] = cachedGzip{size: info.Size(), modTime: info.ModTime(), content: content}
	staticGzipCache.Unlock()
	return content, nil
}

type stringWriter struct {
	builder *strings.Builder
}

func (w *stringWriter) Write(data []byte) (int, error) {
	return w.builder.Write(data)
}

func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(fields[0]), "gzip") {
			for _, parameter := range fields[1:] {
				parameter = strings.TrimSpace(parameter)
				if strings.HasPrefix(parameter, "q=") {
					value, err := strconv.ParseFloat(strings.TrimPrefix(parameter, "q="), 64)
					return err != nil || value > 0
				}
			}
			return true
		}
	}
	return false
}

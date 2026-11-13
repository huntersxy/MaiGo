package dashboardstatic

import (
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
)

type spaHandler struct {
	rootDir string
}

// RootIndexHandler 在根路径 / 返回原版 Dashboard index.html
func RootIndexHandler(rootDir string) http.HandlerFunc {
	rootDir = strings.TrimSpace(rootDir)
	if !validDashboardRoot(rootDir) {
		return func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
	}
	h := &spaHandler{rootDir: rootDir}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
			return
		}
		root, err := os.OpenRoot(h.rootDir)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer root.Close()
		h.serveRaw(w, r, root, "index.html")
	}
}

// SPAFallbackHandler 对齐 Python serve_spa：未命中的 GET 路径先找静态文件，
// 找不到就回落 index.html；/api 前缀仍返回 404
func SPAFallbackHandler(rootDir string) http.HandlerFunc {
	rootDir = strings.TrimSpace(rootDir)
	if !validDashboardRoot(rootDir) {
		return func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
	}
	h := &spaHandler{rootDir: rootDir}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		rel := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
		rel = strings.TrimPrefix(rel, "/")
		if rel == "api" || strings.HasPrefix(rel, "api/") {
			http.NotFound(w, r)
			return
		}
		root, err := os.OpenRoot(h.rootDir)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer root.Close()
		if rel == "" {
			h.serveRaw(w, r, root, "index.html")
			return
		}
		if file, err := root.Open(rel); err == nil {
			defer file.Close()
			if info, statErr := file.Stat(); statErr == nil && !info.IsDir() {
				h.writeRawFile(w, r, rel, file, info)
				return
			}
		}
		h.serveRaw(w, r, root, "index.html")
	}
}

// serveRaw 原样输出 HTML
func (h *spaHandler) serveRaw(w http.ResponseWriter, r *http.Request, root *os.Root, rel string) {
	file, err := root.Open(rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		return
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

// writeRawFile 原样输出静态文件，资产按长缓存处理
func (h *spaHandler) writeRawFile(w http.ResponseWriter, r *http.Request, rel string, file io.ReadSeeker, info os.FileInfo) {
	if parts := strings.Split(rel, "/"); len(parts) > 0 && parts[0] == "assets" {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else if strings.HasSuffix(strings.ToLower(rel), ".html") || rel == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

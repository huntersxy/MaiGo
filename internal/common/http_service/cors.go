// Package httpservice 是后端 HTTP 监听与跨域策略层。
//
// 对齐 MaiBot src/common/http_service/：
//   - config.py -> HTTPConfig（配置结构见 config/maibot/types_generated.go）
//   - app.py    -> CORS 中间件与并发上限（本包）
//   - server.py -> 多地址监听、端口冲突处理、地址日志（本包）
//
// Python 的 HTTPServer 同时服务文件传输与 WebUI，两者共用同一端口与跨域策略。
// 本包只负责监听与跨域，文件服务与 WebUI 都在其之上复用，避免 WebUI 包承担
// HTTP 服务生命周期。
package httpservice

import (
	"fmt"
	"net/http"
	"strings"
)

// CORS 配置对齐 Python http_service/app.py 的 _setup_cors。
//
// 前端开发服务器固定跑在 5173（Vite）与 7999（旧版），必须放行跨域，
// 否则开发时浏览器预检会被拒。生产同源场景下不产生额外响应头。
const (
	// DevOriginVite Vite 前端开发服务器
	DevOriginVite = "5173"
	// DevOriginLegacy 旧版前端开发服务器
	DevOriginLegacy = "7999"
)

// CORSAllowMethods 允许的请求方法，对齐 Python allow_methods
var CORSAllowMethods = []string{
	http.MethodGet,
	http.MethodHead,
	http.MethodPost,
	http.MethodPut,
	http.MethodDelete,
	http.MethodPatch,
	http.MethodOptions,
}

// CORSAllowHeaders 允许的请求头，对齐 Python allow_headers
var CORSAllowHeaders = []string{
	"Content-Type",
	"Accept",
	"Origin",
	"X-Requested-With",
	"Range",
}

// CORSExposeHeaders 暴露给浏览器的响应头，对齐 Python expose_headers
//
// 文件传输依赖 Content-Range 与 Content-Disposition 实现断点续传与下载文件名，
// 不放行时前端读不到这两个头。
var CORSExposeHeaders = []string{
	"Content-Length",
	"Content-Type",
	"Content-Range",
	"Content-Disposition",
	"ETag",
}

// AllowedOrigins 返回允许跨域的来源列表
//
// 对齐 Python：开发端口固定放行，另加当前服务端口自身的 localhost/127.0.0.1，
// 端口为 0 或负数时不追加（避免生成 "http://localhost:0" 这种无效来源）。
func AllowedOrigins(port int) []string {
	origins := make([]string, 0, 6)
	for _, devPort := range []string{DevOriginVite, DevOriginLegacy} {
		origins = append(origins,
			"http://localhost:"+devPort,
			"http://127.0.0.1:"+devPort,
		)
	}
	if port > 0 {
		origins = append(origins,
			fmt.Sprintf("http://localhost:%d", port),
			fmt.Sprintf("http://127.0.0.1:%d", port),
		)
	}
	return origins
}

// CORSMiddleware 返回跨域中间件
//
// 行为对齐 Python CORSMiddleware(allow_credentials=True)：
//   - 命中白名单时回显该 Origin（不能用 "*"，否则与 allow_credentials 冲突）
//   - 预检请求直接 204 返回，不进入业务处理
//   - 非白名单来源不写 CORS 头，由浏览器自行拦截
func CORSMiddleware(port int) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, 6)
	for _, origin := range AllowedOrigins(port) {
		allowed[origin] = true
	}
	allowMethods := strings.Join(CORSAllowMethods, ", ")
	allowHeaders := strings.Join(CORSAllowHeaders, ", ")
	exposeHeaders := strings.Join(CORSExposeHeaders, ", ")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.TrimSpace(r.Header.Get("Origin"))
			if origin == "" || !allowed[origin] {
				next.ServeHTTP(w, r)
				return
			}

			header := w.Header()
			header.Set("Access-Control-Allow-Origin", origin)
			header.Set("Access-Control-Allow-Credentials", "true")
			header.Set("Access-Control-Expose-Headers", exposeHeaders)
			// 同一响应可能因不同 Origin 被缓存，必须声明 Vary 以免串号
			header.Add("Vary", "Origin")

			if r.Method == http.MethodOptions {
				header.Set("Access-Control-Allow-Methods", allowMethods)
				header.Set("Access-Control-Allow-Headers", allowHeaders)
				header.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

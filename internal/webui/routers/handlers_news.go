package webuiapi

import (
	"net/http"
)

// handleNewsGet 对齐 Python news.py:12-23：仅 502 分支，缺少服务初始化也按资讯源不可用处理
func (d Deps) handleNewsGet(w http.ResponseWriter, r *http.Request) {
	if !d.authenticated(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "Token 无效或已过期"})
		return
	}
	force, present, boolErr := optionalQueryBool(r, "force")
	if boolErr != nil {
		writeRequestValidationError(w, *boolErr)
		return
	}
	if !present {
		force = false
	}
	fetch := d.newsFetch
	if fetch == nil {
		if d.News == nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"detail": "首页资讯服务未初始化"})
			return
		}
		fetch = d.News.Fetch
	}
	resp, err := fetch(r.Context(), force)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (d Deps) handleNewsStatus(w http.ResponseWriter, r *http.Request) {
	if d.News == nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": "首页资讯服务未初始化"})
		return
	}
	writeJSON(w, http.StatusOK, d.News.Status())
}

func (d Deps) handleHTMLRenderStatus(w http.ResponseWriter, r *http.Request) {
	if d.HTMLRender == nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": "HTML 渲染服务未初始化"})
		return
	}
	writeJSON(w, http.StatusOK, d.HTMLRender.Status())
}

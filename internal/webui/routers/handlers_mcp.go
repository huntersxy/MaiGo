package webuiapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/bytedance/sonic"

	"MaiGo/internal/mcp_module"
)

func (d Deps) handleMCPStatus(w http.ResponseWriter, r *http.Request) {
	if !d.authenticated(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "Token 无效或已过期"})
		return
	}
	if d.Agent == nil {
		writeJSON(w, http.StatusOK, mcp.StatusSnapshot{})
		return
	}
	writeJSON(w, http.StatusOK, d.Agent.MCPStatus())
}

func (d Deps) handleMCPTest(w http.ResponseWriter, r *http.Request) {
	if !d.authenticated(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "Token 无效或已过期"})
		return
	}

	var raw map[string]any
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"detail": "MCP 服务配置无效: " + err.Error(),
		})
		return
	}

	cfg, err := d.resolveMCPTestConfig(r, raw)
	if err != nil {
		if errors.Is(err, mcp.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": err.Error()})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}

	result := mcp.TestConnection(r.Context(), cfg)
	writeJSON(w, http.StatusOK, result)
}

func (d Deps) resolveMCPTestConfig(r *http.Request, raw map[string]any) (mcp.ServerConfig, error) {
	if id, ok := mcp.WebUIRefID(raw); ok {
		if d.MCPStore == nil {
			return mcp.ServerConfig{}, errors.New("MCP 配置存储未初始化")
		}
		cfg, _, err := d.MCPStore.Get(r.Context(), id)
		if err != nil {
			return mcp.ServerConfig{}, err
		}
		cfg.Enabled = true
		return cfg, nil
	}
	if mcp.WebUIRefNameOnly(raw) {
		if d.MCPStore == nil {
			return mcp.ServerConfig{}, errors.New("MCP 配置存储未初始化")
		}
		name := strings.TrimSpace(webUIStringField(raw, "name"))
		cfg, _, err := d.MCPStore.GetByName(r.Context(), name)
		if err != nil {
			return mcp.ServerConfig{}, err
		}
		cfg.Enabled = true
		return cfg, nil
	}
	cfg, _, err := mcp.ServerConfigFromWebUI(raw)
	return cfg, err
}

func webUIStringField(raw map[string]any, key string) string {
	value, ok := raw[key]
	if !ok || value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

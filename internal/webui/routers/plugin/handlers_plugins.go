package plugin

import (
	"MaiGo/internal/webui/httputil"
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"

	"MaiGo/internal/plugin_market"
)

// marketplaceInstallRequest 对齐 routers/plugin/schemas.InstallPluginRequest
type marketplaceInstallRequest struct {
	PluginID      string  `json:"plugin_id"`
	RepositoryURL string  `json:"repository_url"`
	Branch        *string `json:"branch"`
	MirrorID      *string `json:"mirror_id"`
	Version       *string `json:"version"`
}

// marketplaceUninstallRequest 对齐 routers/plugin/schemas.UninstallPluginRequest
type marketplaceUninstallRequest struct {
	PluginID string `json:"plugin_id"`
}

// marketplaceUpdateRequest 对齐 routers/plugin/schemas.UpdatePluginRequest
type marketplaceUpdateRequest struct {
	Force          bool    `json:"force"`
	ExpectedCommit *string `json:"expected_commit"`
	PluginID       string  `json:"plugin_id"`
	RepositoryURL  string  `json:"repository_url"`
	Branch         *string `json:"branch"`
	MirrorID       *string `json:"mirror_id"`
	Version        *string `json:"version"`
}

// marketplaceSourceRequest 对齐 PluginSourceSettings 请求体
type marketplaceSourceRequest struct {
	InformationSource    string  `json:"information_source"`
	DownloadSource       string  `json:"download_source"`
	CustomDownloadPrefix *string `json:"custom_download_prefix"`
}

// expectedCommitPattern 对齐 UpdatePluginRequest.expected_commit 的 pydantic 约束
var expectedCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// writeMarketFailure 对齐 routers/plugin/marketplace.MarketRoute 的错误映射
func writeMarketFailure(w http.ResponseWriter, err error, fallbackPrefix string) {
	if marketErr, ok := pluginmarket.AsMarketError(err); ok {
		if marketErr.Payload != nil {
			httputil.WriteJSON(w, marketErr.StatusCode, map[string]any{"detail": marketErr.Payload})
			return
		}
		httputil.WriteJSON(w, marketErr.StatusCode, map[string]any{"detail": marketErr.Message})
		return
	}
	if statusErr, ok := pluginmarket.AsHTTPStatusError(err); ok {
		status := http.StatusBadGateway
		if statusErr.Code == http.StatusNotFound {
			status = http.StatusNotFound
		}
		httputil.WriteJSON(w, status, map[string]any{"detail": statusErr.Error()})
		return
	}
	httputil.WriteJSON(w, http.StatusBadGateway, map[string]any{"detail": fallbackPrefix + err.Error()})
}

func decodePluginRequest(r *http.Request, target any) error {
	return sonic.ConfigDefault.NewDecoder(r.Body).Decode(target)
}

// marketService 把 Deps 的实例目录同步给市场服务，避免安装目录与 HTTP 层不一致
func (d Deps) marketService() *pluginmarket.Service {
	if d.PluginMarket != nil && strings.TrimSpace(d.InstanceDir) != "" {
		d.PluginMarket.SetInstanceDir(d.InstanceDir)
	}
	return d.PluginMarket
}

// handlePluginMarketplaceList 对齐 GET /api/webui/plugins/marketplace
func (d Deps) handlePluginMarketplaceList(w http.ResponseWriter, r *http.Request) {
	if d.PluginMarket == nil {
		httputil.WriteAPIError(w, http.StatusServiceUnavailable, "插件市场服务未初始化")
		return
	}
	compatibleOnly := queryBool(r, "compatible_only", false)
	forceRefresh := queryBool(r, "force_refresh", false)
	response, err := d.PluginMarket.List(r.Context(), compatibleOnly, forceRefresh)
	if err != nil {
		writeMarketFailure(w, err, "插件市场请求失败：")
		return
	}
	httputil.WriteJSON(w, http.StatusOK, response.Payload())
}

// handlePluginMarketplaceInstall 对齐 POST /plugins/install（本仓库沿用 /marketplace/install 路径）
func (d Deps) handlePluginMarketplaceInstall(w http.ResponseWriter, r *http.Request) {
	service := d.marketService()
	if service == nil {
		httputil.WriteAPIError(w, http.StatusServiceUnavailable, "插件市场服务未初始化")
		return
	}
	var body marketplaceInstallRequest
	if err := decodePluginRequest(r, &body); err != nil {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "请求参数无效"})
		return
	}
	pluginID := strings.TrimSpace(body.PluginID)
	if pluginID == "" {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "plugin_id 为必填"})
		return
	}
	if err := pluginmarket.ValidatePluginID(pluginID); err != nil {
		writeMarketFailure(w, err, "插件市场请求失败：")
		return
	}
	release, err := service.Reserve(pluginID, "install")
	if err != nil {
		writeMarketFailure(w, err, "插件市场请求失败：")
		return
	}
	defer release()

	if body.Version != nil {
		entry, target, err := service.ResolveRelease(r.Context(), pluginID, *body.Version)
		if err != nil {
			writeMarketFailure(w, err, "获取插件发布版本失败：")
			return
		}
		if target != nil {
			mirrorID := ""
			if body.MirrorID != nil {
				mirrorID = *body.MirrorID
			}
			result, err := service.InstallRelease(
				r.Context(), pluginID, entry, target, false, *body.Version == "latest", mirrorID, false, nil)
			if err != nil {
				writeInstallFailure(w, err)
				return
			}
			httputil.WriteJSON(w, http.StatusOK, result.Payload())
			return
		}
		body.RepositoryURL = entry.RepositoryURL
	}
	branch := "main"
	if body.Branch != nil {
		branch = *body.Branch
	}
	mirrorID := ""
	if body.MirrorID != nil {
		mirrorID = *body.MirrorID
	}
	result, err := service.InstallFromBranch(r.Context(), pluginID, body.RepositoryURL, branch, mirrorID)
	if err != nil {
		writeInstallFailure(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, result)
}

// handlePluginMarketplaceUninstall 对齐 POST /plugins/uninstall
func (d Deps) handlePluginMarketplaceUninstall(w http.ResponseWriter, r *http.Request) {
	service := d.marketService()
	if service == nil {
		httputil.WriteAPIError(w, http.StatusServiceUnavailable, "插件市场服务未初始化")
		return
	}
	var body marketplaceUninstallRequest
	if err := decodePluginRequest(r, &body); err != nil {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "请求参数无效"})
		return
	}
	pluginID := strings.TrimSpace(body.PluginID)
	if pluginID == "" {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "plugin_id 为必填"})
		return
	}
	if err := pluginmarket.ValidatePluginID(pluginID); err != nil {
		writeMarketFailure(w, err, "插件市场请求失败：")
		return
	}
	release, err := service.Reserve(pluginID, "uninstall")
	if err != nil {
		writeMarketFailure(w, err, "插件市场请求失败：")
		return
	}
	defer release()
	result, err := service.Uninstall(r.Context(), pluginID)
	if err != nil {
		writeInstallFailure(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, result)
}

// handlePluginMarketplaceUpdate 对齐 POST /plugins/update
func (d Deps) handlePluginMarketplaceUpdate(w http.ResponseWriter, r *http.Request) {
	service := d.marketService()
	if service == nil {
		httputil.WriteAPIError(w, http.StatusServiceUnavailable, "插件市场服务未初始化")
		return
	}
	var body marketplaceUpdateRequest
	if err := decodePluginRequest(r, &body); err != nil {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "请求参数无效"})
		return
	}
	if body.ExpectedCommit != nil && !expectedCommitPattern.MatchString(*body.ExpectedCommit) {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "expected_commit 必须为 40 位小写十六进制"})
		return
	}
	pluginID := strings.TrimSpace(body.PluginID)
	if pluginID == "" {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "plugin_id 为必填"})
		return
	}
	if err := pluginmarket.ValidatePluginID(pluginID); err != nil {
		writeMarketFailure(w, err, "插件市场请求失败：")
		return
	}
	lockID := pluginID
	if body.Version != nil {
		entry, _, err := service.ResolveRelease(r.Context(), pluginID, *body.Version)
		if err != nil {
			writeMarketFailure(w, err, "获取插件发布版本失败：")
			return
		}
		if entry.ManifestID != nil && strings.TrimSpace(*entry.ManifestID) != "" {
			lockID = strings.TrimSpace(*entry.ManifestID)
		} else if strings.TrimSpace(entry.ID) != "" {
			lockID = strings.TrimSpace(entry.ID)
		}
		if err := pluginmarket.ValidatePluginID(lockID); err != nil {
			writeMarketFailure(w, err, "插件市场请求失败：")
			return
		}
	}
	release, err := service.Reserve(lockID, "update")
	if err != nil {
		writeMarketFailure(w, err, "插件市场请求失败：")
		return
	}
	defer release()
	branch := "main"
	if body.Branch != nil {
		branch = *body.Branch
	}
	mirrorID := ""
	if body.MirrorID != nil {
		mirrorID = *body.MirrorID
	}
	result, err := service.Update(r.Context(), pluginmarket.UpdateRequest{
		Force:          body.Force,
		ExpectedCommit: body.ExpectedCommit,
		PluginID:       pluginID,
		RepositoryURL:  body.RepositoryURL,
		Branch:         &branch,
		MirrorID:       mirrorID,
		Version:        body.Version,
	})
	if err != nil {
		if errors.Is(r.Context().Err(), context.Canceled) {
			httputil.WriteJSON(w, 499, map[string]any{"detail": "更新已取消"})
			return
		}
		writeInstallFailure(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, result)
}

// writeInstallFailure 对齐 management.install_plugin 的异常映射
func writeInstallFailure(w http.ResponseWriter, err error) {
	if marketErr, ok := pluginmarket.AsMarketError(err); ok {
		if marketErr.Payload != nil {
			httputil.WriteJSON(w, marketErr.StatusCode, map[string]any{"detail": marketErr.Payload})
			return
		}
		httputil.WriteJSON(w, marketErr.StatusCode, map[string]any{"detail": marketErr.Message})
		return
	}
	if statusErr, ok := pluginmarket.AsHTTPStatusError(err); ok {
		status := http.StatusBadGateway
		if statusErr.Code == http.StatusNotFound {
			status = http.StatusNotFound
		}
		httputil.WriteJSON(w, status, map[string]any{"detail": statusErr.Error()})
		return
	}
	httputil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"detail": "服务器错误: " + err.Error()})
}

func pluginIDFromPath(r *http.Request) string {
	return strings.TrimSpace(chi.URLParam(r, "plugin_id"))
}

// handlePluginMarketplaceDetail 对齐 GET /marketplace/{plugin_id}
func (d Deps) handlePluginMarketplaceDetail(w http.ResponseWriter, r *http.Request) {
	if d.PluginMarket == nil {
		httputil.WriteAPIError(w, http.StatusServiceUnavailable, "插件市场服务未初始化")
		return
	}
	response, err := d.PluginMarket.Detail(r.Context(), pluginIDFromPath(r), queryBool(r, "force_refresh", false))
	if err != nil {
		writeMarketFailure(w, err, "插件市场请求失败：")
		return
	}
	httputil.WriteJSON(w, http.StatusOK, response.Payload())
}

// handlePluginMarketplaceStats 对齐 GET /marketplace/{plugin_id}/stats
func (d Deps) handlePluginMarketplaceStats(w http.ResponseWriter, r *http.Request) {
	if d.PluginMarket == nil {
		httputil.WriteAPIError(w, http.StatusServiceUnavailable, "插件市场服务未初始化")
		return
	}
	response, err := d.PluginMarket.Stats(r.Context(), pluginIDFromPath(r), queryBool(r, "force_refresh", false))
	if err != nil {
		writeMarketFailure(w, err, "插件市场请求失败：")
		return
	}
	httputil.WriteJSON(w, http.StatusOK, response.Payload())
}

// handlePluginMarketplaceReadme 对齐 GET /marketplace/{plugin_id}/readme
func (d Deps) handlePluginMarketplaceReadme(w http.ResponseWriter, r *http.Request) {
	d.handlePluginDocument(w, r, true)
}

// handlePluginMarketplaceChangelog 对齐 GET /marketplace/{plugin_id}/changelog
func (d Deps) handlePluginMarketplaceChangelog(w http.ResponseWriter, r *http.Request) {
	d.handlePluginDocument(w, r, false)
}

func (d Deps) handlePluginDocument(w http.ResponseWriter, r *http.Request, readme bool) {
	service := d.marketService()
	if service == nil {
		httputil.WriteAPIError(w, http.StatusServiceUnavailable, "插件市场服务未初始化")
		return
	}
	pluginID := pluginIDFromPath(r)
	branch := queryString(r, "branch", "main")
	preferLocal := queryBool(r, "prefer_local", true)
	forceRefresh := queryBool(r, "force_refresh", false)
	var (
		response pluginmarket.DocumentResponse
		err      error
	)
	if readme {
		response, err = service.GetReadme(r.Context(), pluginID, 200000, preferLocal, branch, forceRefresh)
	} else {
		response, err = service.GetChangelog(r.Context(), pluginID, 200000, preferLocal, branch, forceRefresh)
	}
	if err != nil {
		writeMarketFailure(w, err, "插件市场请求失败：")
		return
	}
	httputil.WriteJSON(w, http.StatusOK, response.Payload())
}

// handlePluginMarketplaceFeedback 对齐 GET /marketplace/{plugin_id}/feedback
func (d Deps) handlePluginMarketplaceFeedback(w http.ResponseWriter, r *http.Request) {
	if d.PluginMarket == nil {
		httputil.WriteAPIError(w, http.StatusServiceUnavailable, "插件市场服务未初始化")
		return
	}
	offset := httputil.QueryInt(r, "offset", 0, 0, 100000)
	if offset < 0 || offset > 100000 {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "offset 超出允许范围"})
		return
	}
	page, err := d.PluginMarket.Feedback(r.Context(), pluginIDFromPath(r), offset)
	if err != nil {
		writeMarketFailure(w, err, "插件市场请求失败：")
		return
	}
	httputil.WriteJSON(w, http.StatusOK, page)
}

// handlePluginMarketplaceSourceGet 对齐 GET /marketplace/source
func (d Deps) handlePluginMarketplaceSourceGet(w http.ResponseWriter, r *http.Request) {
	if d.marketService() == nil {
		httputil.WriteAPIError(w, http.StatusServiceUnavailable, "插件市场服务未初始化")
		return
	}
	settings, err := pluginmarket.LoadSourceSettings(d.InstanceDir)
	if err != nil {
		writeMarketFailure(w, err, "插件市场请求失败：")
		return
	}
	httputil.WriteJSON(w, http.StatusOK, settings)
}

// handlePluginMarketplaceSourceUpdate 对齐 PUT /marketplace/source
func (d Deps) handlePluginMarketplaceSourceUpdate(w http.ResponseWriter, r *http.Request) {
	service := d.marketService()
	if service == nil {
		httputil.WriteAPIError(w, http.StatusServiceUnavailable, "插件市场服务未初始化")
		return
	}
	var body marketplaceSourceRequest
	if err := decodePluginRequest(r, &body); err != nil {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "请求参数无效"})
		return
	}
	settings := pluginmarket.PluginSourceSettings{
		InformationSource:    body.InformationSource,
		DownloadSource:       body.DownloadSource,
		CustomDownloadPrefix: body.CustomDownloadPrefix,
	}
	if err := service.UpdateSourceSettings(d.InstanceDir, settings); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	reloaded, err := pluginmarket.LoadSourceSettings(d.InstanceDir)
	if err != nil {
		writeMarketFailure(w, err, "插件市场请求失败：")
		return
	}
	httputil.WriteJSON(w, http.StatusOK, reloaded)
}

// handlePluginMarketplaceLike 对齐 POST /marketplace/like
func (d Deps) handlePluginMarketplaceLike(w http.ResponseWriter, r *http.Request) {
	d.handlePluginVote(w, r, false)
}

// handlePluginMarketplaceDislike 对齐 POST /marketplace/dislike
func (d Deps) handlePluginMarketplaceDislike(w http.ResponseWriter, r *http.Request) {
	d.handlePluginVote(w, r, true)
}

func (d Deps) handlePluginVote(w http.ResponseWriter, r *http.Request, dislike bool) {
	if d.PluginMarket == nil {
		httputil.WriteAPIError(w, http.StatusServiceUnavailable, "插件市场服务未初始化")
		return
	}
	var body pluginmarket.VoteRequest
	if err := decodePluginRequest(r, &body); err != nil {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "请求参数无效"})
		return
	}
	reply, err := d.PluginMarket.Vote(r.Context(), body, dislike)
	if err != nil {
		writeMarketFailure(w, err, "插件市场请求失败：")
		return
	}
	httputil.WriteJSON(w, reply.StatusCode, reply.Data)
}

// handlePluginMarketplaceRate 对齐 POST /marketplace/rate
func (d Deps) handlePluginMarketplaceRate(w http.ResponseWriter, r *http.Request) {
	if d.PluginMarket == nil {
		httputil.WriteAPIError(w, http.StatusServiceUnavailable, "插件市场服务未初始化")
		return
	}
	var body pluginmarket.RatingRequest
	if err := decodePluginRequest(r, &body); err != nil {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "请求参数无效"})
		return
	}
	reply, err := d.PluginMarket.Rate(r.Context(), body)
	if err != nil {
		writeMarketFailure(w, err, "插件市场请求失败：")
		return
	}
	httputil.WriteJSON(w, reply.StatusCode, reply.Data)
}

// handlePluginMarketplaceDownload 对齐 POST /marketplace/download
func (d Deps) handlePluginMarketplaceDownload(w http.ResponseWriter, r *http.Request) {
	if d.PluginMarket == nil {
		httputil.WriteAPIError(w, http.StatusServiceUnavailable, "插件市场服务未初始化")
		return
	}
	var body pluginmarket.DownloadRequest
	if err := decodePluginRequest(r, &body); err != nil {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "请求参数无效"})
		return
	}
	reply, err := d.PluginMarket.RecordDownload(r.Context(), body)
	if err != nil {
		writeMarketFailure(w, err, "插件市场请求失败：")
		return
	}
	httputil.WriteJSON(w, reply.StatusCode, reply.Data)
}

// handlePluginMarketplaceUserStates 对齐 GET /marketplace/user-states
func (d Deps) handlePluginMarketplaceUserStates(w http.ResponseWriter, r *http.Request) {
	if d.PluginMarket == nil {
		httputil.WriteAPIError(w, http.StatusServiceUnavailable, "插件市场服务未初始化")
		return
	}
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if userID == "" {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "user_id 为必填"})
		return
	}
	reply, err := d.PluginMarket.GetUserStates(r.Context(), userID)
	if err != nil {
		writeMarketFailure(w, err, "插件市场请求失败：")
		return
	}
	httputil.WriteJSON(w, reply.StatusCode, reply.Data)
}

// handlePluginMarketplaceUserState 对齐 GET /marketplace/{plugin_id}/user-state
func (d Deps) handlePluginMarketplaceUserState(w http.ResponseWriter, r *http.Request) {
	if d.PluginMarket == nil {
		httputil.WriteAPIError(w, http.StatusServiceUnavailable, "插件市场服务未初始化")
		return
	}
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if userID == "" {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "user_id 为必填"})
		return
	}
	reply, err := d.PluginMarket.GetUserState(r.Context(), pluginIDFromPath(r), userID)
	if err != nil {
		writeMarketFailure(w, err, "插件市场请求失败：")
		return
	}
	httputil.WriteJSON(w, reply.StatusCode, reply.Data)
}

// handlePluginInstallZip 对齐 POST /plugins/install-zip
func (d Deps) handlePluginInstallZip(w http.ResponseWriter, r *http.Request) {
	service := d.marketService()
	if service == nil {
		httputil.WriteAPIError(w, http.StatusServiceUnavailable, "插件市场服务未初始化")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "请求参数无效"})
		return
	}
	defer file.Close()
	if !strings.HasSuffix(strings.ToLower(header.Filename), ".zip") {
		httputil.WriteJSON(w, http.StatusBadRequest, map[string]any{"detail": "请选择 ZIP 文件"})
		return
	}
	size, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, map[string]any{"detail": "ZIP 解压失败：" + err.Error()})
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, map[string]any{"detail": "ZIP 解压失败：" + err.Error()})
		return
	}
	readerAt, ok := file.(io.ReaderAt)
	if !ok {
		httputil.WriteJSON(w, http.StatusBadRequest, map[string]any{"detail": "ZIP 解压失败：上传文件不可随机读取"})
		return
	}
	result, err := service.InstallZipArchive(readerAt, size)
	if err != nil {
		writeMarketFailure(w, err, "ZIP 解压失败：")
		return
	}
	httputil.WriteJSON(w, http.StatusOK, result)
}

func queryBool(r *http.Request, key string, fallback bool) bool {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return value
}

func queryString(r *http.Request, key, fallback string) string {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback
	}
	return raw
}

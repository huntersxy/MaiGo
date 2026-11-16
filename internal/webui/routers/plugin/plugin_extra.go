package plugin

import (
	"MaiGo/internal/webui/httputil"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"

	"MaiGo/internal/common/network_security"
	"MaiGo/internal/plugin_market"
)

// pluginAuthMiddleware 对齐 Python 插件路由：只有版本与 Git 状态无需登录
func (d Deps) pluginAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/webui/plugins/version", "/api/webui/plugins/git-status":
			next.ServeHTTP(w, r)
		default:
			if !d.requireAuth(w, r) {
				return
			}
			if d.ValidateBody != nil && !d.ValidateBody(w, r) {
				return
			}
			next.ServeHTTP(w, r)
		}
	})
}

const (
	pluginIconMaxBytes      = 512 * 1024
	pluginIconCacheControl  = "public, max-age=3600"
	marketIconCacheControl  = "private, max-age=3600"
	pluginCloneTimeout      = 5 * time.Minute
	pluginCloneMaxAttempts  = 3
	marketIconRequestTimout = 8 * time.Second
)

var (
	pluginVersionSuffixPattern = regexp.MustCompile(`(?i)[-.](?:snapshot|dev|pre|alpha|beta|rc)`)
	pluginCloneSegmentPattern  = regexp.MustCompile(`^[\w.-]+$`)
	pluginIconSuffixes         = map[string]string{
		".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".svg": "image/svg+xml", ".webp": "image/webp",
	}
)

// handlePluginVersion 对齐 GET /api/webui/plugins/version
func (d Deps) handlePluginVersion(w http.ResponseWriter, r *http.Request) {
	version := d.buildVersion()
	major, minor, patch := parsePluginVersion(version)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"version":       version,
		"version_major": major,
		"version_minor": minor,
		"version_patch": patch,
	})
}

// parsePluginVersion 对齐 support.parse_version
func parsePluginVersion(version string) (int, int, int) {
	base := pluginVersionSuffixPattern.Split(version, 2)[0]
	parts := strings.Split(base, ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	values := make([]int, 3)
	for index := 0; index < 3; index++ {
		value, err := strconv.Atoi(strings.TrimSpace(parts[index]))
		if err != nil {
			return 0, 0, 0
		}
		values[index] = value
	}
	return values[0], values[1], values[2]
}

// handlePluginGitStatus 对齐 GET /api/webui/plugins/git-status
func (d Deps) handlePluginGitStatus(w http.ResponseWriter, r *http.Request) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"installed": false,
			"error":     "系统中未找到 Git，请先安装 Git",
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, gitPath, "--version").Output()
	if err != nil {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"installed": false,
			"error":     "Git 命令执行失败: " + err.Error(),
		})
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"installed": true,
		"version":   strings.TrimSpace(string(output)),
		"path":      gitPath,
	})
}

type pluginCloneRequest struct {
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	TargetPath string `json:"target_path"`
	Branch     string `json:"branch"`
	MirrorID   string `json:"mirror_id"`
	CustomURL  string `json:"custom_url"`
	Depth      *int   `json:"depth"`
}

// handlePluginClone 对齐 POST /api/webui/plugins/clone
func (d Deps) handlePluginClone(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	raw, ok := httputil.ReadJSONBody(w, r)
	if !ok {
		return
	}
	var body pluginCloneRequest
	if len(raw) > 0 {
		if err := sonic.ConfigDefault.Unmarshal(raw, &body); err != nil {
			httputil.WriteValidationError(w, "请求格式错误")
			return
		}
	}
	body.Owner = strings.TrimSpace(body.Owner)
	body.Repo = strings.TrimSuffix(strings.TrimSpace(body.Repo), ".git")
	body.TargetPath = strings.TrimSpace(body.TargetPath)
	body.Branch = strings.TrimSpace(body.Branch)
	body.CustomURL = strings.TrimSpace(body.CustomURL)
	if !validPluginCloneSegment(body.Owner) || !validPluginCloneSegment(body.Repo) {
		httputil.WriteValidationError(w, "owner 与 repo 必须是合法的仓库路径")
		return
	}
	if body.TargetPath == "" {
		httputil.WriteValidationError(w, "target_path 不能为空")
		return
	}
	depth := 1
	if body.Depth != nil {
		depth = *body.Depth
		if depth < 1 {
			httputil.WriteValidationError(w, "depth 必须大于等于 1")
			return
		}
	}
	pluginsRoot, err := pluginmarket.ResolvePluginsDir(d.InstanceDir)
	if err != nil {
		httputil.WriteAPIError(w, http.StatusInternalServerError, "服务器错误: "+err.Error())
		return
	}
	targetPath, err := resolvePluginCloneTarget(pluginsRoot, body.TargetPath)
	if err != nil {
		httputil.WriteAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	cloneURL, mirrorUsed, err := resolvePluginCloneURL(d.InstanceDir, body)
	if err != nil {
		httputil.WriteAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validatePluginCloneURL(cloneURL, d.enforcePublicOutbound()); err != nil {
		httputil.WriteAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := exec.LookPath("git"); err != nil {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": false, "error": "Git 未安装或不在 PATH 中", "attempts": 0, "mirror_used": mirrorUsed, "url": cloneURL,
		})
		return
	}
	result := runPluginClone(r.Context(), cloneURL, targetPath, body.Branch, depth)
	result["mirror_used"] = mirrorUsed
	result["url"] = cloneURL
	httputil.WriteJSON(w, http.StatusOK, result)
}

func validPluginCloneSegment(value string) bool {
	return value != "" && value != "." && value != ".." && pluginCloneSegmentPattern.MatchString(value)
}

// resolvePluginCloneTarget 对齐 validate_safe_path：目标必须位于 plugins 根目录内
func resolvePluginCloneTarget(pluginsRoot, targetPath string) (string, error) {
	if strings.Contains(targetPath, "..") || strings.Contains(targetPath, "\x00") {
		return "", errors.New("路径包含非法字符")
	}
	if strings.HasPrefix(targetPath, "/") || strings.HasPrefix(targetPath, "\\") || (len(targetPath) > 1 && targetPath[1] == ':') {
		return "", errors.New("不允许使用绝对路径")
	}
	rootResolved, err := filepath.Abs(pluginsRoot)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(rootResolved, targetPath))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(rootResolved, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("路径超出允许范围")
	}
	return target, nil
}

// resolvePluginCloneURL 只使用设置中的下载源，custom_url 优先
func resolvePluginCloneURL(instanceDir string, body pluginCloneRequest) (string, string, error) {
	if body.CustomURL != "" {
		return body.CustomURL, "custom", nil
	}
	settings, err := pluginmarket.LoadSourceSettings(instanceDir)
	if err != nil {
		return "", "", fmt.Errorf("读取插件下载源设置失败: %w", err)
	}
	customPrefix := ""
	if settings.CustomDownloadPrefix != nil {
		customPrefix = *settings.CustomDownloadPrefix
	}
	source, err := pluginmarket.SelectGitSource(settings.DownloadSource, customPrefix)
	if err != nil {
		return "", "", err
	}
	cloneURL, err := source.CloneURL(body.Owner, body.Repo)
	if err != nil {
		return "", "", err
	}
	return cloneURL, source.ID, nil
}

// validatePluginCloneURL 校验克隆地址，动态读取公网校验开关
func validatePluginCloneURL(raw string, requirePublicNetwork bool) error {
	_, err := networksecurity.ValidatePublicURL(raw, networksecurity.Options{
		RequirePublicNetwork: requirePublicNetwork,
		AllowFakeIP:          true,
	})
	return err
}

// runPluginClone 执行 git clone，失败重试并清理半成品目录
func runPluginClone(parent context.Context, cloneURL, targetPath, branch string, depth int) map[string]any {
	attempts := 0
	var lastError string
	for attempt := 0; attempt < pluginCloneMaxAttempts; attempt++ {
		attempts++
		_ = os.RemoveAll(targetPath)
		args := []string{"clone"}
		if branch != "" {
			args = append(args, "-b", branch)
		}
		if depth > 0 {
			args = append(args, "--depth", strconv.Itoa(depth))
		}
		args = append(args, cloneURL, targetPath)
		ctx, cancel := context.WithTimeout(parent, pluginCloneTimeout)
		// 与 plugin_market 共用同一执行器：超时/取消时终止 git 及其子进程树
		_, stderrText, runErr := pluginmarket.RunGitCommand(ctx, "", pluginCloneTimeout, args...)
		cancel()
		if runErr == nil {
			return map[string]any{"success": true, "path": targetPath, "error": nil, "attempts": attempts, "message": nil}
		}
		_ = os.RemoveAll(targetPath)
		lastError = strings.TrimSpace(stderrText)
		if lastError == "" {
			lastError = runErr.Error()
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			lastError = "克隆超时（超过 5 分钟）"
		}
	}
	return map[string]any{"success": false, "path": nil, "error": lastError, "attempts": attempts, "message": nil}
}

// handlePluginLocalIcon 对齐 GET /api/webui/plugins/icon/{plugin_id}
func (d Deps) handlePluginLocalIcon(w http.ResponseWriter, r *http.Request) {
	pluginID := strings.TrimSpace(chi.URLParam(r, "plugin_id"))
	if err := pluginmarket.ValidatePluginID(pluginID); err != nil {
		writePluginRouteFailure(w, err)
		return
	}
	pluginsRoot, err := pluginmarket.ResolvePluginsDir(d.InstanceDir)
	if err != nil {
		httputil.WriteAPIError(w, http.StatusInternalServerError, "服务器错误: "+err.Error())
		return
	}
	pluginPath, err := pluginmarket.FindInstalledPlugin(pluginsRoot, pluginID)
	if err != nil {
		httputil.WriteAPIError(w, http.StatusInternalServerError, "服务器错误: "+err.Error())
		return
	}
	if pluginPath == "" {
		httputil.WriteAPIError(w, http.StatusNotFound, "插件未安装")
		return
	}
	manifest, err := pluginmarket.LoadManifestJSON(filepath.Join(pluginPath, "_manifest.json"))
	if err != nil || manifest == nil {
		httputil.WriteAPIError(w, http.StatusNotFound, "插件 manifest 不存在或无法读取")
		return
	}
	iconPath := localPluginIconPath(manifest)
	if iconPath == "" {
		httputil.WriteAPIError(w, http.StatusNotFound, "插件未声明本地图标")
		return
	}
	mediaType, err := validateLocalPluginIconPath(iconPath)
	if err != nil {
		httputil.WriteAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	resolved, err := resolvePluginIconFile(pluginPath, iconPath)
	if err != nil {
		httputil.WriteAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() {
		httputil.WriteAPIError(w, http.StatusNotFound, "插件图标不存在")
		return
	}
	if info.Size() > pluginIconMaxBytes {
		httputil.WriteAPIError(w, http.StatusBadRequest, "插件图标文件过大")
		return
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		httputil.WriteAPIError(w, http.StatusInternalServerError, "服务器错误: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Cache-Control", pluginIconCacheControl)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(raw)
}

func localPluginIconPath(manifest map[string]any) string {
	display, _ := manifest["display"].(map[string]any)
	if display == nil {
		return ""
	}
	icon, _ := display["icon"].(map[string]any)
	if icon == nil || strings.TrimSpace(stringFromAny(icon["type"])) != "local" {
		return ""
	}
	return strings.TrimSpace(stringFromAny(icon["value"]))
}

func validateLocalPluginIconPath(iconPath string) (string, error) {
	if filepath.IsAbs(iconPath) || strings.Contains(iconPath, "..") || strings.Contains(iconPath, "\x00") || strings.HasPrefix(iconPath, "/") || strings.HasPrefix(iconPath, "\\") {
		return "", errors.New("图标路径必须是插件目录内的相对路径")
	}
	mediaType, ok := pluginIconSuffixes[strings.ToLower(filepath.Ext(iconPath))]
	if !ok {
		return "", errors.New("不支持的图标文件类型")
	}
	return mediaType, nil
}

func resolvePluginIconFile(pluginRoot, iconPath string) (string, error) {
	rootResolved, err := filepath.Abs(pluginRoot)
	if err != nil {
		return "", err
	}
	target := filepath.Join(rootResolved, iconPath)
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", errors.New("图标路径超出插件目录")
	}
	relative, err := filepath.Rel(rootResolved, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("图标路径超出插件目录")
	}
	return resolved, nil
}

// handlePluginMarketIcon 对齐 GET /api/webui/plugins/market-icon
func (d Deps) handlePluginMarketIcon(w http.ResponseWriter, r *http.Request) {
	rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
	const prefix = "https://cnb.cool/Mai-with-u/plugin-repo/-/git/raw/"
	parsed, err := url.Parse(rawURL)
	if err != nil || !strings.HasPrefix(rawURL, prefix) || hasParentPathSegment(parsed.Path) {
		httputil.WriteAPIError(w, http.StatusBadRequest, "只能读取 CNB 插件市场仓库图标")
		return
	}

	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, rawURL, nil)
	if err != nil {
		httputil.WriteAPIError(w, http.StatusBadGateway, "获取 CNB 插件图标失败：无效的 URL")
		return
	}
	request.Header.Set("User-Agent", "MaiBot")
	request.Header.Set("Accept", "image/*")
	client := d.marketIconClientOrDefault()
	if client == nil {
		client = &http.Client{
			Timeout: marketIconRequestTimout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	response, err := client.Do(request)
	if err != nil {
		httputil.WriteAPIError(w, http.StatusBadGateway, "获取 CNB 插件图标失败："+err.Error())
		return
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		httputil.WriteAPIError(w, http.StatusBadGateway, fmt.Sprintf("获取 CNB 插件图标失败：HTTP %d", response.StatusCode))
		return
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !isPluginIconMediaType(mediaType) {
		httputil.WriteAPIError(w, http.StatusBadGateway, "获取 CNB 插件图标失败：图标响应类型无效")
		return
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, pluginIconMaxBytes+1))
	if err != nil {
		httputil.WriteAPIError(w, http.StatusBadGateway, "获取 CNB 插件图标失败："+err.Error())
		return
	}
	if len(content) > pluginIconMaxBytes {
		httputil.WriteAPIError(w, http.StatusBadGateway, "获取 CNB 插件图标失败：插件图标文件过大")
		return
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Cache-Control", marketIconCacheControl)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(content)
}

func hasParentPathSegment(path string) bool {
	for _, segment := range strings.Split(strings.ReplaceAll(path, "\\", "/"), "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func isPluginIconMediaType(mediaType string) bool {
	switch strings.ToLower(mediaType) {
	case "image/png", "image/jpeg", "image/webp", "image/svg+xml":
		return true
	}
	return false
}

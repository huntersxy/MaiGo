package memorix

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

// RemoteHost 通过 HTTP/JSON 调用独立运行的 Python A_Memorix 宿主（侧车）。
//
// 基址由 MAIBOT_MEMORIX_REMOTE_URL 指定；请求与响应字段为 snake_case，与
// host_service.invoke 的载荷对齐。端点包括 /v1/memorix/{search,ingest_text,
// read,admin,invoke}，其中 /invoke 的 404/405/501 视为显式 unsupported。
//
// 侧车协议细节见 agents/MODULE_ALIGNMENT.md。
type RemoteHost struct {
	baseURL    string
	httpClient *http.Client
	token      string
}

// RemoteHostOptions 远程 Host 客户端选项
type RemoteHostOptions struct {
	Timeout time.Duration
	Token   string
}

// NewRemoteHost 构造 RemoteHost；baseURL 必须含 scheme 与 host
func NewRemoteHost(baseURL string, opts RemoteHostOptions) (*RemoteHost, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("memorix remote base url 为空")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("memorix remote url 无效: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("memorix remote url 缺少 scheme 或 host: %q", baseURL)
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &RemoteHost{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		token: strings.TrimSpace(opts.Token),
	}, nil
}

// InvokeAdmin 代理 WebUI 管理调用到侧车 POST /v1/memorix/invoke
// 侧车未实现该入口时返回 AdminUnsupportedError，避免把能力边界误报成 500
func (h *RemoteHost) InvokeAdmin(ctx context.Context, component string, args map[string]any, timeoutMs *int) (any, error) {
	if h == nil {
		return nil, NewAdminUnsupportedError("remote", component, adminActionOf(args), "remote_host_not_configured")
	}
	if timeoutMs != nil && *timeoutMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(*timeoutMs)*time.Millisecond)
		defer cancel()
	}
	payload := cloneMap(args)
	body := map[string]any{
		"component": strings.TrimSpace(component),
		"args":      payload,
	}
	if timeoutMs != nil && *timeoutMs > 0 {
		body["timeout_ms"] = *timeoutMs
	}
	status, raw, err := h.doRequest(ctx, http.MethodPost, "/v1/memorix/invoke", body)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound || status == http.StatusMethodNotAllowed || status == http.StatusNotImplemented {
		return nil, NewAdminUnsupportedError("remote", component, adminActionOf(args), "remote_invoke_not_supported")
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("memorix remote invoke HTTP %d: %s", status, remoteBodySnippet(raw))
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var out any
	if err := sonic.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("memorix remote invoke 解码失败: %w", err)
	}
	return out, nil
}

func adminActionOf(args map[string]any) string {
	if value, ok := args["action"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func remoteBodySnippet(raw []byte) string {
	snippet := strings.TrimSpace(string(raw))
	if len(snippet) > 512 {
		snippet = snippet[:512] + "…"
	}
	return snippet
}

func (h *RemoteHost) IngestMessage(ctx context.Context, req IngestMessageRequest) (IngestResult, error) {
	if h == nil {
		return IngestResult{Success: false, Detail: "remote host 未配置"}, nil
	}
	body := map[string]any{
		"external_id":    req.ExternalID,
		"source_type":    req.SourceType,
		"text":           req.Text,
		"chat_id":        req.ChatID,
		"person_ids":     append([]string(nil), req.PersonIDs...),
		"participants":   append([]string(nil), req.Participants...),
		"timestamp":      timePtrToRemoteAny(req.Timestamp),
		"time_start":     timePtrToRemoteAny(req.TimeStart),
		"time_end":       timePtrToRemoteAny(req.TimeEnd),
		"tags":           append([]string(nil), req.Tags...),
		"metadata":       cloneMap(req.Metadata),
		"respect_filter": req.RespectFilter,
		"user_id":        req.UserID,
		"group_id":       req.GroupID,
		"entities":       []string{},
		"relations":      []any{},
	}
	if mt := strings.TrimSpace(req.MemoryType); mt != "" {
		body["memory_type"] = mt
	}
	if label := strings.TrimSpace(req.Label); label != "" {
		body["label"] = label
	}
	var out IngestResult
	if err := h.postJSON(ctx, "/v1/memorix/ingest_text", body, &out); err != nil {
		return IngestResult{}, err
	}
	return out, nil
}

func (h *RemoteHost) Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	if h == nil {
		return SearchResult{Success: false, Error: "remote host 未配置"}, nil
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 5
	}
	body := map[string]any{
		"query":          strings.TrimSpace(req.Query),
		"limit":          limit,
		"mode":           strings.TrimSpace(req.Mode),
		"chat_id":        req.ChatID,
		"person_id":      req.PersonID,
		"time_start":     req.TimeStart,
		"time_end":       req.TimeEnd,
		"respect_filter": req.RespectFilter,
		"user_id":        req.UserID,
		"group_id":       req.GroupID,
	}
	if body["mode"] == "" {
		body["mode"] = "search"
	}
	var out SearchResult
	if err := h.postJSON(ctx, "/v1/memorix/search", body, &out); err != nil {
		return SearchResult{}, err
	}
	return out, nil
}

func (h *RemoteHost) Read(ctx context.Context, req ReadRequest) (ReadResult, error) {
	if h == nil {
		return ReadResult{Success: false, Error: "remote host 未配置"}, nil
	}
	body := map[string]any{
		"item_id":    req.ID,
		"episode_id": strings.TrimSpace(req.EpisodeID),
	}
	var out ReadResult
	if err := h.postJSON(ctx, "/v1/memorix/read", body, &out); err != nil {
		return ReadResult{}, err
	}
	return out, nil
}

func (h *RemoteHost) AdminList(ctx context.Context, req AdminListRequest) (AdminListResult, error) {
	if h == nil {
		return AdminListResult{Success: false, Error: "remote host 未配置"}, nil
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	body := map[string]any{
		"domain": req.Domain,
		"action": req.Action,
		"limit":  limit,
		"cursor": req.Cursor,
		"status": req.Status,
	}
	var out AdminListResult
	if err := h.postJSON(ctx, "/v1/memorix/admin_list", body, &out); err != nil {
		return AdminListResult{}, err
	}
	return out, nil
}

func (h *RemoteHost) Health(ctx context.Context) (HealthResult, error) {
	if h == nil {
		return HealthResult{Success: false, Message: "remote host 未配置"}, nil
	}
	var out HealthResult
	if err := h.getJSON(ctx, "/v1/memorix/health", &out); err != nil {
		return HealthResult{}, err
	}
	if out.Backend == "" {
		out.Backend = "remote"
	}
	return out, nil
}

func (h *RemoteHost) postJSON(ctx context.Context, path string, body any, dest any) error {
	status, raw, err := h.doRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("memorix remote HTTP %d: %s", status, remoteBodySnippet(raw))
	}
	if len(raw) == 0 {
		return nil
	}
	if err := sonic.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("memorix remote 解码失败: %w", err)
	}
	return nil
}

func (h *RemoteHost) getJSON(ctx context.Context, path string, dest any) error {
	status, raw, err := h.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("memorix remote HTTP %d: %s", status, remoteBodySnippet(raw))
	}
	if len(raw) == 0 {
		return nil
	}
	if err := sonic.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("memorix remote 解码失败: %w", err)
	}
	return nil
}

func (h *RemoteHost) applyAuth(req *http.Request) {
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
}

func (h *RemoteHost) doRequest(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		payload, err := sonic.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("memorix remote 编码失败: %w", err)
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.baseURL+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	h.applyAuth(req)
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("memorix remote 请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("memorix remote 读响应失败: %w", err)
	}
	return resp.StatusCode, raw, nil
}

func timePtrToRemoteAny(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.Unix()
}

func cloneMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// RemoteURLFromEnv 读取 MAIBOT_MEMORIX_REMOTE_URL
func RemoteURLFromEnv() string {
	return strings.TrimSpace(os.Getenv("MAIBOT_MEMORIX_REMOTE_URL"))
}

// RemoteHostOptionsFromEnv 从环境变量构造 RemoteHostOptions
func RemoteHostOptionsFromEnv() RemoteHostOptions {
	opts := RemoteHostOptions{
		Token: strings.TrimSpace(os.Getenv("MAIBOT_MEMORIX_REMOTE_TOKEN")),
	}
	if raw := strings.TrimSpace(os.Getenv("MAIBOT_MEMORIX_REMOTE_TIMEOUT")); raw != "" {
		if sec, err := strconv.Atoi(raw); err == nil && sec > 0 {
			opts.Timeout = time.Duration(sec) * time.Second
		}
	}
	return opts
}

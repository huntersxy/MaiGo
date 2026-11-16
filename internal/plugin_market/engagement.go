package pluginmarket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/bytedance/sonic"
)

// MarketReply 对齐 engagement.MarketReply
type MarketReply struct {
	StatusCode int
	Data       map[string]any
}

// VoteRequest 对齐 engagement.VoteRequest
type VoteRequest struct {
	PluginID string `json:"plugin_id"`
	UserID   string `json:"user_id"`
}

// RatingRequest 对齐 engagement.RatingRequest，可选字段用指针区分是否出现
type RatingRequest struct {
	PluginID  string  `json:"plugin_id"`
	UserID    string  `json:"user_id"`
	Rating    *int    `json:"rating"`
	Comment   *string `json:"comment"`
	Username  *string `json:"username"`
	Anonymous *bool   `json:"anonymous"`
	// commentSet 记录请求体是否出现过 comment 键（含显式 null），
	// 对齐 Python `"comment" in model_fields_set` 的判定
	commentSet bool
}

// UnmarshalJSON 额外记录 comment 键是否出现，供「评分或评论至少一项」判定
func (r *RatingRequest) UnmarshalJSON(data []byte) error {
	type plain RatingRequest
	var decoded plain
	if err := sonic.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := sonic.Unmarshal(data, &fields); err == nil {
		_, decoded.commentSet = fields["comment"]
	}
	*r = RatingRequest(decoded)
	return nil
}

// DownloadRequest 对齐 engagement.DownloadRequest
type DownloadRequest struct {
	PluginID    string  `json:"plugin_id"`
	UserID      *string `json:"user_id"`
	Fingerprint *string `json:"fingerprint"`
}

// Validate 对齐 VoteRequest 的字段约束
func (r *VoteRequest) Validate() error {
	if message := lengthError("plugin_id", r.PluginID, 1, 200); message != "" {
		return errors.New(message)
	}
	if message := lengthError("user_id", r.UserID, 1, 300); message != "" {
		return errors.New(message)
	}
	return nil
}

// Validate 对齐 RatingRequest 的字段约束与「评分或评论至少一项」规则
func (r *RatingRequest) Validate() error {
	if message := lengthError("plugin_id", r.PluginID, 1, 200); message != "" {
		return errors.New(message)
	}
	if message := lengthError("user_id", r.UserID, 1, 300); message != "" {
		return errors.New(message)
	}
	if r.Rating != nil && (*r.Rating < 1 || *r.Rating > 5) {
		return fmt.Errorf("rating 必须在 1 到 5 之间")
	}
	if r.Comment != nil && len([]rune(*r.Comment)) > 500 {
		return fmt.Errorf("comment 长度不能超过 500")
	}
	if r.Username != nil && len([]rune(*r.Username)) > 100 {
		return fmt.Errorf("username 长度不能超过 100")
	}
	// 对齐 Python：rating 必须显式给出且非 null；comment 只要出现在请求体即算提供（可为 null）
	if r.Rating == nil && !r.commentSet {
		return fmt.Errorf("rating 和 comment 至少需要提供一个")
	}
	anonymous := r.Anonymous != nil && *r.Anonymous
	if !anonymous && r.Username != nil {
		trimmed := strings.TrimSpace(*r.Username)
		if trimmed == "" {
			return fmt.Errorf("非匿名评价的用户名不能为空")
		}
		r.Username = &trimmed
	}
	return nil
}

// Validate 对齐 DownloadRequest 的字段约束
func (r *DownloadRequest) Validate() error {
	if message := lengthError("plugin_id", r.PluginID, 1, 200); message != "" {
		return errors.New(message)
	}
	if r.UserID != nil {
		if message := lengthError("user_id", *r.UserID, 1, 300); message != "" {
			return errors.New(message)
		}
	}
	if r.Fingerprint != nil {
		if message := lengthError("fingerprint", *r.Fingerprint, 1, 300); message != "" {
			return errors.New(message)
		}
	}
	return nil
}

func lengthError(field, value string, minLength, maxLength int) string {
	length := len([]rune(value))
	if length < minLength {
		return fmt.Sprintf("%s 长度不能少于 %d", field, minLength)
	}
	if length > maxLength {
		return fmt.Sprintf("%s 长度不能超过 %d", field, maxLength)
	}
	return ""
}

// marketRequest 对齐 engagement._request：统计、评论与用户状态共用市场地址
func (s *Service) marketRequest(ctx context.Context, method, path string, payload map[string]any) (MarketReply, error) {
	targetURL := marketBaseURL() + path
	var body io.Reader
	if payload != nil {
		encoded, err := sonic.Marshal(payload)
		if err != nil {
			return MarketReply{}, err
		}
		body = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, body)
	if err != nil {
		return MarketReply{}, &MarketError{StatusCode: 502, Message: "插件市场互动请求失败：" + targetURL + " - " + err.Error()}
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{
		Timeout: requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return MarketReply{}, &MarketError{StatusCode: 502, Message: "插件市场互动请求失败：" + targetURL + " - " + err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return MarketReply{}, &MarketError{StatusCode: 502, Message: "插件市场互动请求失败：" + targetURL + " - " + err.Error()}
	}
	var decoded any
	if err := sonic.Unmarshal(raw, &decoded); err != nil {
		return MarketReply{}, fmt.Errorf("插件市场互动响应解析失败: %w", err)
	}
	data, ok := decoded.(map[string]any)
	if !ok {
		return MarketReply{}, fmt.Errorf("插件市场互动响应必须为对象")
	}
	if method != http.MethodGet && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if success, ok := data["success"].(bool); !ok || success {
			s.cache.Invalidate(marketBaseURL() + "/stats/") // 含 /stats/summary
		}
	}
	return MarketReply{StatusCode: resp.StatusCode, Data: data}, nil
}

// GetUserState 对齐 engagement.get_user_state
func (s *Service) GetUserState(ctx context.Context, pluginID, userID string) (MarketReply, error) {
	if message := lengthError("plugin_id", pluginID, 1, 200); message != "" {
		return MarketReply{}, errors.New(message)
	}
	if message := lengthError("user_id", userID, 1, 300); message != "" {
		return MarketReply{}, errors.New(message)
	}
	query := url.Values{}
	query.Set("plugin_id", pluginID)
	query.Set("user_id", userID)
	return s.marketRequest(ctx, http.MethodGet, "/stats/user-state?"+query.Encode(), nil)
}

// GetUserStates 对齐 engagement.get_user_states
func (s *Service) GetUserStates(ctx context.Context, userID string) (MarketReply, error) {
	if message := lengthError("user_id", userID, 1, 300); message != "" {
		return MarketReply{}, errors.New(message)
	}
	query := url.Values{}
	query.Set("user_id", userID)
	return s.marketRequest(ctx, http.MethodGet, "/stats/user-states?"+query.Encode(), nil)
}

// Vote 对齐 engagement.vote
func (s *Service) Vote(ctx context.Context, request VoteRequest, dislike bool) (MarketReply, error) {
	if err := request.Validate(); err != nil {
		return MarketReply{}, err
	}
	path := "/stats/like"
	if dislike {
		path = "/stats/dislike"
	}
	return s.marketRequest(ctx, http.MethodPost, path, map[string]any{
		"plugin_id": request.PluginID,
		"user_id":   request.UserID,
	})
}

// Rate 对齐 engagement.rate
func (s *Service) Rate(ctx context.Context, request RatingRequest) (MarketReply, error) {
	if err := request.Validate(); err != nil {
		return MarketReply{}, err
	}
	payload := map[string]any{
		"plugin_id": request.PluginID,
		"user_id":   request.UserID,
	}
	if request.Rating != nil {
		payload["rating"] = *request.Rating
	}
	if request.Comment != nil {
		payload["comment"] = *request.Comment
	}
	if request.Username != nil {
		payload["username"] = *request.Username
	}
	if request.Anonymous != nil {
		payload["anonymous"] = *request.Anonymous
	}
	anonymous := request.Anonymous != nil && *request.Anonymous
	if anonymous {
		delete(payload, "username")
	}
	return s.marketRequest(ctx, http.MethodPost, "/stats/rate", payload)
}

// RecordDownload 对齐 engagement.record_download
func (s *Service) RecordDownload(ctx context.Context, request DownloadRequest) (MarketReply, error) {
	if err := request.Validate(); err != nil {
		return MarketReply{}, err
	}
	payload := map[string]any{"plugin_id": request.PluginID}
	if request.UserID != nil {
		payload["user_id"] = *request.UserID
	} else {
		payload["user_id"] = nil
	}
	if request.Fingerprint != nil {
		payload["fingerprint"] = *request.Fingerprint
	} else {
		payload["fingerprint"] = nil
	}
	return s.marketRequest(ctx, http.MethodPost, "/stats/download", payload)
}

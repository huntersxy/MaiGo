package pluginmarket

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/bytedance/sonic"
)

const defaultFeedbackBaseURL = "https://plugin.maibot.chat"

// DeveloperReply 对齐 feedback.DeveloperReply
type DeveloperReply struct {
	Body    string `json:"body"`
	Login   string `json:"login"`
	Updated int64  `json:"updated"`
}

// PublicReview 对齐 feedback.PublicReview
type PublicReview struct {
	ID        string          `json:"id"`
	Comment   string          `json:"comment"`
	Rating    *int            `json:"rating"`
	Username  *string         `json:"username"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
	Reply     *DeveloperReply `json:"reply"`
}

// FeedbackPage 对齐 feedback.FeedbackPage
type FeedbackPage struct {
	PluginID   string         `json:"plugin_id"`
	SnapshotAt int64          `json:"snapshot_at"`
	Items      []PublicReview `json:"items"`
	Total      int            `json:"total"`
	NextOffset *int           `json:"next_offset"`
}

// Feedback 对齐 feedback.get_feedback：直接读取配对好的评论与回复
func (s *Service) Feedback(ctx context.Context, pluginID string, offset int) (FeedbackPage, error) {
	s.mu.RLock()
	baseURL := s.feedbackBaseURL
	s.mu.RUnlock()
	if baseURL == "" {
		baseURL = defaultFeedbackBaseURL
	}
	query := url.Values{}
	query.Set("offset", strconv.Itoa(offset))
	query.Set("limit", "20")
	targetURL := baseURL + "/api/feedback/" + quoteSegment(pluginID) + "?" + query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return FeedbackPage{}, &MarketError{StatusCode: 502, Message: "无法连接评论与开发者回复服务，请稍后重试"}
	}
	req.Header.Set("User-Agent", "MaiBot-WebUI/1.0 (+https://plugin.maibot.chat)")
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return FeedbackPage{}, &MarketError{StatusCode: 502, Message: "无法连接评论与开发者回复服务，请稍后重试"}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return FeedbackPage{}, &MarketError{StatusCode: 502, Message: "无法连接评论与开发者回复服务，请稍后重试"}
	}
	if resp.StatusCode == http.StatusNotFound {
		return FeedbackPage{}, &MarketError{StatusCode: 404, Message: "该插件暂未收录到评论与回复服务"}
	}
	if resp.StatusCode >= 400 {
		return FeedbackPage{}, &MarketError{StatusCode: 502, Message: "评论与开发者回复服务暂不可用，请稍后重试"}
	}
	var decoded any
	if err := sonic.Unmarshal(raw, &decoded); err != nil {
		return FeedbackPage{}, &MarketError{StatusCode: 502, Message: "评论与开发者回复服务返回的数据格式无效"}
	}
	payload, ok := decoded.(map[string]any)
	if !ok {
		return FeedbackPage{}, &MarketError{StatusCode: 502, Message: "评论与开发者回复服务返回的数据格式无效"}
	}
	page, err := parseFeedbackPage(payload)
	if err != nil {
		return FeedbackPage{}, &MarketError{StatusCode: 502, Message: "评论与开发者回复服务返回的数据格式无效"}
	}
	return page, nil
}

func parseFeedbackPage(payload map[string]any) (FeedbackPage, error) {
	page := FeedbackPage{
		PluginID:   safeString(payload["plugin_id"]),
		SnapshotAt: int64(safeInt(payload["snapshot_at"], 0)),
		Total:      safeInt(payload["total"], -1),
		Items:      []PublicReview{},
	}
	if page.PluginID == "" || page.Total < 0 {
		return FeedbackPage{}, fmt.Errorf("评论与回复字段无效")
	}
	if payload["next_offset"] != nil {
		next := safeInt(payload["next_offset"], -1)
		if next < 0 {
			return FeedbackPage{}, fmt.Errorf("next_offset 无效")
		}
		page.NextOffset = &next
	}
	items, ok := anyList(payload["items"])
	if !ok {
		return FeedbackPage{}, fmt.Errorf("items 必须为列表")
	}
	for _, item := range items {
		reviewMap, ok := anyMap(item)
		if !ok {
			return FeedbackPage{}, fmt.Errorf("items 中存在非对象条目")
		}
		review := PublicReview{
			ID:        safeString(reviewMap["id"]),
			Comment:   safeString(reviewMap["comment"]),
			CreatedAt: safeString(reviewMap["created_at"]),
			UpdatedAt: safeString(reviewMap["updated_at"]),
		}
		if review.ID == "" || review.CreatedAt == "" || review.UpdatedAt == "" {
			return FeedbackPage{}, fmt.Errorf("评论缺少必填字段")
		}
		if reviewMap["rating"] != nil {
			rating := safeInt(reviewMap["rating"], 0)
			review.Rating = &rating
		}
		if reviewMap["username"] != nil {
			username := safeString(reviewMap["username"])
			review.Username = &username
		}
		if reviewMap["reply"] != nil {
			replyMap, ok := anyMap(reviewMap["reply"])
			if !ok {
				return FeedbackPage{}, fmt.Errorf("reply 必须为对象")
			}
			review.Reply = &DeveloperReply{
				Body:    safeString(replyMap["body"]),
				Login:   safeString(replyMap["login"]),
				Updated: int64(safeInt(replyMap["updated"], 0)),
			}
		}
		page.Items = append(page.Items, review)
	}
	return page, nil
}

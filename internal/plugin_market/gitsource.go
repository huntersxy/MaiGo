package pluginmarket

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bytedance/sonic"
)

// GitSource 对齐 common.git_source.GitSource：信息源与下载源共用同一套 URL 规则
type GitSource struct {
	ID          string
	Name        string
	ClonePrefix string
	RawPrefix   string
}

var gitPathSegmentPattern = regexp.MustCompile(`^[\w.-]+$`)

// SelectGitSource 对齐 GitSource.select：仅支持 github、cnb 与自定义 HTTPS 前缀
func SelectGitSource(sourceID, customPrefix string) (GitSource, error) {
	switch sourceID {
	case "github":
		return GitSource{ID: sourceID, Name: "GitHub 官方源", ClonePrefix: "https://github.com", RawPrefix: "https://raw.githubusercontent.com"}, nil
	case "cnb":
		return GitSource{ID: sourceID, Name: "CNB 镜像", ClonePrefix: "https://cnb.cool/Mai-with-u", RawPrefix: "https://cnb.cool/Mai-with-u"}, nil
	case "custom":
		prefix := strings.TrimRight(strings.TrimSpace(customPrefix), "/")
		parsed, err := url.Parse(prefix)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return GitSource{}, fmt.Errorf("自定义源必须是无认证信息、查询参数和片段的 HTTPS 前缀")
		}
		return GitSource{ID: sourceID, Name: "自定义源", ClonePrefix: prefix, RawPrefix: prefix}, nil
	}
	return GitSource{}, fmt.Errorf("不支持的 Git 源: %s", sourceID)
}

// CNBRepositoryName 对齐 cnb_repository_name：固定镜像与哈希镜像名保持同一规则
func CNBRepositoryName(owner, repo string) (string, error) {
	for _, part := range []string{owner, repo} {
		if !gitPathSegmentPattern.MatchString(part) || part == "." || part == ".." {
			return "", fmt.Errorf("无效的 GitHub 仓库路径")
		}
	}
	source := strings.ToLower(owner) + "/" + strings.ToLower(strings.TrimSuffix(repo, ".git"))
	if source == "mai-with-u/plugin-repo" || source == "mai-with-u/maibot" {
		return strings.Split(source, "/")[1], nil
	}
	sum := sha256.Sum256([]byte(source))
	digest := hex.EncodeToString(sum[:])[:12]
	readable := make([]rune, 0, len(source))
	for _, r := range strings.ReplaceAll(source, "/", "--") {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			readable = append(readable, r)
		} else {
			readable = append(readable, '-')
		}
	}
	if len(readable) > 46 {
		readable = readable[:46]
	}
	return "p-" + string(readable) + "-" + digest, nil
}

// CloneURL 对齐 GitSource.clone_url
func (g GitSource) CloneURL(owner, repo string) (string, error) {
	path := owner + "/" + repo
	if g.ID == "cnb" {
		cnbName, err := CNBRepositoryName(owner, repo)
		if err != nil {
			return "", err
		}
		path = cnbName
	}
	return fmt.Sprintf("%s/%s.git", g.ClonePrefix, path), nil
}

// RawURL 对齐 GitSource.raw_url
func (g GitSource) RawURL(owner, repo, branch, filePath string) (string, error) {
	for _, part := range strings.Split(filePath, "/") {
		if part == "." || part == ".." {
			return "", fmt.Errorf("无效的文件路径")
		}
	}
	path := owner + "/" + repo
	if g.ID == "cnb" {
		cnbName, err := CNBRepositoryName(owner, repo)
		if err != nil {
			return "", err
		}
		path = cnbName + "/-/git/raw"
	}
	return fmt.Sprintf("%s/%s/%s/%s", g.RawPrefix, path, quoteSegment(branch), quotePath(filePath)), nil
}

// APIURL 对齐 GitSource.api_url：仓库元数据 API 仅 GitHub 支持
func (g GitSource) APIURL(owner, repo, path string) (string, error) {
	if g.ID != "github" {
		return "", fmt.Errorf("仓库元数据 API 仅支持 GitHub 信息源")
	}
	return fmt.Sprintf("https://api.github.com/repos/%s/%s/%s", owner, repo, path), nil
}

// quoteSegment 对齐 Python `quote(value, safe=”)`：只保留 unreserved 字符
func quoteSegment(value string) string {
	return pyQuote(value, "")
}

// quotePath 对齐 Python `quote(value, safe='/')`：保留路径分隔符
func quotePath(value string) string {
	return pyQuote(value, "/")
}

// pyQuote 复刻 urllib.parse.quote：unreserved（字母数字与 `_.-~`）与 safe 字符原样保留，
// 其余按 UTF-8 字节百分号编码。Go 的 url.PathEscape 会保留 `:@&=+$`，与 Python 不同。
func pyQuote(value, safe string) string {
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		char := value[index]
		if isQuoteUnreserved(char) || strings.IndexByte(safe, char) >= 0 {
			builder.WriteByte(char)
			continue
		}
		builder.WriteString(fmt.Sprintf("%%%02X", char))
	}
	return builder.String()
}

func isQuoteUnreserved(char byte) bool {
	switch {
	case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
		return true
	case char == '_', char == '.', char == '-', char == '~':
		return true
	default:
		return false
	}
}

// ReadText 对齐 GitSource.read_text：禁止重定向绕过调用方的 URL 校验
func (g GitSource) ReadText(ctx context.Context, targetURL string, timeout time.Duration) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "MaiBot")

	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return "", &HTTPStatusError{Code: resp.StatusCode, URL: targetURL, Reason: strings.TrimSpace(http.StatusText(resp.StatusCode))}
	}
	// 对齐 Python _NoRedirect + urllib：3xx 视为错误，避免绕过调用方的 URL 校验
	if resp.StatusCode >= http.StatusMultipleChoices && resp.StatusCode < http.StatusBadRequest {
		return "", &HTTPStatusError{Code: resp.StatusCode, URL: targetURL, Reason: strings.TrimSpace(http.StatusText(resp.StatusCode))}
	}
	if !utf8.Valid(body) {
		return "", fmt.Errorf("响应不是有效的 UTF-8: %s", targetURL)
	}
	return string(body), nil
}

// ReadJSON 对齐 GitSource.read_json：只接受 JSON 对象
func (g GitSource) ReadJSON(ctx context.Context, targetURL string, timeout time.Duration) (map[string]any, error) {
	raw, err := g.ReadText(ctx, targetURL, timeout)
	if err != nil {
		return nil, err
	}
	var payload any
	if err := sonic.UnmarshalString(raw, &payload); err != nil {
		return nil, err
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("API 响应不是对象")
	}
	return object, nil
}

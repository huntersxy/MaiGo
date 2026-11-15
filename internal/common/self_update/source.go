package selfupdate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"MaiGo/internal/common/version"

	"github.com/pelletier/go-toml/v2"
)

const (
	pypiJSONURL = "https://pypi.org/pypi/maibot/json"
	pypiIndex   = "https://pypi.org/simple"
)

var packageIndexes = map[string]struct {
	Name string
	URL  string
}{
	"pypi":   {Name: "PyPI 官方", URL: pypiIndex},
	"tuna":   {Name: "清华镜像", URL: "https://pypi.tuna.tsinghua.edu.cn/simple"},
	"aliyun": {Name: "阿里镜像", URL: "https://mirrors.aliyun.com/pypi/simple"},
}

type updateSource struct {
	ID       string
	Name     string
	CloneURL string
	APIType  string
	IndexURL string
}

func selectUpdateSource(sourceID, customPrefix string, requirePublicNetwork bool) (updateSource, error) {
	switch normalizeSourceID(sourceID) {
	case "github":
		return updateSource{
			ID: "github", Name: "GitHub 官方源", APIType: "github",
			CloneURL: "https://github.com/Mai-with-u/MaiBot.git",
		}, nil
	case "cnb":
		return updateSource{
			ID: "cnb", Name: "CNB 镜像", APIType: "cnb",
			CloneURL: "https://cnb.cool/Mai-with-u/maibot.git",
		}, nil
	case "custom":
		prefix, err := validateCustomPrefix(customPrefix, requirePublicNetwork)
		if err != nil {
			return updateSource{}, err
		}
		return updateSource{
			ID: "custom", Name: "自定义源", APIType: "custom",
			CloneURL: prefix + "/Mai-with-u/MaiBot.git",
		}, nil
	default:
		return updateSource{}, errorf("invalid_source", "不支持的 Git 源: %s", sourceID)
	}
}

func validateCustomPrefix(value string, requirePublicNetwork bool) (string, error) {
	return validatePublicPrefix(value, requirePublicNetwork)
}

func packageIndex(sourceID string) (string, error) {
	entry, ok := packageIndexes[normalizeSourceID(sourceID)]
	if !ok {
		return "", errorf("invalid_source", "包安装更新只支持 PyPI 官方、清华或阿里")
	}
	return entry.URL, nil
}

func packageSourceName(sourceID string) string {
	entry, ok := packageIndexes[normalizeSourceID(sourceID)]
	if !ok {
		return ""
	}
	return entry.Name
}

func publicUpdateSource(source updateSource) *UpdateSource {
	return &UpdateSource{ID: source.ID, Name: source.Name, URL: source.CloneURL}
}

func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func getJSON(ctx context.Context, client *http.Client, requestURL string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "MaiBot")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return errorf("fetch_failed", "请求 %s 失败: HTTP %d %s", requestURL, response.StatusCode, strings.TrimSpace(string(body)))
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 8<<20))
	if err := decoder.Decode(target); err != nil {
		return errorf("fetch_failed", "解析 %s 失败: %v", requestURL, err)
	}
	return nil
}

func getText(ctx context.Context, client *http.Client, requestURL string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "MaiBot")
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", errorf("fetch_failed", "请求 %s 失败: HTTP %d", requestURL, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

type packageReleasePayload struct {
	Releases map[string][]struct {
		Yanked bool `json:"yanked"`
	} `json:"releases"`
}

var (
	simpleAnchorPattern = regexp.MustCompile(`(?is)<a\s+([^>]*)>`)
	simpleHrefPattern   = regexp.MustCompile(`(?i)href\s*=\s*["']([^"']+)["']`)
	releaseFilePattern  = regexp.MustCompile(`(?i)^maibot-(\d+\.\d+\.\d+)(?:\.tar\.gz|-[^/]*\.whl)`)
)

func fetchPackageReleases(ctx context.Context, sourceID string) (packageReleasePayload, error) {
	selected := normalizeSourceID(sourceID)
	index, err := packageIndex(selected)
	if err != nil {
		return packageReleasePayload{}, err
	}
	client := newHTTPClient()
	if selected == "pypi" {
		var payload packageReleasePayload
		if err := getJSON(ctx, client, pypiJSONURL, &payload); err != nil {
			return packageReleasePayload{}, errorf("fetch_failed", "无法从 PyPI 官方检查正式版本：%v", err)
		}
		return payload, nil
	}
	text, err := getText(ctx, client, strings.TrimRight(index, "/")+"/maibot/")
	if err != nil {
		return packageReleasePayload{}, errorf("fetch_failed", "无法从 %s 检查正式版本：%v", packageSourceName(selected), err)
	}
	payload := packageReleasePayload{Releases: map[string][]struct {
		Yanked bool `json:"yanked"`
	}{}}
	for _, anchor := range simpleAnchorPattern.FindAllStringSubmatch(text, -1) {
		attributes := anchor[1]
		hrefMatch := simpleHrefPattern.FindStringSubmatch(attributes)
		if len(hrefMatch) != 2 {
			continue
		}
		rawPath := hrefMatch[1]
		if parsed, parseErr := url.Parse(rawPath); parseErr == nil {
			rawPath = parsed.Path
		}
		trimmed := html.UnescapeString(rawPath)
		if slash := strings.LastIndex(trimmed, "/"); slash >= 0 {
			trimmed = trimmed[slash+1:]
		}
		match := releaseFilePattern.FindStringSubmatch(trimmed)
		if len(match) != 2 {
			continue
		}
		release := match[1]
		payload.Releases[release] = append(payload.Releases[release], struct {
			Yanked bool `json:"yanked"`
		}{Yanked: strings.Contains(strings.ToLower(attributes), "data-yanked")})
	}
	if len(payload.Releases) == 0 {
		return packageReleasePayload{}, errorf("fetch_failed", "%s 没有可识别的正式版本", packageSourceName(selected))
	}
	return payload, nil
}

func releaseExists(payload packageReleasePayload, target string) bool {
	files := payload.Releases[target]
	for _, file := range files {
		if !file.Yanked {
			return true
		}
	}
	return false
}

func latestRelease(ctx context.Context, current, sourceID string) (string, error) {
	payload, err := fetchPackageReleases(ctx, sourceID)
	if err != nil {
		return "", err
	}
	candidates := make([]string, 0, len(payload.Releases))
	for release := range payload.Releases {
		if !version.IsValidSemVer(release) {
			continue
		}
		if releaseExists(payload, release) {
			candidates = append(candidates, release)
		}
	}
	if version.IsValidSemVer(current) {
		candidates = append(candidates, current)
	}
	if len(candidates) == 0 {
		return "", errorf("unknown_version", "%s 没有可用的正式版本", packageSourceName(sourceID))
	}
	sort.Slice(candidates, func(i, j int) bool {
		compared, compareErr := version.CompareSemVer(candidates[i], candidates[j])
		return compareErr == nil && compared > 0
	})
	return candidates[0], nil
}

var (
	projectVersionPattern  = regexp.MustCompile(`(?m)^\s*version\s*=\s*["']([^"']+)["']`)
	projectRequiresPattern = regexp.MustCompile(`(?m)^\s*requires-python\s*=\s*["']([^"']+)["']`)
	// goBaselinePattern 匹配 Go 侧 internal/common/version/version.go 的 Baseline 常量。
	// 该文件是 pyproject.toml 读不到时的兜底版本源，若不单独识别，
	// 兜底路径即便存在也解析不出任何版本号。
	goBaselinePattern = regexp.MustCompile(`(?m)^\s*Baseline\s*=\s*"([^"]+)"`)
)

// readProjectVersion 只读取 pyproject.toml 的 [project] 配置节
// 无任何配置节时兼容内部合成的单行版本文本
func readProjectVersion(text string) (mainVersion string, requiresPython string) {
	var document map[string]any
	if err := toml.Unmarshal([]byte(text), &document); err == nil {
		if project, ok := document["project"].(map[string]any); ok {
			return tomlString(project["version"]), tomlString(project["requires-python"])
		}
		if hasTOMLSection(text) {
			return "", ""
		}
	}
	if hasTOMLSection(text) {
		return "", ""
	}
	if match := projectVersionPattern.FindStringSubmatch(text); len(match) == 2 {
		mainVersion = strings.TrimSpace(match[1])
	}
	if match := projectRequiresPattern.FindStringSubmatch(text); len(match) == 2 {
		requiresPython = strings.TrimSpace(match[1])
	}
	if mainVersion == "" {
		// Go 源码兜底：Baseline = "x.y.z"
		if match := goBaselinePattern.FindStringSubmatch(text); len(match) == 2 {
			mainVersion = strings.TrimSpace(match[1])
		}
	}
	return mainVersion, requiresPython
}

func tomlString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func hasTOMLSection(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			return true
		}
	}
	return false
}

func remoteVersionText(ctx context.Context, client *http.Client, source updateSource, target string) (string, error) {
	if source.APIType == "github" {
		var contents struct {
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		api := fmt.Sprintf("https://api.github.com/repos/%s/contents/pyproject.toml?ref=%s", OfficialRepositoryPath, target)
		if err := getJSON(ctx, client, api, &contents); err != nil {
			return readVersionFile(ctx, client, source, target)
		}
		if contents.Encoding == "base64" {
			decoded, decodeErr := decodeBase64(contents.Content)
			if decodeErr == nil {
				return decoded, nil
			}
		}
		return readVersionFile(ctx, client, source, target)
	}
	return readVersionFile(ctx, client, source, target)
}

func readVersionFile(ctx context.Context, client *http.Client, source updateSource, target string) (string, error) {
	paths := []string{"pyproject.toml", "VERSION", "version.txt", "internal/common/version/version.go"}
	for _, path := range paths {
		var requestURL string
		if source.APIType == "github" {
			requestURL = fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", OfficialRepositoryPath, target, path)
		} else if source.APIType == "cnb" {
			requestURL = fmt.Sprintf("https://cnb.cool/Mai-with-u/maibot/-/git/raw/%s/%s", target, path)
		} else {
			continue
		}
		text, err := getText(ctx, client, requestURL)
		if err == nil {
			if strings.HasSuffix(path, ".go") {
				match := regexp.MustCompile(`Build\s*=\s*"([^"]+)"`).FindStringSubmatch(text)
				if len(match) == 2 {
					return "version = \"" + match[1] + "\"", nil
				}
				continue
			}
			return text, nil
		}
	}
	return "", errorf("fetch_failed", "来源 %s 没有可读取的版本文件", source.Name)
}

func decodeBase64(value string) (string, error) {
	cleaned := strings.NewReplacer("\n", "", "\r", "", " ", "").Replace(value)
	raw, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

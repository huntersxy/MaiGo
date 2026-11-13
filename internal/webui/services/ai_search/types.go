package search

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

const (
	MaxCandidates        = 2000
	MaxQueryLength       = 500
	MaxCandidateID       = 180
	MaxCandidateTitle    = 120
	MaxCandidateDesc     = 240
	MaxCandidateCat      = 80
	MaxCandidateDoc      = 2000
	MaxLanguageLength    = 16
	MaxToolRounds        = 4
	MaxToolCallsPerRound = 4
)

// ValidationError 表示请求不符合 Python Pydantic 模型的约束
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// OutputError 表示模型输出无法解析为结构化搜索结果
type OutputError struct{ Message string }

func (e *OutputError) Error() string { return e.Message }

// UnavailableError 表示缺少运行 AI 搜索所需的模型或索引
type UnavailableError struct{ Message string }

func (e *UnavailableError) Error() string { return e.Message }

// Candidate 由 WebUI 真实搜索索引提供的候选项
type Candidate struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Document    string `json:"document"`
}

// Request AI 搜索请求
type Request struct {
	Query      string      `json:"query"`
	Language   string      `json:"language"`
	Candidates []Candidate `json:"candidates"`
}

// ModelResult 模型选择的单个候选
type ModelResult struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// Source Agent 阅读并引用的官方文档
type Source struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// ModelOutput 模型必须返回的结构化搜索结果
type ModelOutput struct {
	Answer        string        `json:"answer"`
	Suggestions   []string      `json:"suggestions"`
	SourceIDs     []string      `json:"source_ids"`
	ExpandedTerms []string      `json:"expanded_terms"`
	Results       []ModelResult `json:"results"`
}

// Response 经过候选 ID 校验后的 AI 搜索响应
type Response struct {
	Cached          bool          `json:"cached"`
	ModelName       string        `json:"model_name"`
	Answer          string        `json:"answer"`
	Suggestions     []string      `json:"suggestions"`
	Sources         []Source      `json:"sources"`
	ExpandedTerms   []string      `json:"expanded_terms"`
	Results         []ModelResult `json:"results"`
	TotalTokens     int           `json:"total_tokens"`
	GroundingError  string        `json:"grounding_error"`
	UsedLocalConfig bool          `json:"used_local_config"`
}

// ProgressEvent AI 搜索流式返回的可核验过程事件
type ProgressEvent struct {
	Type    string   `json:"type"`
	Stage   string   `json:"stage"`
	Status  any      `json:"status"`
	Round   *int     `json:"round"`
	Tool    string   `json:"tool"`
	Query   string   `json:"query"`
	Targets []string `json:"targets"`
	Titles  []string `json:"titles"`
	Count   *int     `json:"count"`
	Error   string   `json:"error"`
}

// NewProgressEvent 构造默认进度事件，空字段与 Python model_dump 对齐
func NewProgressEvent(stage string) ProgressEvent {
	return ProgressEvent{Type: "progress", Stage: stage, Targets: []string{}, Titles: []string{}}
}

// Emit 进度回调
type Emit func(ProgressEvent)

// Runner 执行一次 AI 搜索，供 WebUI 注入真实模型或测试替身
type Runner interface {
	Run(ctx context.Context, request Request, emit Emit) (Response, error)
}

// NormalizeLanguage 与 Python resolve_prompt_locale 对齐
func NormalizeLanguage(language string) string {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(language), "_", "-"))
	if strings.HasPrefix(normalized, "en") {
		return "en-US"
	}
	if strings.HasPrefix(normalized, "ja") {
		return "ja-JP"
	}
	return "zh-CN"
}

// ValidateRequest 校验查询与候选目录
func ValidateRequest(request Request) error {
	query := strings.TrimSpace(request.Query)
	if query == "" {
		return &ValidationError{Message: "query 不能为空"}
	}
	if len([]rune(request.Query)) > MaxQueryLength {
		return &ValidationError{Message: fmt.Sprintf("query 长度不能超过 %d", MaxQueryLength)}
	}
	if len([]rune(request.Language)) > MaxLanguageLength {
		return &ValidationError{Message: fmt.Sprintf("language 长度不能超过 %d", MaxLanguageLength)}
	}
	if len(request.Candidates) == 0 {
		return &ValidationError{Message: "candidates 不能为空"}
	}
	if len(request.Candidates) > MaxCandidates {
		return &ValidationError{Message: fmt.Sprintf("candidates 最多 %d 个", MaxCandidates)}
	}
	seen := make(map[string]struct{}, len(request.Candidates))
	for index, candidate := range request.Candidates {
		if strings.TrimSpace(candidate.ID) == "" || len([]rune(candidate.ID)) > MaxCandidateID {
			return &ValidationError{Message: fmt.Sprintf("candidates[%d].id 长度必须在 1 到 %d 之间", index, MaxCandidateID)}
		}
		if strings.TrimSpace(candidate.Title) == "" || len([]rune(candidate.Title)) > MaxCandidateTitle {
			return &ValidationError{Message: fmt.Sprintf("candidates[%d].title 长度必须在 1 到 %d 之间", index, MaxCandidateTitle)}
		}
		if len([]rune(candidate.Description)) > MaxCandidateDesc {
			return &ValidationError{Message: fmt.Sprintf("candidates[%d].description 长度不能超过 %d", index, MaxCandidateDesc)}
		}
		if len([]rune(candidate.Category)) > MaxCandidateCat {
			return &ValidationError{Message: fmt.Sprintf("candidates[%d].category 长度不能超过 %d", index, MaxCandidateCat)}
		}
		if len([]rune(candidate.Document)) > MaxCandidateDoc {
			return &ValidationError{Message: fmt.Sprintf("candidates[%d].document 长度不能超过 %d", index, MaxCandidateDoc)}
		}
		if _, exists := seen[candidate.ID]; exists {
			return &ValidationError{Message: "AI 搜索候选项包含重复 ID"}
		}
		seen[candidate.ID] = struct{}{}
	}
	return nil
}

// NormalizeOutput 仅保留真实候选 ID，去空白、去重并保持顺序
func NormalizeOutput(output ModelOutput, candidates []Candidate) ModelOutput {
	valid := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		valid[candidate.ID] = struct{}{}
	}
	results := make([]ModelResult, 0, len(output.Results))
	seenResults := make(map[string]struct{}, len(output.Results))
	for _, result := range output.Results {
		if _, ok := valid[result.ID]; !ok {
			continue
		}
		if _, exists := seenResults[result.ID]; exists {
			continue
		}
		seenResults[result.ID] = struct{}{}
		results = append(results, result)
	}
	return ModelOutput{
		Answer:        strings.TrimSpace(output.Answer),
		Suggestions:   dedupeStrings(output.Suggestions),
		SourceIDs:     dedupeStrings(output.SourceIDs),
		ExpandedTerms: dedupeStrings(output.ExpandedTerms),
		Results:       results,
	}
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

// ErrRunnerMissing 供处理器判断是否应该返回 503
var ErrRunnerMissing = errors.New("AI 搜索服务未接线")

// MessageText 提取 eino 消息文本
func MessageText(message *schema.AgenticMessage) string {
	if message == nil {
		return ""
	}
	var builder strings.Builder
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		switch {
		case block.AssistantGenText != nil:
			builder.WriteString(block.AssistantGenText.Text)
		case block.UserInputText != nil:
			builder.WriteString(block.UserInputText.Text)
		}
	}
	return builder.String()
}

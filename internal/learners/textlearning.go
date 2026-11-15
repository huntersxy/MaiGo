package learners

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/text/cases"

	"MaiGo/internal/llm_models"
	"MaiGo/internal/prompt"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// 长文学习常量，对齐 services/text_learning.py
const (
	MaxLearningSourceLength = 200_000
	LearningChunkLength     = 4_500
	maxLearningCandidates   = 30
)

// LearningKind 学习类型：表达或黑话
type LearningKind string

const (
	LearningKindExpression LearningKind = "expression"
	LearningKindJargon     LearningKind = "jargon"
)

// LearnedCandidate 学习候选，标题对应情境/黑话，描述对应风格/含义
type LearnedCandidate struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Evidence    string `json:"evidence"`
}

const learningUnicodeSpace = `\s\p{Zs}\x{0085}\x{001C}-\x{001F}`

var (
	learningBoundaryPatterns = []*regexp.Regexp{
		regexp.MustCompile(`\n[\s\p{Zs}\x{0085}\x{001C}-\x{001F}]*\n`),
		regexp.MustCompile(`\n`),
		regexp.MustCompile(`[。！？]|[.!?](?:[` + learningUnicodeSpace + `”’"']|$)`),
	}
	learningSourceBoundaryPattern = regexp.MustCompile(`[。！？；;.!?][` + learningUnicodeSpace + `]*|[` + learningUnicodeSpace + `]+`)
)

// SplitLearningText 对齐 split_learning_text：优先段落、换行、句末切分，超长硬切
func SplitLearningText(text string) ([]string, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("请先输入要学习的文段")
	}
	runes := []rune(text)
	if len(runes) > MaxLearningSourceLength {
		return nil, errors.New("文段不能超过 200,000 字")
	}
	chunks := make([]string, 0, len(runes)/LearningChunkLength+1)
	for len(runes) > LearningChunkLength {
		window := string(runes[:LearningChunkLength])
		cut := LearningChunkLength
		for _, pattern := range learningBoundaryPatterns {
			lastEnd := -1
			for _, loc := range pattern.FindAllStringIndex(window, -1) {
				runeEnd := utf8.RuneCountInString(window[:loc[1]])
				if runeEnd >= LearningChunkLength/2 {
					lastEnd = runeEnd
				}
			}
			if lastEnd > 0 {
				cut = lastEnd
				break
			}
		}
		chunks = append(chunks, string(runes[:cut]))
		runes = runes[cut:]
	}
	if len(runes) > 0 {
		chunks = append(chunks, string(runes))
	}
	return chunks, nil
}

// BuildLearningSources 为原文建立稳定编号，长段按句末或空白切到 400 字以内
func BuildLearningSources(text string) map[int]string {
	sources := map[int]string{}
	nextID := 1
	for _, line := range splitPythonLines(text) {
		remaining := []rune(strings.TrimSpace(line))
		for len(remaining) > 0 {
			cut := 400
			if len(remaining) < cut {
				cut = len(remaining)
			}
			if len(remaining) > 400 {
				window := string(remaining[:400])
				lastEnd := -1
				for _, loc := range learningSourceBoundaryPattern.FindAllStringIndex(window, -1) {
					runeEnd := utf8.RuneCountInString(window[:loc[1]])
					if runeEnd >= 200 {
						lastEnd = runeEnd
					}
				}
				if lastEnd > 0 {
					cut = lastEnd
				}
			}
			excerpt := strings.TrimSpace(string(remaining[:cut]))
			if excerpt != "" {
				sources[nextID] = excerpt
				nextID++
			}
			remaining = []rune(strings.TrimSpace(string(remaining[cut:])))
		}
	}
	return sources
}

// splitPythonLines 对齐 Python str.splitlines 的换行集合
func splitPythonLines(text string) []string {
	lines := make([]string, 0, strings.Count(text, "\n")+1)
	start := 0
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == '\r' {
			lines = append(lines, text[start:i])
			i += size
			if i < len(text) && text[i] == '\n' {
				i++
			}
			start = i
			continue
		}
		if r == '\n' || r == '\v' || r == '\f' || r == '\x1c' || r == '\x1d' || r == '\x1e' || r == '\x85' || r == '\u2028' || r == '\u2029' {
			lines = append(lines, text[start:i])
			i += size
			start = i
			continue
		}
		i += size
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
}

type extractedCandidate struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	SourceID    *int   `json:"source_id"`
}

type candidateExtraction struct {
	Candidates *[]extractedCandidate `json:"candidates"`
}

// ParseLearningCandidates 校验模型 JSON，并按原文编号补全依据
func ParseLearningCandidates(response, source string, kind LearningKind) ([]LearnedCandidate, error) {
	payload := strings.TrimSpace(response)
	if payload == "" {
		return nil, errors.New("学习模型未返回正文，请检查学习模型的输出 token 上限后重试当前段")
	}
	if strings.HasPrefix(payload, "```") {
		lines := strings.Split(payload, "\n")
		if strings.TrimSpace(lines[len(lines)-1]) != "```" {
			return nil, errors.New("学习模型返回了不完整的 JSON 代码块")
		}
		if len(lines) < 2 {
			payload = ""
		} else {
			payload = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(payload)))
	decoder.DisallowUnknownFields()
	var extraction candidateExtraction
	if err := decoder.Decode(&extraction); err != nil {
		return nil, learningCandidateDecodeError(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("学习模型返回的 JSON 格式错误或未完整输出，请检查输出 token 上限后重试当前段")
	}
	if extraction.Candidates == nil {
		return nil, errors.New("学习模型返回的候选字段不符合要求，请重试当前段；详细原因见后台日志")
	}
	candidates := *extraction.Candidates
	for i := range candidates {
		candidates[i].Title = strings.TrimSpace(candidates[i].Title)
		candidates[i].Description = strings.TrimSpace(candidates[i].Description)
	}
	if err := validateExtractedCandidates(candidates); err != nil {
		return nil, errors.New("学习模型返回的候选字段不符合要求，请重试当前段；详细原因见后台日志")
	}
	if len(candidates) > maxLearningCandidates {
		return nil, errors.New("学习模型单段返回超过 30 条候选，请重试该段")
	}
	sources := BuildLearningSources(source)
	unique := make([]LearnedCandidate, 0, len(candidates))
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		evidence := ""
		if candidate.SourceID != nil {
			if text, ok := sources[*candidate.SourceID]; ok {
				evidence = text
			} else {
				zap.L().Warn("学习候选引用不存在的原文编号，保留为无依据候选",
					zap.String("kind", string(kind)), zap.Int("source_id", *candidate.SourceID))
			}
		}
		title := candidate.Title
		description := candidate.Description
		key := title + "\x00" + description
		if kind == LearningKindJargon {
			key = cases.Fold().String(title)
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, LearnedCandidate{Title: title, Description: description, Evidence: evidence})
	}
	return unique, nil
}

// learningCandidateDecodeError 区分 JSON 语法错误与字段类型/未知字段错误
func learningCandidateDecodeError(err error) error {
	var syntaxError *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &syntaxError) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return errors.New("学习模型返回的 JSON 格式错误或未完整输出，请检查输出 token 上限后重试当前段")
	}
	if errors.As(err, &typeError) || strings.Contains(err.Error(), "unknown field") {
		return errors.New("学习模型返回的候选字段不符合要求，请重试当前段；详细原因见后台日志")
	}
	return errors.New("学习模型返回的候选字段不符合要求，请重试当前段；详细原因见后台日志")
}

func validateExtractedCandidates(candidates []extractedCandidate) error {
	for _, candidate := range candidates {
		titleRunes := len([]rune(candidate.Title))
		descRunes := len([]rune(candidate.Description))
		if titleRunes < 1 || titleRunes > 200 {
			return fmt.Errorf("候选标题长度非法")
		}
		if descRunes < 1 || descRunes > 1000 {
			return fmt.Errorf("候选描述长度非法")
		}
		if strings.TrimSpace(candidate.Title) == "" || strings.TrimSpace(candidate.Description) == "" {
			return fmt.Errorf("候选字段不能为空")
		}
		if candidate.SourceID != nil && *candidate.SourceID < 1 {
			return fmt.Errorf("候选原文编号非法")
		}
	}
	return nil
}

var (
	textLearningPromptOnce sync.Once
	textLearningPrompts    *prompt.Manager
	textLearningPromptErr  error
)

func defaultTextLearningPrompts() *prompt.Manager {
	textLearningPromptOnce.Do(func() {
		textLearningPrompts, textLearningPromptErr = prompt.NewManager()
		if textLearningPromptErr != nil {
			zap.L().Debug("长文学习 prompt 管理器初始化失败", zap.Error(textLearningPromptErr))
		}
	})
	return textLearningPrompts
}

// ExtractTextCandidates 使用 learner 任务从一段原文中提取候选，不写库
func ExtractTextCandidates(ctx context.Context, router *llm.Router, prompts *prompt.Manager, text string, kind LearningKind) ([]LearnedCandidate, error) {
	if router == nil {
		return nil, errors.New("LLM 路由器未接入")
	}
	if kind != LearningKindExpression && kind != LearningKindJargon {
		return nil, errors.New("学习类型仅支持 expression 或 jargon")
	}
	if prompts == nil {
		prompts = defaultTextLearningPrompts()
	}
	if prompts == nil {
		if textLearningPromptErr != nil {
			return nil, textLearningPromptErr
		}
		return nil, errors.New("prompt 管理器不可用")
	}
	instructions, err := prompts.Render("text_learning_"+string(kind), nil)
	if err != nil {
		return nil, err
	}
	type sourceItem struct {
		ID   int    `json:"id"`
		Text string `json:"text"`
	}
	sources := BuildLearningSources(text)
	items := make([]sourceItem, 0, len(sources))
	ids := make([]int, 0, len(sources))
	for id := range sources {
		ids = append(ids, id)
	}
	sortInts(ids)
	for _, id := range ids {
		items = append(items, sourceItem{ID: id, Text: sources[id]})
	}
	numbered, err := marshalLearningSources(items)
	if err != nil {
		return nil, err
	}
	_, client, err := router.NewClientForTask(ctx, llm.TaskLearner)
	if err != nil {
		return nil, err
	}
	msg, err := client.Generate(llm.WithResponseFormat(ctx, "json_object"), []*schema.AgenticMessage{
		schema.SystemAgenticMessage(instructions),
		schema.UserAgenticMessage(numbered),
	})
	if err != nil {
		return nil, err
	}
	plain := agenticPlainTextFromMessage(msg)
	if strings.TrimSpace(plain) == "" && strings.TrimSpace(agenticReasoningFromMessage(msg)) != "" {
		return nil, errors.New("学习模型只返回了思考内容，没有生成候选结果，请检查输出 token 上限后重试当前段")
	}
	return ParseLearningCandidates(plain, text, kind)
}

// marshalLearningSources 生成给模型的原文 JSON，不转义 HTML 字符
func marshalLearningSources(items any) (string, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(items); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

func agenticPlainTextFromMessage(msg *schema.AgenticMessage) string {
	if msg == nil {
		return ""
	}
	var b strings.Builder
	for _, block := range msg.ContentBlocks {
		if block == nil {
			continue
		}
		switch block.Type {
		case schema.ContentBlockTypeUserInputText:
			if block.UserInputText != nil {
				b.WriteString(block.UserInputText.Text)
			}
		case schema.ContentBlockTypeAssistantGenText:
			if block.AssistantGenText != nil {
				b.WriteString(block.AssistantGenText.Text)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func agenticReasoningFromMessage(msg *schema.AgenticMessage) string {
	if msg == nil {
		return ""
	}
	var b strings.Builder
	for _, block := range msg.ContentBlocks {
		if block != nil && block.Reasoning != nil {
			b.WriteString(block.Reasoning.Text)
		}
	}
	return b.String()
}

func sortInts(values []int) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

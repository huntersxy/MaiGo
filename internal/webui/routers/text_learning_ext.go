package webuiapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/bytedance/sonic"

	"MaiGo/internal/learners"
	"MaiGo/internal/llm_models"
	"MaiGo/internal/prompt"
)

// textLearningExtractFunc 便于 WebUI 测试注入学习服务，生产默认走 learners 实现
type textLearningExtractFunc func(context.Context, *llm.Router, *prompt.Manager, string, learners.LearningKind) ([]learners.LearnedCandidate, error)

// TextLearningRequest 长文学习请求，chunk 长度上限与 Python CHUNK_LENGTH 一致
type TextLearningRequest struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

func (d Deps) handleTextLearningPrepare(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&body); err != nil {
		writeValidationError(w, "请求格式错误")
		return
	}
	if len([]rune(body.Text)) < 1 {
		writeValidationError(w, "text 长度必须大于等于 1")
		return
	}
	if len([]rune(body.Text)) > learners.MaxLearningSourceLength {
		writeValidationError(w, "text 不能超过 200000 字")
		return
	}
	chunks, err := learners.SplitLearningText(body.Text)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chunks": chunks})
}

func (d Deps) handleTextLearningExtract(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	var body TextLearningRequest
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&body); err != nil {
		writeValidationError(w, "请求格式错误")
		return
	}
	kind := learners.LearningKind(body.Kind)
	if kind != learners.LearningKindExpression && kind != learners.LearningKindJargon {
		writeValidationError(w, "kind 仅支持 expression 或 jargon")
		return
	}
	textLength := len([]rune(body.Text))
	if textLength < 1 || textLength > learners.LearningChunkLength {
		writeValidationError(w, "text 长度必须在 1 到 4500 之间")
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		writeAPIError(w, http.StatusBadRequest, "学习文段不能为空")
		return
	}
	extract := d.textLearningExtract
	if extract == nil {
		extract = learners.ExtractTextCandidates
	}
	candidates, err := extract(r.Context(), d.agentRouter(), nil, body.Text, kind)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "学习失败："+err.Error())
		return
	}
	if candidates == nil {
		candidates = []learners.LearnedCandidate{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": candidates})
}

// agentRouter 返回 Agent 注入的任务路由器；未接入时返回 nil
func (d Deps) agentRouter() *llm.Router {
	if d.Agent == nil {
		return nil
	}
	return d.Agent.LLMRouter()
}

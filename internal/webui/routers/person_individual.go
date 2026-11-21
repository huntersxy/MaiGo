package webuiapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/go-chi/chi/v5"

	"MaiGo/internal/person_info"
)

func (d Deps) handleIndividualDetail(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	record, err := personinfo.DefaultRegistry().GetIndividual(r.Context(), chi.URLParam(r, "individual_id"))
	if err != nil {
		writeIndividualError(w, err, "获取个体详情失败", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, individualRecordToWebUI(record))
}

func (d Deps) handleIndividualDirectory(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	page, pageSize, ok := queryPagination(w, r, 20)
	if !ok {
		return
	}
	isKnown, ok := optionalQueryBoolPtr(w, r, "is_known")
	if !ok {
		return
	}
	linkedOnly, ok := optionalQueryBoolPtr(w, r, "linked_only")
	if !ok {
		return
	}
	result, err := personinfo.DefaultRegistry().ListIndividualDirectory(r.Context(), personinfo.IndividualDirectoryFilter{
		Page: page, PageSize: pageSize, View: r.URL.Query().Get("view"),
		Search: r.URL.Query().Get("search"), Platform: r.URL.Query().Get("platform"),
		IsKnown: isKnown, LinkedOnly: linkedOnly != nil && *linkedOnly,
	})
	if err != nil {
		writeIndividualError(w, err, "获取人物目录失败", http.StatusBadRequest)
		return
	}
	data := make([]map[string]any, 0, len(result.Data))
	for _, item := range result.Data {
		members := make([]map[string]any, 0, len(item.Members))
		for i := range item.Members {
			members = append(members, personInfoToWebUI(&item.Members[i]))
		}
		data = append(data, map[string]any{
			"individual_id": item.IndividualID, "display_name": item.DisplayName,
			"is_known": item.IsKnown, "profile_sources": item.ProfileSources,
			"member_count": item.MemberCount, "members": members,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": data, "total": result.Total, "page": result.Page, "page_size": result.PageSize,
	})
}

func (d Deps) handleIndividualMergePreview(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	var request personinfo.IndividualMergeRequest
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&request); err != nil {
		writeValidationError(w, "请求格式错误")
		return
	}
	preview, err := personinfo.DefaultRegistry().PreviewIndividualMerge(r.Context(), request)
	if err != nil {
		writeIndividualError(w, err, "生成合并预览失败", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (d Deps) handleIndividualMerge(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	var request struct {
		TargetIndividualID string            `json:"target_individual_id"`
		ExpectedMembers    map[string]string `json:"expected_members"`
		ExpectedRevision   string            `json:"expected_revision"`
		Profile            struct {
			DisplayName string `json:"display_name"`
			IsKnown     *bool  `json:"is_known"`
		} `json:"profile"`
		ResolvedConflicts []string `json:"resolved_conflicts"`
	}
	if err := sonic.ConfigDefault.NewDecoder(r.Body).Decode(&request); err != nil {
		writeValidationError(w, "请求格式错误")
		return
	}
	if len(request.ExpectedMembers) < 2 || len(request.ExpectedMembers) > 1000 ||
		strings.TrimSpace(request.ExpectedRevision) == "" || request.Profile.IsKnown == nil {
		writeValidationError(w, "合并请求缺少成员快照、版本或最终资料")
		return
	}
	merged, err := personinfo.DefaultRegistry().MergeIndividuals(r.Context(), personinfo.IndividualMergeInput{
		TargetIndividualID: request.TargetIndividualID,
		ExpectedMembers:    request.ExpectedMembers,
		ExpectedRevision:   request.ExpectedRevision,
		Profile: personinfo.IndividualProfile{
			DisplayName: request.Profile.DisplayName,
			IsKnown:     *request.Profile.IsKnown,
		},
		ResolvedConflicts: request.ResolvedConflicts,
	})
	if err != nil {
		writeIndividualError(w, err, "合并个体失败", http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, individualRecordToWebUI(merged))
}

func individualRecordToWebUI(record *personinfo.IndividualRecord) map[string]any {
	members := make([]map[string]any, 0, len(record.Members))
	for i := range record.Members {
		members = append(members, personInfoToWebUI(&record.Members[i]))
	}
	return map[string]any{
		"individual_id":   record.Individual.IndividualID,
		"display_name":    record.Individual.DisplayName,
		"is_known":        record.Individual.IsKnown,
		"profile_sources": record.ProfileSources,
		"members":         members,
	}
}

func writeIndividualError(w http.ResponseWriter, err error, message string, validationStatus int) {
	switch {
	case errors.Is(err, personinfo.ErrIndividualNotFound), errors.Is(err, personinfo.ErrPersonNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": err.Error()})
	case errors.Is(err, personinfo.ErrMemoryUnavailable):
		writeAPIError(w, http.StatusServiceUnavailable, "记忆管理器未初始化")
	case errors.Is(err, personinfo.ErrIndividualChanged):
		writeJSON(w, http.StatusConflict, map[string]any{"detail": err.Error()})
	default:
		var inputErr *personinfo.IndividualInputError
		if errors.As(err, &inputErr) {
			writeJSON(w, validationStatus, map[string]any{"detail": err.Error()})
			return
		}
		writeAPIError(w, http.StatusInternalServerError, message+": "+err.Error())
	}
}

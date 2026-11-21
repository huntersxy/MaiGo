package webuiapi

import (
	"net/http"

	"MaiGo/internal/services/schedules"
)

func (d Deps) handleSchedules(w http.ResponseWriter, r *http.Request) {
	includeCompleted, ok := optionalQueryBoolPtr(w, r, "include_completed")
	if !ok {
		return
	}
	items, err := schedules.ReadScheduleItems(schedules.Directory(d.desktopPetRoot()), "", includeCompleted != nil && *includeCompleted)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

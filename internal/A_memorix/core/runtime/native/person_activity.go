package native

// 人物活跃时间跟踪，对齐 Python _mark_person_active 的内存时间戳集合

import (
	"strings"
	"time"
)

// markPersonActiveSeen 记录人物最近活跃时间并落库画像活跃标记
func (r *Runtime) markPersonActiveSeen(personID string) {
	personID = strings.TrimSpace(personID)
	if personID == "" {
		return
	}
	r.activeMu.Lock()
	if r.activePersons == nil {
		r.activePersons = map[string]float64{}
	}
	r.activePersons[personID] = float64(time.Now().UnixNano()) / 1e9
	r.activeMu.Unlock()
	if r.metadataStore == nil {
		return
	}
	if err := r.metadataStore.MarkPersonProfileActive("", "", personID, nil); err != nil {
		r.logger.Warning("[native] 记录人物活跃失败: " + err.Error())
	}
}

// activePersonTimestamps 返回人物活跃时间快照
func (r *Runtime) activePersonTimestamps() map[string]float64 {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	out := make(map[string]float64, len(r.activePersons))
	for personID, seenAt := range r.activePersons {
		out[personID] = seenAt
	}
	return out
}

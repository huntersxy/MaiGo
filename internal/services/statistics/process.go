package stats

import (
	"sync"
	"time"
)

var (
	processMu     sync.RWMutex
	processStart  time.Time
	processMarked bool
)

// MarkProcessStarted 记录 Worker 进程启动时刻；应在 runWorker 入口尽早调用一次
func MarkProcessStarted() {
	processMu.Lock()
	defer processMu.Unlock()
	if processMarked {
		return
	}
	processStart = time.Now()
	processMarked = true
}

// ProcessStart 返回进程启动时刻；未 Mark 时为零值
func ProcessStart() time.Time {
	processMu.RLock()
	defer processMu.RUnlock()
	return processStart
}

// ProcessStatus 供 WebUI system/status 等使用的运行快照
type ProcessStatus struct {
	Running   bool      `json:"running"`
	UptimeSec float64   `json:"uptime"`
	Version   string    `json:"version"`
	StartTime time.Time `json:"-"`
	StartRFC  string    `json:"start_time"`
	Backend   string    `json:"backend"`
}

// ProcessSnapshot 构建当前进程状态；version 为空时用 "dev"
func ProcessSnapshot(version string) ProcessStatus {
	processMu.RLock()
	start := processStart
	marked := processMarked
	processMu.RUnlock()

	if version == "" {
		version = "dev"
	}
	st := ProcessStatus{
		Running: marked,
		Version: version,
		Backend: "maibot-go",
	}
	if !marked {
		return st
	}
	st.StartTime = start.UTC()
	st.StartRFC = start.UTC().Format(time.RFC3339)
	st.UptimeSec = time.Since(start).Seconds()
	return st
}

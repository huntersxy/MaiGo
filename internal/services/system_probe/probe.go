package system_probe

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"runtime"
	"time"
)

// providerCallTimeout 是外部 provider 与 runtime 采集的独立上限。
const providerCallTimeout = 500 * time.Millisecond

// Options 是探针的采集边界与外部只读 provider。
type Options struct {
	InstanceDir  string
	DatabasePath string
	// Database 返回底层 sql.DB；未接线时为 nil。
	Database func() *sql.DB
	// DSNOverride 表示 MAIBOT_DATABASE_DSN 显式覆盖了默认库路径。
	DSNOverride  bool
	Version      string
	StartedAt    time.Time
	Counts       func() Counts
	Scheduler    func() SchedulerStats
	Subprocesses func() []Subprocess
	Timeout      time.Duration
	ProcessLimit int
	ScanLimit    int
	HistoryLimit int
	Now          func() time.Time
	StatfsFn     func(path string) (StatfsResult, error)
	Reader       Reader

	// collector 是包内测试 seam；生产为 nil，由平台实现填充。
	collector collector
}

// platformData 是平台采集器的原始输出。
type platformData struct {
	CPU             *CPUSection
	Memory          *MemorySection
	Disk            *DiskSection
	Network         *NetworkSection
	Processes       *ProcessSection
	Cgroup          *CgroupSection
	PSI             *PSISection
	Errors          []ProbeError
	CPUTicks        *cpuSample
	BTime           *float64
	MemoryAvailable *uint64
	ProcessRSS      *uint64
}

type collector interface {
	collect(ctx context.Context, probe *Probe, subprocesses []Subprocess) platformData
}

// Probe 是无生命周期资源的只读探针；并发 Snapshot 安全。
type Probe struct {
	opts      Options
	state     *probeState
	limiter   *statfsLimiter
	collector collector
}

// New 构造探针并补齐边界默认值。
func New(opts Options) *Probe {
	if opts.Timeout <= 0 {
		opts.Timeout = 2 * time.Second
	}
	if opts.Timeout > 5*time.Second {
		opts.Timeout = 5 * time.Second
	}
	if opts.ProcessLimit <= 0 {
		opts.ProcessLimit = defaultProcessShow
	}
	if opts.ProcessLimit > maxProcessShow {
		opts.ProcessLimit = maxProcessShow
	}
	if opts.ScanLimit <= 0 {
		opts.ScanLimit = defaultScanLimit
	}
	if opts.ScanLimit > defaultScanLimit {
		opts.ScanLimit = defaultScanLimit
	}
	if opts.HistoryLimit <= 0 {
		opts.HistoryLimit = 60
	}
	if opts.HistoryLimit > 60 {
		opts.HistoryLimit = 60
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Reader == nil {
		opts.Reader = defaultReader()
	}
	probe := &Probe{
		opts:    opts,
		state:   newProbeState(opts.HistoryLimit),
		limiter: newStatfsLimiter(2),
	}
	if opts.collector != nil {
		probe.collector = opts.collector
	} else {
		probe.collector = newPlatformCollector()
	}
	return probe
}

// Snapshot 采集一次完整快照；超时与单 section 失败只写 errors[]。
func (p *Probe) Snapshot(ctx context.Context) *Snapshot {
	if p == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := p.opts.Now()
	startedWall := time.Now()
	collectCtx, cancel := context.WithTimeout(ctx, p.opts.Timeout)
	defer cancel()

	var data platformData
	subprocesses := p.subprocessesSnapshot(ctx, &data.Errors)
	collected := p.collect(collectCtx, subprocesses)
	collected.Errors = append(data.Errors, collected.Errors...)
	data = collected
	runtimeCtx, runtimeCancel := context.WithTimeout(ctx, providerCallTimeout)
	runtimeSection := safeValueCtx(runtimeCtx, "runtime", &data.Errors, func() *RuntimeSection {
		return collectRuntimeSection(p.state)
	})
	runtimeCancel()
	if runtimeSection == nil {
		runtimeSection = &RuntimeSection{}
	}

	snapshot := &Snapshot{
		Meta: Meta{
			Schema:              1,
			GeneratedAt:         now.UTC().Format(time.RFC3339),
			TimeoutMS:           p.opts.Timeout.Milliseconds(),
			Hostname:            hostname(),
			OS:                  runtime.GOOS,
			Arch:                runtime.GOARCH,
			GoVersion:           runtime.Version(),
			PID:                 os.Getpid(),
			InstanceDir:         p.opts.InstanceDir,
			Version:             p.opts.Version,
			ClockTicksPerSecond: clockTicksPerSec,
			BootTimeUnix:        data.BTime,
		},
		CPU:       data.CPU,
		Memory:    data.Memory,
		Disk:      data.Disk,
		Network:   data.Network,
		Processes: data.Processes,
		Cgroup:    data.Cgroup,
		PSI:       data.PSI,
	}
	if !p.opts.StartedAt.IsZero() {
		startedAt := p.opts.StartedAt.UTC().Format(time.RFC3339)
		uptime := now.Sub(p.opts.StartedAt).Seconds()
		if uptime < 0 {
			uptime = 0
		}
		snapshot.Meta.StartedAt = &startedAt
		snapshot.Meta.UptimeSec = &uptime
	}

	if snapshot.CPU != nil {
		if snapshot.CPU.LogicalCores <= 0 {
			snapshot.CPU.LogicalCores = runtime.NumCPU()
		}
		snapshot.CPU.GOMAXPROCS = runtime.GOMAXPROCS(0)
		snapshot.CPU.ClockTicksPerSecond = clockTicksPerSec
		if data.CPUTicks != nil {
			if usage := p.state.observeCPU(*data.CPUTicks, now); usage != nil {
				snapshot.CPU.Usage = *usage
			}
		}
	}
	if snapshot.Memory != nil {
		snapshot.Memory.Usage = calculateMemoryUsage(snapshot.Memory.System)
	}
	linkCgroupSections(snapshot)
	snapshot.Runtime = runtimeSection
	snapshot.App = p.appSection(ctx, data.Processes, subprocesses, &data.Errors)

	lastUsage := p.state.lastCPUUsageSnapshot()
	historySample := HistorySample{
		TimestampUnix:   now.Unix(),
		Goroutines:      snapshot.Runtime.Goroutines,
		CPUTotalPercent: lastUsage.TotalPercent,
	}
	if snapshot.CPU != nil && snapshot.CPU.Usage.TotalPercent != nil {
		historySample.CPUTotalPercent = snapshot.CPU.Usage.TotalPercent
	}
	historySample.MemoryAvailableBytes = data.MemoryAvailable
	historySample.ProcessRSSBytes = data.ProcessRSS
	if p.opts.Database != nil && snapshot.App != nil {
		inUse := snapshot.App.DB.Pool.InUse
		historySample.DBInUse = &inUse
	}
	p.state.appendHistory(historySample)
	snapshot.History = p.state.snapshotHistory()

	errors := data.Errors
	if len(errors) > maxProbeErrors {
		errors = errors[:maxProbeErrors]
		snapshot.Meta.ErrorsTruncated = true
	}
	if errors == nil {
		errors = []ProbeError{}
	}
	snapshot.Errors = errors
	snapshot.Meta.Partial = len(errors) > 0 || snapshot.CPU == nil || snapshot.Memory == nil
	snapshot.Meta.WindowSec = copyFloat(snapshot.CPUWindow())
	snapshot.Meta.ElapsedMS = time.Since(startedWall).Milliseconds()
	if snapshot.Meta.ElapsedMS < 0 {
		snapshot.Meta.ElapsedMS = 0
	}
	sanitizeSnapshot(snapshot)
	return snapshot
}

// CPUWindow 返回本次 CPU 差分窗口。
func (s *Snapshot) CPUWindow() *float64 {
	if s == nil || s.CPU == nil {
		return nil
	}
	return s.CPU.Usage.WindowSec
}

func (p *Probe) collect(ctx context.Context, subprocesses []Subprocess) (data platformData) {
	defer func() {
		if recovered := recover(); recovered != nil {
			data.Errors = append(data.Errors, ProbeError{
				Section: "collector",
				Error:   fmt.Sprintf("采集 panic: %v", recovered),
			})
		}
	}()
	if p.collector == nil {
		return data
	}
	return p.collector.collect(ctx, p, subprocesses)
}

func (p *Probe) subprocessesSnapshot(ctx context.Context, errs *[]ProbeError) []Subprocess {
	if p.opts.Subprocesses == nil {
		return nil
	}
	callCtx, cancel := context.WithTimeout(ctx, providerCallTimeout)
	defer cancel()
	return safeValueCtx(callCtx, "app", errs, p.opts.Subprocesses)
}

func (p *Probe) appSection(ctx context.Context, processes *ProcessSection, subprocesses []Subprocess, errs *[]ProbeError) *AppSection {
	appCtx, cancel := context.WithTimeout(ctx, providerCallTimeout)
	defer cancel()
	now := p.opts.Now()
	app := &AppSection{
		Version:      p.opts.Version,
		DB:           AppDB{Path: p.opts.DatabasePath, DSNOverride: p.opts.DSNOverride},
		Counts:       Counts{},
		Subprocesses: make([]Subprocess, 0, len(subprocesses)),
	}
	if !p.opts.StartedAt.IsZero() {
		startedAt := p.opts.StartedAt.UTC().Format(time.RFC3339)
		uptime := now.Sub(p.opts.StartedAt).Seconds()
		if uptime < 0 {
			uptime = 0
		}
		app.StartedAt = &startedAt
		app.UptimeSec = &uptime
	}
	if p.opts.Database != nil {
		if db := safeValueCtx(appCtx, "app", errs, p.opts.Database); db != nil {
			stats := db.Stats()
			app.DB.Pool = DBPool{
				OpenConnections:   stats.OpenConnections,
				InUse:             stats.InUse,
				Idle:              stats.Idle,
				MaxOpen:           stats.MaxOpenConnections,
				WaitCount:         stats.WaitCount,
				WaitDurationMS:    stats.WaitDuration.Milliseconds(),
				MaxIdleClosed:     stats.MaxIdleClosed,
				MaxLifetimeClosed: stats.MaxLifetimeClosed,
			}
		}
	}
	app.DBFiles = safeValueCtx(appCtx, "app", errs, func() DBFiles {
		return databaseFiles(p.opts.DatabasePath)
	})
	if p.opts.Counts != nil {
		app.Counts = safeValueCtx(appCtx, "app", errs, p.opts.Counts)
	}
	if p.opts.Scheduler != nil {
		app.Scheduler = safeValueCtx(appCtx, "app", errs, p.opts.Scheduler)
	}
	for _, subprocess := range subprocesses {
		if len(app.Subprocesses) >= maxSubprocesses {
			app.SubprocessesTruncated = true
			break
		}
		copy := subprocess
		if copy.LoadedPlugins != nil {
			copy.LoadedPlugins = append([]string(nil), subprocess.LoadedPlugins...)
		}
		enrichSubprocess(&copy, processes)
		app.Subprocesses = append(app.Subprocesses, copy)
	}
	return app
}

func enrichSubprocess(subprocess *Subprocess, processes *ProcessSection) {
	if subprocess == nil || processes == nil {
		return
	}
	for _, info := range processes.List {
		if info.PID != subprocess.PID {
			continue
		}
		subprocess.RSSBytes = info.RSSBytes
		subprocess.Threads = info.Threads
		subprocess.CPUUserTicks = info.CPUUserTicks
		subprocess.CPUSystemTicks = info.CPUSystemTicks
		subprocess.Alive = subprocess.Alive || info.State != "Z"
		if subprocess.StartedAtUnix == nil {
			subprocess.StartedAtUnix = copyFloat(info.StartedAtUnix)
		}
		return
	}
}

func databaseFiles(path string) DBFiles {
	var files DBFiles
	if path == "" {
		return files
	}
	files.MainBytes = fileSize(path)
	files.WALBytes = fileSize(path + "-wal")
	files.SHMBytes = fileSize(path + "-shm")
	files.TotalBytes = files.MainBytes + files.WALBytes + files.SHMBytes
	return files
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return 0
	}
	return info.Size()
}

func linkCgroupSections(snapshot *Snapshot) {
	if snapshot == nil {
		return
	}
	if snapshot.Cgroup != nil {
		if snapshot.CPU != nil {
			snapshot.CPU.Cgroup = snapshot.Cgroup.CPU
		}
		if snapshot.Memory != nil {
			snapshot.Memory.Cgroup = snapshot.Cgroup.Memory
		}
	}
	if snapshot.PSI != nil {
		if snapshot.CPU != nil {
			snapshot.CPU.PSI = snapshot.PSI.CPU
		}
		if snapshot.Memory != nil {
			snapshot.Memory.PSI = snapshot.PSI.Memory
		}
	}
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}

// safeValue 调用外部 provider，panic 只记录到 errors 并返回零值。
type callResult[T any] struct {
	value    T
	panicked any
}

// safeValueCtx 在带超时的 goroutine 中调用外部 provider；超时或 panic 只记录 errors。
func safeValueCtx[T any](ctx context.Context, section string, errs *[]ProbeError, fn func() T) (value T) {
	if fn == nil {
		return value
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		appendProbeError(errs, ProbeError{Section: section, Error: "timeout: " + err.Error()})
		var zero T
		return zero
	}
	done := make(chan callResult[T], 1)
	go func() {
		result := callResult[T]{}
		defer func() {
			result.panicked = recover()
			done <- result
		}()
		result.value = fn()
	}()
	select {
	case result := <-done:
		if result.panicked != nil {
			appendProbeError(errs, ProbeError{Section: section, Error: fmt.Sprintf("panic: %v", result.panicked)})
			var zero T
			return zero
		}
		return result.value
	case <-ctx.Done():
		appendProbeError(errs, ProbeError{Section: section, Error: "timeout: " + ctx.Err().Error()})
		var zero T
		return zero
	}
}

// collectWithRecover 执行单个 section，panic 与超时只记录到 errors。
func collectWithRecover(ctx context.Context, section, path string, errs *[]ProbeError, fn func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			appendProbeError(errs, ProbeError{Section: section, Path: path, Error: fmt.Sprintf("panic: %v", recovered)})
		}
	}()
	if err := ctx.Err(); err != nil {
		appendProbeError(errs, ProbeError{Section: section, Path: path, Error: "timeout: " + err.Error()})
		return
	}
	fn()
	if err := ctx.Err(); err != nil {
		appendProbeError(errs, ProbeError{Section: section, Path: path, Error: "timeout: " + err.Error()})
	}
}

func appendProbeError(errs *[]ProbeError, probeError ProbeError) {
	if errs == nil {
		return
	}
	*errs = append(*errs, probeError)
}

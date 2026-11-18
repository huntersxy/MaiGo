package system_probe

// Snapshot 是资源探针的完整响应；所有 section 与 Python 无对齐义务。
type Snapshot struct {
	Meta      Meta            `json:"meta"`
	CPU       *CPUSection     `json:"cpu"`
	Memory    *MemorySection  `json:"memory"`
	Disk      *DiskSection    `json:"disk"`
	Network   *NetworkSection `json:"network"`
	Processes *ProcessSection `json:"processes"`
	Runtime   *RuntimeSection `json:"runtime"`
	App       *AppSection     `json:"app"`
	Cgroup    *CgroupSection  `json:"cgroup"`
	PSI       *PSISection     `json:"psi"`
	History   HistorySection  `json:"history"`
	Errors    []ProbeError    `json:"errors"`
}

// Meta 描述本次采样的环境与边界。
type Meta struct {
	Schema              int      `json:"schema"`
	GeneratedAt         string   `json:"generated_at"`
	ElapsedMS           int64    `json:"elapsed_ms"`
	TimeoutMS           int64    `json:"timeout_ms"`
	WindowSec           *float64 `json:"window_sec"`
	Hostname            string   `json:"hostname"`
	OS                  string   `json:"os"`
	Arch                string   `json:"arch"`
	GoVersion           string   `json:"go_version"`
	PID                 int      `json:"pid"`
	InstanceDir         string   `json:"instance_dir"`
	Version             string   `json:"version"`
	StartedAt           *string  `json:"started_at"`
	UptimeSec           *float64 `json:"uptime_sec"`
	BootTimeUnix        *float64 `json:"boot_time_unix"`
	ClockTicksPerSecond int      `json:"clock_ticks_per_second"`
	Partial             bool     `json:"partial"`
	ErrorsTruncated     bool     `json:"errors_truncated"`
}

// CPUSection 汇总 /proc/stat、/proc/loadavg 与 cgroup CPU 限制。
type CPUSection struct {
	LogicalCores        int          `json:"logical_cores"`
	GOMAXPROCS          int          `json:"gomaxprocs"`
	ClockTicksPerSecond int          `json:"clock_ticks_per_second"`
	Load                CPULoad      `json:"load"`
	Stat                CPUStat      `json:"stat"`
	PerCore             []CPUCore    `json:"per_core"`
	PerCoreTruncated    bool         `json:"per_core_truncated"`
	Usage               CPUUsage     `json:"usage"`
	Cgroup              *CgroupCPU   `json:"cgroup"`
	PSI                 *PSIResource `json:"psi"`
}

// CPULoad 来自 /proc/loadavg。
type CPULoad struct {
	Load1          *float64 `json:"load1"`
	Load5          *float64 `json:"load5"`
	Load15         *float64 `json:"load15"`
	Runnable       int64    `json:"runnable"`
	TotalProcesses int64    `json:"total_processes"`
	LastPID        int64    `json:"last_pid"`
}

// CPUStat 是 /proc/stat 聚合行的 tick 与进程计数。
type CPUStat struct {
	UserTicks      uint64 `json:"user_ticks"`
	NiceTicks      uint64 `json:"nice_ticks"`
	SystemTicks    uint64 `json:"system_ticks"`
	IdleTicks      uint64 `json:"idle_ticks"`
	IOWaitTicks    uint64 `json:"iowait_ticks"`
	IRQTicks       uint64 `json:"irq_ticks"`
	SoftIRQTicks   uint64 `json:"softirq_ticks"`
	StealTicks     uint64 `json:"steal_ticks"`
	GuestTicks     uint64 `json:"guest_ticks"`
	GuestNiceTicks uint64 `json:"guest_nice_ticks"`
	Ctxt           uint64 `json:"ctxt"`
	Processes      uint64 `json:"processes"`
	ProcsRunning   uint64 `json:"procs_running"`
	ProcsBlocked   uint64 `json:"procs_blocked"`
}

// CPUCore 是单个逻辑核的 tick。
type CPUCore struct {
	Core           int    `json:"core"`
	UserTicks      uint64 `json:"user_ticks"`
	NiceTicks      uint64 `json:"nice_ticks"`
	SystemTicks    uint64 `json:"system_ticks"`
	IdleTicks      uint64 `json:"idle_ticks"`
	IOWaitTicks    uint64 `json:"iowait_ticks"`
	IRQTicks       uint64 `json:"irq_ticks"`
	SoftIRQTicks   uint64 `json:"softirq_ticks"`
	StealTicks     uint64 `json:"steal_ticks"`
	GuestTicks     uint64 `json:"guest_ticks"`
	GuestNiceTicks uint64 `json:"guest_nice_ticks"`
}

// CPUUsage 是两次采样之间的 CPU 占比，首采样或窗口不足时为 null。
type CPUUsage struct {
	UserPercent   *float64 `json:"user_percent"`
	SystemPercent *float64 `json:"system_percent"`
	IOWaitPercent *float64 `json:"iowait_percent"`
	StealPercent  *float64 `json:"steal_percent"`
	IdlePercent   *float64 `json:"idle_percent"`
	TotalPercent  *float64 `json:"total_percent"`
	WindowSec     *float64 `json:"window_sec"`
}

// MemorySection 汇总 /proc/meminfo、/proc/self/status 与 cgroup 内存限制。
type MemorySection struct {
	System  MemorySystem  `json:"system"`
	Usage   MemoryUsage   `json:"usage"`
	Cgroup  *CgroupMemory `json:"cgroup"`
	PSI     *PSIResource  `json:"psi"`
	Process ProcessMemory `json:"process"`
}

// MemorySystem 是 /proc/meminfo 的字节字段。
type MemorySystem struct {
	MemTotalBytes      int64 `json:"mem_total_bytes"`
	MemFreeBytes       int64 `json:"mem_free_bytes"`
	MemAvailableBytes  int64 `json:"mem_available_bytes"`
	BuffersBytes       int64 `json:"buffers_bytes"`
	CachedBytes        int64 `json:"cached_bytes"`
	SReclaimableBytes  int64 `json:"sreclaimable_bytes"`
	ShmemBytes         int64 `json:"shmem_bytes"`
	AnonPagesBytes     int64 `json:"anon_pages_bytes"`
	MappedBytes        int64 `json:"mapped_bytes"`
	PageTablesBytes    int64 `json:"page_tables_bytes"`
	SlabBytes          int64 `json:"slab_bytes"`
	DirtyBytes         int64 `json:"dirty_bytes"`
	WritebackBytes     int64 `json:"writeback_bytes"`
	SwapTotalBytes     int64 `json:"swap_total_bytes"`
	SwapFreeBytes      int64 `json:"swap_free_bytes"`
	SwapCachedBytes    int64 `json:"swap_cached_bytes"`
	CommitLimitBytes   int64 `json:"commit_limit_bytes"`
	CommittedASBytes   int64 `json:"committed_as_bytes"`
	HugePagesTotal     int64 `json:"huge_pages_total"`
	HugePagesFree      int64 `json:"huge_pages_free"`
	HugePagesSizeBytes int64 `json:"huge_pages_size_bytes"`
	VmallocUsedBytes   int64 `json:"vmalloc_used_bytes"`
}

// MemoryUsage 是系统内存占比；MemTotal 为 0 时全部为 null。
type MemoryUsage struct {
	UsedPercent      *float64 `json:"used_percent"`
	AvailablePercent *float64 `json:"available_percent"`
}

// ProcessMemory 是当前进程在 /proc/self/status 中的内存字段。
type ProcessMemory struct {
	RSSBytes             int64 `json:"rss_bytes"`
	PeakRSSBytes         int64 `json:"peak_rss_bytes"`
	VirtualBytes         int64 `json:"virtual_bytes"`
	PeakVirtualBytes     int64 `json:"peak_virtual_bytes"`
	SwapBytes            int64 `json:"swap_bytes"`
	AnonymousRSSBytes    int64 `json:"anonymous_rss_bytes"`
	FileRSSBytes         int64 `json:"file_rss_bytes"`
	SharedMemoryRSSBytes int64 `json:"shared_memory_rss_bytes"`
	PageTableBytes       int64 `json:"page_table_bytes"`
	Threads              int   `json:"threads"`
}

// DiskSection 汇总挂载点容量、块设备统计与 statfs 限流器状态。
type DiskSection struct {
	Mounts           []DiskMount  `json:"mounts"`
	Devices          []DiskDevice `json:"devices"`
	DevicesTruncated bool         `json:"devices_truncated"`
	StatfsStats      LimiterStats `json:"statfs_stats"`
}

// DiskMount 是一个挂载点的容量快照。
type DiskMount struct {
	Label             string   `json:"label"`
	Path              string   `json:"path"`
	TotalBytes        int64    `json:"total_bytes"`
	FreeBytes         int64    `json:"free_bytes"`
	AvailableBytes    int64    `json:"available_bytes"`
	UsedBytes         int64    `json:"used_bytes"`
	UsedPercent       *float64 `json:"used_percent"`
	InodesTotal       int64    `json:"inodes_total"`
	InodesFree        int64    `json:"inodes_free"`
	InodesUsedPercent *float64 `json:"inodes_used_percent"`
	FSTypeID          int64    `json:"fs_type_id"`
}

// DiskDevice 是 /proc/diskstats 的块设备累计量。
type DiskDevice struct {
	Name             string `json:"name"`
	ReadsCompleted   uint64 `json:"reads_completed"`
	ReadsMerged      uint64 `json:"reads_merged"`
	SectorsRead      uint64 `json:"sectors_read"`
	ReadBytes        uint64 `json:"read_bytes"`
	ReadTimeMS       uint64 `json:"read_time_ms"`
	WritesCompleted  uint64 `json:"writes_completed"`
	WritesMerged     uint64 `json:"writes_merged"`
	SectorsWritten   uint64 `json:"sectors_written"`
	WriteBytes       uint64 `json:"write_bytes"`
	WriteTimeMS      uint64 `json:"write_time_ms"`
	IOInProgress     uint64 `json:"io_in_progress"`
	IOTimeMS         uint64 `json:"io_time_ms"`
	WeightedIOTimeMS uint64 `json:"weighted_io_time_ms"`
}

// NetworkSection 汇总网卡、socket 与 TCP 状态计数。
type NetworkSection struct {
	Interfaces          []NetworkInterface `json:"interfaces"`
	InterfacesTruncated bool               `json:"interfaces_truncated"`
	Sockstat            Sockstat           `json:"sockstat"`
	TCPStates           TCPStates          `json:"tcp_states"`
	TCPScanned          int64              `json:"tcp_scanned"`
	TCPTruncated        bool               `json:"tcp_truncated"`
}

// NetworkInterface 是 /proc/net/dev 的累计计数。
type NetworkInterface struct {
	Name         string `json:"name"`
	RXBytes      uint64 `json:"rx_bytes"`
	RXPackets    uint64 `json:"rx_packets"`
	RXErrors     uint64 `json:"rx_errors"`
	RXDropped    uint64 `json:"rx_dropped"`
	RXFIFO       uint64 `json:"rx_fifo"`
	RXFrame      uint64 `json:"rx_frame"`
	RXCompressed uint64 `json:"rx_compressed"`
	RXMulticast  uint64 `json:"rx_multicast"`
	TXBytes      uint64 `json:"tx_bytes"`
	TXPackets    uint64 `json:"tx_packets"`
	TXErrors     uint64 `json:"tx_errors"`
	TXDropped    uint64 `json:"tx_dropped"`
	TXFIFO       uint64 `json:"tx_fifo"`
	TXColls      uint64 `json:"tx_colls"`
	TXCarrier    uint64 `json:"tx_carrier"`
	TXCompressed uint64 `json:"tx_compressed"`
}

// Sockstat 是 /proc/net/sockstat 与 sockstat6 的聚合值；mem 字段由页换算为字节。
type Sockstat struct {
	SocketsUsed int64 `json:"sockets_used"`
	TCPInUse    int64 `json:"tcp_in_use"`
	TCPOrphan   int64 `json:"tcp_orphan"`
	TCPTW       int64 `json:"tcp_tw"`
	TCPAlloc    int64 `json:"tcp_alloc"`
	TCPMemPages int64 `json:"tcp_mem_pages"`
	TCPMemBytes int64 `json:"tcp_mem_bytes"`
	UDPInUse    int64 `json:"udp_in_use"`
	UDPMemPages int64 `json:"udp_mem_pages"`
	UDPMemBytes int64 `json:"udp_mem_bytes"`
	RawInUse    int64 `json:"raw_in_use"`
	FragInUse   int64 `json:"frag_in_use"`
}

// TCPStates 是 /proc/net/tcp(6) 的 st 计数。
type TCPStates struct {
	Established int64 `json:"established"`
	SynSent     int64 `json:"syn_sent"`
	SynRecv     int64 `json:"syn_recv"`
	FinWait1    int64 `json:"fin_wait1"`
	FinWait2    int64 `json:"fin_wait2"`
	TimeWait    int64 `json:"time_wait"`
	Close       int64 `json:"close"`
	CloseWait   int64 `json:"close_wait"`
	LastAck     int64 `json:"last_ack"`
	Listen      int64 `json:"listen"`
	Closing     int64 `json:"closing"`
	Unknown     int64 `json:"unknown"`
}

// ProcessSection 是当前用户进程的按 RSS 排序快照。
type ProcessSection struct {
	Limit      int           `json:"limit"`
	Scanned    int           `json:"scanned"`
	Truncated  bool          `json:"truncated"`
	NofileSoft *uint64       `json:"nofile_soft"`
	NofileHard *uint64       `json:"nofile_hard"`
	OpenFDs    int           `json:"open_fds"`
	List       []ProcessInfo `json:"list"`
}

// ProcessInfo 是单个进程的只读信息。
type ProcessInfo struct {
	PID              int      `json:"pid"`
	PPID             int      `json:"ppid"`
	UID              int      `json:"uid"`
	Name             string   `json:"name"`
	State            string   `json:"state"`
	Cmdline          string   `json:"cmdline"`
	CmdlineTruncated bool     `json:"cmdline_truncated"`
	Kind             string   `json:"kind"`
	RSSBytes         int64    `json:"rss_bytes"`
	VirtualBytes     int64    `json:"virtual_bytes"`
	Threads          int      `json:"threads"`
	CPUUserTicks     uint64   `json:"cpu_user_ticks"`
	CPUSystemTicks   uint64   `json:"cpu_system_ticks"`
	StartedAtUnix    *float64 `json:"started_at_unix"`
}

// RuntimeSection 汇总 Go 运行时指标。
type RuntimeSection struct {
	Memstats           RuntimeMemstats      `json:"memstats"`
	GC                 RuntimeGC            `json:"gc"`
	Goroutines         int                  `json:"goroutines"`
	GOMAXPROCS         int                  `json:"gomaxprocs"`
	ThreadsTotal       uint64               `json:"threads_total"`
	GoroutinesRunning  uint64               `json:"goroutines_running"`
	GoroutinesRunnable uint64               `json:"goroutines_runnable"`
	GoroutinesWaiting  uint64               `json:"goroutines_waiting"`
	GoroutinesNotInGo  uint64               `json:"goroutines_not_in_go"`
	MemoryClasses      RuntimeMemoryClasses `json:"memory_classes"`
	Sched              RuntimeSched         `json:"sched"`
	CPUClasses         RuntimeCPUClasses    `json:"cpu_classes"`
}

// RuntimeMemstats 对应 runtime.MemStats 的稳定字段。
type RuntimeMemstats struct {
	AllocBytes        uint64   `json:"alloc_bytes"`
	TotalAllocBytes   uint64   `json:"total_alloc_bytes"`
	SysBytes          uint64   `json:"sys_bytes"`
	HeapAllocBytes    uint64   `json:"heap_alloc_bytes"`
	HeapSysBytes      uint64   `json:"heap_sys_bytes"`
	HeapIdleBytes     uint64   `json:"heap_idle_bytes"`
	HeapInuseBytes    uint64   `json:"heap_inuse_bytes"`
	HeapReleasedBytes uint64   `json:"heap_released_bytes"`
	HeapObjects       uint64   `json:"heap_objects"`
	StackInuseBytes   uint64   `json:"stack_inuse_bytes"`
	StackSysBytes     uint64   `json:"stack_sys_bytes"`
	MSpanInuseBytes   uint64   `json:"mspan_inuse_bytes"`
	MSpanSysBytes     uint64   `json:"mspan_sys_bytes"`
	MCacheInuseBytes  uint64   `json:"mcache_inuse_bytes"`
	MCacheSysBytes    uint64   `json:"mcache_sys_bytes"`
	BuckHashSysBytes  uint64   `json:"buck_hash_sys_bytes"`
	GCSysBytes        uint64   `json:"gc_sys_bytes"`
	OtherSysBytes     uint64   `json:"other_sys_bytes"`
	NextGCBytes       uint64   `json:"next_gc_bytes"`
	LastGCUnix        *float64 `json:"last_gc_unix"`
	NumGC             uint32   `json:"num_gc"`
	NumForcedGC       uint32   `json:"num_forced_gc"`
	GCCPUFraction     *float64 `json:"gc_cpu_fraction"`
	PauseTotalNS      uint64   `json:"pause_total_ns"`
	Mallocs           uint64   `json:"mallocs"`
	Frees             uint64   `json:"frees"`
	Lookups           uint64   `json:"lookups"`
}

// RuntimeGC 是 GC 与堆目标的运行时指标。
type RuntimeGC struct {
	PauseP50NS      *float64 `json:"pause_p50_ns"`
	PauseP95NS      *float64 `json:"pause_p95_ns"`
	PauseP99NS      *float64 `json:"pause_p99_ns"`
	PauseMaxNS      *float64 `json:"pause_max_ns"`
	PauseWindow     uint64   `json:"pause_window"`
	AutomaticCycles uint64   `json:"automatic_cycles"`
	ForcedCycles    uint64   `json:"forced_cycles"`
	GOGCPercent     *uint64  `json:"gogc_percent"`
	GOMEMLimitBytes *uint64  `json:"gomemlimit_bytes"`
	HeapGoalBytes   uint64   `json:"heap_goal_bytes"`
	HeapLiveBytes   uint64   `json:"heap_live_bytes"`
}

// RuntimeMemoryClasses 是 /memory/classes/* 的字节量。
type RuntimeMemoryClasses struct {
	HeapObjectsBytes   uint64 `json:"heap_objects_bytes"`
	HeapFreeBytes      uint64 `json:"heap_free_bytes"`
	HeapUnusedBytes    uint64 `json:"heap_unused_bytes"`
	HeapReleasedBytes  uint64 `json:"heap_released_bytes"`
	HeapStacksBytes    uint64 `json:"heap_stacks_bytes"`
	TotalBytes         uint64 `json:"total_bytes"`
	OSStacksBytes      uint64 `json:"os_stacks_bytes"`
	MSpanInuseBytes    uint64 `json:"mspan_inuse_bytes"`
	MCacheInuseBytes   uint64 `json:"mcache_inuse_bytes"`
	MetadataOtherBytes uint64 `json:"metadata_other_bytes"`
}

// RuntimeSched 是调度延迟直方图与窗口样本数。
type RuntimeSched struct {
	LatencyP50NS  *float64 `json:"latency_p50_ns"`
	LatencyP95NS  *float64 `json:"latency_p95_ns"`
	LatencyP99NS  *float64 `json:"latency_p99_ns"`
	LatencyMaxNS  *float64 `json:"latency_max_ns"`
	LatencyWindow uint64   `json:"latency_window"`
}

// RuntimeCPUClasses 是 /cpu/classes/* 的累计 CPU 秒。
type RuntimeCPUClasses struct {
	TotalCPUSeconds float64 `json:"total_cpu_seconds"`
	UserCPUSeconds  float64 `json:"user_cpu_seconds"`
	GCCPUSeconds    float64 `json:"gc_cpu_seconds"`
	IdleCPUSeconds  float64 `json:"idle_cpu_seconds"`
	MutexWaitNS     int64   `json:"mutex_wait_ns"`
}

// AppSection 是应用层只读状态，由 bootstrap 注入 provider。
type AppSection struct {
	Version               string         `json:"version"`
	UptimeSec             *float64       `json:"uptime_sec"`
	StartedAt             *string        `json:"started_at"`
	DB                    AppDB          `json:"db"`
	DBFiles               DBFiles        `json:"db_files"`
	Counts                Counts         `json:"counts"`
	Scheduler             SchedulerStats `json:"scheduler"`
	Subprocesses          []Subprocess   `json:"subprocesses"`
	SubprocessesTruncated bool           `json:"subprocesses_truncated"`
}

// AppDB 是连接池统计与库路径。
type AppDB struct {
	Path        string `json:"path"`
	DSNOverride bool   `json:"dsn_override"`
	Pool        DBPool `json:"pool"`
}

// DBPool 对应 sql.DBStats 的常用字段。
type DBPool struct {
	OpenConnections   int   `json:"open_connections"`
	InUse             int   `json:"in_use"`
	Idle              int   `json:"idle"`
	MaxOpen           int   `json:"max_open"`
	WaitCount         int64 `json:"wait_count"`
	WaitDurationMS    int64 `json:"wait_duration_ms"`
	MaxIdleClosed     int64 `json:"max_idle_closed"`
	MaxLifetimeClosed int64 `json:"max_lifetime_closed"`
}

// DBFiles 是 SQLite 主库与 WAL/SHM 的文件体积。
type DBFiles struct {
	MainBytes  int64 `json:"main_bytes"`
	WALBytes   int64 `json:"wal_bytes"`
	SHMBytes   int64 `json:"shm_bytes"`
	TotalBytes int64 `json:"total_bytes"`
}

// Counts 是消息计数快照。
type Counts struct {
	Inbound      int64 `json:"inbound"`
	Outbound     int64 `json:"outbound"`
	PendingChats int64 `json:"pending_chats"`
}

// SchedulerStats 是原生会话调度器快照；未启用时 Available 为 false。
type SchedulerStats struct {
	Available      bool   `json:"available"`
	Workers        int    `json:"workers"`
	ActiveSessions int    `json:"active_sessions"`
	QueuedTasks    int    `json:"queued_tasks"`
	RunningTasks   int    `json:"running_tasks"`
	Submitted      uint64 `json:"submitted"`
	Completed      uint64 `json:"completed"`
	Failed         uint64 `json:"failed"`
	Rejected       uint64 `json:"rejected"`
}

// Subprocess 是插件 Runner 或 Python 兼容侧车的白名单字段。
type Subprocess struct {
	Kind           string   `json:"kind"`
	Group          string   `json:"group"`
	PID            int      `json:"pid"`
	Alive          bool     `json:"alive"`
	RSSBytes       int64    `json:"rss_bytes"`
	Threads        int      `json:"threads"`
	CPUUserTicks   uint64   `json:"cpu_user_ticks"`
	CPUSystemTicks uint64   `json:"cpu_system_ticks"`
	StartedAtUnix  *float64 `json:"started_at_unix"`
	Phase          string   `json:"phase"`
	RestartCount   int      `json:"restart_count"`
	LoadedPlugins  []string `json:"loaded_plugins"`
	PythonSpawn    bool     `json:"python_spawn"`
}

// CgroupSection 是 cgroup v2/v1 的 CPU、内存与 pids 限制。
type CgroupSection struct {
	Version   string        `json:"version"`
	Path      string        `json:"path"`
	Available bool          `json:"available"`
	Partial   bool          `json:"partial"`
	CPU       *CgroupCPU    `json:"cpu"`
	Memory    *CgroupMemory `json:"memory"`
	Pids      *CgroupPids   `json:"pids"`
}

// CgroupCPU 是 cgroup CPU 统计与配额。
type CgroupCPU struct {
	UsageUSec        int64    `json:"usage_usec"`
	UserUSec         int64    `json:"user_usec"`
	SystemUSec       int64    `json:"system_usec"`
	NrPeriods        int64    `json:"nr_periods"`
	NrThrottled      int64    `json:"nr_throttled"`
	ThrottledUSec    int64    `json:"throttled_usec"`
	ThrottledPercent *float64 `json:"throttled_percent"`
	QuotaUSec        *int64   `json:"quota_us"`
	PeriodUSec       *int64   `json:"period_us"`
	CPUs             *float64 `json:"cpus"`
	Unlimited        bool     `json:"unlimited"`
}

// CgroupMemory 是 cgroup 内存用量与事件。
type CgroupMemory struct {
	CurrentBytes     uint64             `json:"current_bytes"`
	PeakBytes        *uint64            `json:"peak_bytes"`
	MaxBytes         *uint64            `json:"max_bytes"`
	HighBytes        *uint64            `json:"high_bytes"`
	SwapCurrentBytes uint64             `json:"swap_current_bytes"`
	SwapMaxBytes     *uint64            `json:"swap_max_bytes"`
	Events           CgroupMemoryEvents `json:"events"`
	Stat             CgroupMemoryStat   `json:"stat"`
}

// CgroupMemoryEvents 是 memory.events 计数。
type CgroupMemoryEvents struct {
	Low          uint64 `json:"low"`
	High         uint64 `json:"high"`
	Max          uint64 `json:"max"`
	OOM          uint64 `json:"oom"`
	OOMKill      uint64 `json:"oom_kill"`
	OOMGroupKill uint64 `json:"oom_group_kill"`
}

// CgroupMemoryStat 是 memory.stat 的常用字段，单位字节（pgfault 除外）。
type CgroupMemoryStat struct {
	AnonBytes         uint64 `json:"anon_bytes"`
	FileBytes         uint64 `json:"file_bytes"`
	KernelBytes       uint64 `json:"kernel_bytes"`
	SlabBytes         uint64 `json:"slab_bytes"`
	SockBytes         uint64 `json:"sock_bytes"`
	ShmemBytes        uint64 `json:"shmem_bytes"`
	Pgfault           uint64 `json:"pgfault"`
	Pgmajfault        uint64 `json:"pgmajfault"`
	WorkingsetRefault uint64 `json:"workingset_refault"`
}

// CgroupPids 是 pids.current/max；max 为 "max" 时 null。
type CgroupPids struct {
	Current int64   `json:"current"`
	Max     *uint64 `json:"max"`
}

// PSISection 是 system 与 cgroup 两套压力信息。
type PSISection struct {
	CPU    *PSIResource `json:"cpu"`
	Memory *PSIResource `json:"memory"`
	IO     *PSIResource `json:"io"`
}

// PSIResource 包含 system 与 cgroup 两组 some/full。
type PSIResource struct {
	System *PSIStatePair `json:"system"`
	Cgroup *PSIStatePair `json:"cgroup"`
}

// PSIStatePair 是 some/full 两行。
type PSIStatePair struct {
	Some *PSIState `json:"some"`
	Full *PSIState `json:"full"`
}

// PSIState 是单行 pressure 值。
type PSIState struct {
	Avg10   *float64 `json:"avg10"`
	Avg60   *float64 `json:"avg60"`
	Avg300  *float64 `json:"avg300"`
	TotalUS int64    `json:"total_us"`
}

// HistorySection 是进程内环形缓冲。
type HistorySection struct {
	Capacity int             `json:"capacity"`
	Samples  []HistorySample `json:"samples"`
}

// HistorySample 是单次 Snapshot 的摘要。
type HistorySample struct {
	TimestampUnix        int64    `json:"timestamp_unix"`
	CPUTotalPercent      *float64 `json:"cpu_total_percent"`
	MemoryAvailableBytes *uint64  `json:"memory_available_bytes"`
	ProcessRSSBytes      *uint64  `json:"process_rss_bytes"`
	Goroutines           int      `json:"goroutines"`
	DBInUse              *int     `json:"db_in_use"`
}

// ProbeError 记录单 section 的失败；不影响其它 section。
type ProbeError struct {
	Section string `json:"section"`
	Path    string `json:"path"`
	Error   string `json:"error"`
	Skipped bool   `json:"skipped"`
}

// LimiterStats 是 statfs 限流器的计数。
type LimiterStats struct {
	Capacity      int   `json:"capacity"`
	Total         int64 `json:"total"`
	OK            int64 `json:"ok"`
	Skipped       int64 `json:"skipped"`
	Stuck         int64 `json:"stuck"`
	MaxDurationMS int64 `json:"max_duration_ms"`
}

//go:build linux

package system_probe

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

type linuxCollector struct{}

func newPlatformCollector() collector {
	return linuxCollector{}
}

func (linuxCollector) collect(ctx context.Context, probe *Probe, subprocesses []Subprocess) platformData {
	var data platformData
	var errs []ProbeError
	reader := probe.opts.Reader

	collectWithRecover(ctx, "cpu", "/proc/stat", &errs, func() {
		raw, err := reader.ReadFile("/proc/stat", limitProcStat)
		if err != nil {
			appendProbeError(&errs, ProbeError{Section: "cpu", Path: "/proc/stat", Error: err.Error()})
			return
		}
		parsed, err := parseProcStat(raw)
		if err != nil {
			appendProbeError(&errs, ProbeError{Section: "cpu", Path: "/proc/stat", Error: err.Error()})
			return
		}
		cpu := &CPUSection{LogicalCores: len(parsed.Cores), Stat: parsed.Stat}
		if len(parsed.Cores) > maxProcStatCores {
			parsed.Cores = parsed.Cores[:maxProcStatCores]
			cpu.PerCoreTruncated = true
		}
		cpu.PerCore = parsed.Cores
		data.CPU = cpu
		data.CPUTicks = &parsed.Aggregate
		data.BTime = parsed.BTime
	})
	collectWithRecover(ctx, "cpu", "/proc/loadavg", &errs, func() {
		raw, err := reader.ReadFile("/proc/loadavg", limitProcStat)
		if err != nil {
			appendProbeError(&errs, ProbeError{Section: "cpu", Path: "/proc/loadavg", Error: err.Error()})
			return
		}
		if data.CPU != nil {
			data.CPU.Load = parseLoadavg(raw)
		}
	})

	collectWithRecover(ctx, "memory", "/proc/meminfo", &errs, func() {
		raw, err := reader.ReadFile("/proc/meminfo", limitProcMeminfo)
		if err != nil {
			appendProbeError(&errs, ProbeError{Section: "memory", Path: "/proc/meminfo", Error: err.Error()})
			return
		}
		system := parseMeminfo(raw)
		data.Memory = &MemorySection{System: system}
		if system.MemAvailableBytes > 0 {
			available := uint64(system.MemAvailableBytes)
			data.MemoryAvailable = &available
		}
	})
	collectWithRecover(ctx, "memory", "/proc/self/status", &errs, func() {
		raw, err := reader.ReadFile("/proc/self/status", limitProcStatus)
		if err != nil {
			appendProbeError(&errs, ProbeError{Section: "memory", Path: "/proc/self/status", Error: err.Error()})
			return
		}
		status := parseProcStatus(raw)
		if data.Memory == nil {
			data.Memory = &MemorySection{}
		}
		data.Memory.Process = status.Memory
		if status.Memory.RSSBytes > 0 {
			rss := uint64(status.Memory.RSSBytes)
			data.ProcessRSS = &rss
		}
	})

	data.Disk = &DiskSection{Mounts: make([]DiskMount, 0, 5)}
	collectWithRecover(ctx, "disk", "statfs", &errs, func() {
		instanceDir := strings.TrimSpace(probe.opts.InstanceDir)
		if instanceDir == "" {
			instanceDir = "."
		}
		type target struct {
			label string
			path  string
		}
		targets := []target{
			{"instance", instanceDir},
			{"data", filepath.Join(instanceDir, "data")},
			{"logs", filepath.Join(instanceDir, "logs")},
			{"temp", os.TempDir()},
			{"root", "/"},
		}
		statfsFn := probe.opts.StatfsFn
		if statfsFn == nil {
			statfsFn = defaultStatfs
		}
		for _, item := range targets {
			if ctx.Err() != nil {
				appendProbeError(&errs, ProbeError{Section: "disk", Path: item.path, Error: "timeout: " + ctx.Err().Error()})
				return
			}
			result, skipped, err := probe.limiter.run(ctx, func() (StatfsResult, error) {
				return statfsFn(item.path)
			})
			if err != nil {
				appendProbeError(&errs, ProbeError{Section: "disk", Path: item.path, Error: err.Error(), Skipped: skipped})
				continue
			}
			if skipped {
				appendProbeError(&errs, ProbeError{Section: "disk", Path: item.path, Error: "statfs 槽位不可用", Skipped: true})
				continue
			}
			data.Disk.Mounts = append(data.Disk.Mounts, buildDiskMount(item.label, item.path, result))
		}
	})
	collectWithRecover(ctx, "disk", "/proc/diskstats", &errs, func() {
		raw, err := reader.ReadFile("/proc/diskstats", limitProcDiskstats)
		if err != nil {
			appendProbeError(&errs, ProbeError{Section: "disk", Path: "/proc/diskstats", Error: err.Error()})
			return
		}
		devices := parseDiskstats(raw)
		if len(devices) > maxDiskDevices {
			devices = devices[:maxDiskDevices]
			data.Disk.DevicesTruncated = true
		}
		data.Disk.Devices = devices
	})
	data.Disk.StatfsStats = probe.limiter.stats()

	collectWithRecover(ctx, "network", "/proc/net/dev", &errs, func() {
		network := &NetworkSection{}
		raw, err := reader.ReadFile("/proc/net/dev", limitProcNetFile)
		if err != nil {
			appendProbeError(&errs, ProbeError{Section: "network", Path: "/proc/net/dev", Error: err.Error()})
		} else {
			interfaces := parseNetDev(raw)
			if len(interfaces) > maxNetworkIfaces {
				interfaces = interfaces[:maxNetworkIfaces]
				network.InterfacesTruncated = true
			}
			network.Interfaces = interfaces
		}
		for _, path := range []string{"/proc/net/sockstat", "/proc/net/sockstat6"} {
			raw, err := reader.ReadFile(path, limitProcNetFile)
			if err != nil {
				appendProbeError(&errs, ProbeError{Section: "network", Path: path, Error: err.Error()})
				continue
			}
			parseSockstat(raw, &network.Sockstat)
		}
		for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
			file, err := reader.Open(path)
			if err != nil {
				appendProbeError(&errs, ProbeError{Section: "network", Path: path, Error: err.Error()})
				continue
			}
			states, scanned, truncated, scanErr := countTCPStates(ctx, file, maxProcNetLines)
			_ = file.Close()
			if scanErr != nil {
				appendProbeError(&errs, ProbeError{Section: "network", Path: path, Error: scanErr.Error(), Skipped: ctx.Err() != nil})
			}
			network.TCPStates = addTCPStates(network.TCPStates, states)
			network.TCPScanned += scanned
			network.TCPTruncated = network.TCPTruncated || truncated
		}
		data.Network = network
	})

	collectWithRecover(ctx, "processes", "/proc", &errs, func() {
		section, processErrs := collectProcesses(ctx, probe, reader, subprocesses, data.BTime)
		errs = append(errs, processErrs...)
		data.Processes = section
	})

	cgroupBase := ""
	collectWithRecover(ctx, "cgroup", "/proc/self/cgroup", &errs, func() {
		raw, err := reader.ReadFile("/proc/self/cgroup", limitProcStat)
		if err != nil {
			appendProbeError(&errs, ProbeError{Section: "cgroup", Path: "/proc/self/cgroup", Error: err.Error()})
			data.Cgroup = &CgroupSection{Version: "none"}
			return
		}
		version, path := parseCgroupVersion(raw)
		cgroup := &CgroupSection{Version: version, Path: path}
		if version == "v2" {
			cgroupBase = filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(path, "/"))
			collectCgroupV2(ctx, probe, reader, cgroup, cgroupBase, &errs)
		} else {
			cgroupBase = "/sys/fs/cgroup"
			collectCgroupV1(ctx, probe, reader, cgroup, parseCgroupV1ControllerPaths(raw), &errs)
		}
		data.Cgroup = cgroup
	})

	collectWithRecover(ctx, "psi", "/proc/pressure", &errs, func() {
		data.PSI = collectPSI(ctx, probe, reader, cgroupBase, &errs)
	})

	data.Errors = errs
	return data
}

func buildDiskMount(label, path string, result StatfsResult) DiskMount {
	blockSize := result.BlockSize
	if blockSize <= 0 {
		blockSize = 4096
	}
	mount := DiskMount{
		Label:          label,
		Path:           path,
		TotalBytes:     result.Blocks * blockSize,
		FreeBytes:      result.BFree * blockSize,
		AvailableBytes: result.BAvail * blockSize,
		InodesTotal:    result.Files,
		InodesFree:     result.FFree,
		FSTypeID:       result.FSTypeID,
	}
	mount.UsedBytes = mount.TotalBytes - mount.FreeBytes
	if mount.UsedBytes < 0 {
		mount.UsedBytes = 0
	}
	if mount.TotalBytes > 0 {
		percent := float64(mount.UsedBytes) / float64(mount.TotalBytes) * 100
		mount.UsedPercent = &percent
	}
	if mount.InodesTotal > 0 {
		percent := float64(mount.InodesTotal-mount.InodesFree) / float64(mount.InodesTotal) * 100
		mount.InodesUsedPercent = &percent
	}
	return mount
}

func defaultStatfs(path string) (StatfsResult, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return StatfsResult{}, err
	}
	return StatfsResult{
		Blocks:    int64(stat.Blocks),
		BFree:     int64(stat.Bfree),
		BAvail:    int64(stat.Bavail),
		Files:     int64(stat.Files),
		FFree:     int64(stat.Ffree),
		FSTypeID:  int64(stat.Type),
		BlockSize: int64(stat.Bsize),
	}, nil
}

func addTCPStates(a, b TCPStates) TCPStates {
	return TCPStates{
		Established: a.Established + b.Established,
		SynSent:     a.SynSent + b.SynSent,
		SynRecv:     a.SynRecv + b.SynRecv,
		FinWait1:    a.FinWait1 + b.FinWait1,
		FinWait2:    a.FinWait2 + b.FinWait2,
		TimeWait:    a.TimeWait + b.TimeWait,
		Close:       a.Close + b.Close,
		CloseWait:   a.CloseWait + b.CloseWait,
		LastAck:     a.LastAck + b.LastAck,
		Listen:      a.Listen + b.Listen,
		Closing:     a.Closing + b.Closing,
		Unknown:     a.Unknown + b.Unknown,
	}
}

func collectProcesses(ctx context.Context, probe *Probe, reader Reader, subprocesses []Subprocess, btime *float64) (*ProcessSection, []ProbeError) {
	var errs []ProbeError
	section := &ProcessSection{Limit: probe.opts.ProcessLimit, List: make([]ProcessInfo, 0, probe.opts.ProcessLimit)}
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err == nil {
		soft := uint64(limit.Cur)
		hard := uint64(limit.Max)
		section.NofileSoft = &soft
		section.NofileHard = &hard
	}
	if fds, err := reader.ReadDir("/proc/self/fd"); err == nil {
		section.OpenFDs = len(fds)
	}

	requiredKinds := make(map[int]string, len(subprocesses)+1)
	selfPID := os.Getpid()
	requiredKinds[selfPID] = "self"
	for _, subprocess := range subprocesses {
		if subprocess.PID <= 0 {
			continue
		}
		if _, exists := requiredKinds[subprocess.PID]; !exists {
			requiredKinds[subprocess.PID] = subprocess.Kind
		}
	}

	names, err := reader.ReadDir("/proc")
	if err != nil {
		appendProbeError(&errs, ProbeError{Section: "processes", Path: "/proc", Error: err.Error()})
		return section, errs
	}
	euid := os.Geteuid()
	pageSize := int64(os.Getpagesize())
	candidates := make([]processCandidate, 0, probe.opts.ProcessLimit+len(requiredKinds))
	addProcess := func(pid int, kind string, required bool, uid int) bool {
		procPath := filepath.Join("/proc", strconv.Itoa(pid))
		statRaw, err := reader.ReadFile(filepath.Join(procPath, "stat"), limitProcStat)
		if err != nil {
			return false
		}
		stat, err := parseProcStatFile(statRaw)
		if err != nil {
			return false
		}
		statusRaw, _ := reader.ReadFile(filepath.Join(procPath, "status"), limitProcStatus)
		status := parseProcStatus(statusRaw)
		cmdlineRaw, _ := reader.ReadFile(filepath.Join(procPath, "cmdline"), limitProcCmdline)
		cmdline, cmdlineTruncated := parseCmdline(cmdlineRaw)

		rssBytes := status.Memory.RSSBytes
		if rssBytes <= 0 && stat.RSSPages > 0 {
			rssBytes = stat.RSSPages * pageSize
		}
		virtualBytes := status.Memory.VirtualBytes
		if virtualBytes <= 0 {
			virtualBytes = stat.VirtualBytes
		}
		threads := stat.Threads
		if threads <= 0 {
			threads = status.Threads
		}
		if !required {
			kind = "other"
		}
		if kind == "" {
			kind = "other"
		}
		info := ProcessInfo{
			PID: stat.PID, PPID: stat.PPID, UID: uid, Name: stat.Name, State: stat.State,
			Cmdline: cmdline, CmdlineTruncated: cmdlineTruncated, Kind: kind,
			RSSBytes: rssBytes, VirtualBytes: virtualBytes, Threads: threads,
			CPUUserTicks: stat.UserTicks, CPUSystemTicks: stat.SystemTicks,
		}
		if btime != nil && stat.StartTimeTicks > 0 {
			started := *btime + float64(stat.StartTimeTicks)/clockTicksPerSec
			info.StartedAtUnix = &started
		}
		candidates = append(candidates, processCandidate{
			PID: pid, UID: uid, Required: required, Info: info,
		})
		return true
	}

	requiredPIDs := make([]int, 0, len(requiredKinds))
	for pid := range requiredKinds {
		requiredPIDs = append(requiredPIDs, pid)
	}
	sort.Ints(requiredPIDs)
	for _, pid := range requiredPIDs {
		if ctx.Err() != nil {
			appendProbeError(&errs, ProbeError{Section: "processes", Path: "/proc", Error: "timeout: " + ctx.Err().Error()})
			break
		}
		procPath := filepath.Join("/proc", strconv.Itoa(pid))
		uid, uidErr := reader.OwnerUID(procPath)
		if uidErr != nil {
			uid = -1
		}
		addProcess(pid, requiredKinds[pid], true, uid)
	}

	scanned := 0
	truncated := false
	examined := 0
	for _, name := range names {
		examined++
		if examined%32 == 0 && ctx.Err() != nil {
			truncated = true
			appendProbeError(&errs, ProbeError{Section: "processes", Path: "/proc", Error: "timeout: " + ctx.Err().Error()})
			break
		}
		pid, err := strconv.Atoi(name)
		if err != nil || pid <= 0 {
			continue
		}
		if _, required := requiredKinds[pid]; required {
			continue
		}
		procPath := filepath.Join("/proc", name)
		uid, uidErr := reader.OwnerUID(procPath)
		if uidErr != nil || uid != euid {
			continue
		}
		if scanned >= probe.opts.ScanLimit {
			truncated = true
			break
		}
		scanned++
		addProcess(pid, "other", false, uid)
	}
	section.Scanned = scanned
	section.Truncated = truncated
	filtered := filterProcessesByUID(candidates, euid)
	section.List = selectProcesses(filtered, probe.opts.ProcessLimit)
	return section, errs
}

func selectProcesses(candidates []processCandidate, limit int) []ProcessInfo {
	if limit <= 0 {
		limit = defaultProcessShow
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Info.RSSBytes == candidates[j].Info.RSSBytes {
			return candidates[i].PID < candidates[j].PID
		}
		return candidates[i].Info.RSSBytes > candidates[j].Info.RSSBytes
	})
	selected := make([]processCandidate, 0, limit+len(candidates))
	selectedPIDs := make(map[int]bool, limit)
	for _, candidate := range candidates {
		if !candidate.Required {
			continue
		}
		selected = append(selected, candidate)
		selectedPIDs[candidate.PID] = true
	}
	for _, candidate := range candidates {
		if len(selected) >= limit {
			break
		}
		if selectedPIDs[candidate.PID] {
			continue
		}
		selected = append(selected, candidate)
		selectedPIDs[candidate.PID] = true
	}
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].Info.RSSBytes == selected[j].Info.RSSBytes {
			return selected[i].PID < selected[j].PID
		}
		return selected[i].Info.RSSBytes > selected[j].Info.RSSBytes
	})
	out := make([]ProcessInfo, 0, len(selected))
	for _, candidate := range selected {
		out = append(out, candidate.Info)
	}
	return out
}

func collectCgroupV2(ctx context.Context, probe *Probe, reader Reader, cgroup *CgroupSection, base string, errs *[]ProbeError) {
	partial := false
	read := func(name string) ([]byte, bool) {
		if ctx.Err() != nil {
			return nil, false
		}
		raw, err := reader.ReadFile(filepath.Join(base, name), limitCgroupFile)
		if err != nil {
			partial = true
			return nil, false
		}
		return raw, true
	}
	cpu := &CgroupCPU{}
	cpuSeen := false
	if raw, ok := read("cpu.stat"); ok {
		*cpu = parseCgroupV2CPUStat(raw)
		cpuSeen = true
	}
	if raw, ok := read("cpu.max"); ok {
		parseCgroupV2CPUMax(raw, cpu)
		cpuSeen = true
	}
	if cpuSeen {
		cgroup.CPU = cpu
	}
	memory := &CgroupMemory{}
	memorySeen := false
	if raw, ok := read("memory.current"); ok {
		memory.CurrentBytes = parseUintBytes(raw)
		memorySeen = true
	}
	if raw, ok := read("memory.peak"); ok {
		if value, valid := parseUintValue(strings.TrimSpace(string(raw))); valid {
			memory.PeakBytes = uintPtr(value)
		}
	}
	if raw, ok := read("memory.max"); ok {
		memory.MaxBytes = parseCgroupV2MemoryLimit(raw)
		memorySeen = true
	}
	if raw, ok := read("memory.high"); ok {
		memory.HighBytes = parseCgroupV2MemoryLimit(raw)
	}
	if raw, ok := read("memory.swap.current"); ok {
		memory.SwapCurrentBytes = parseUintBytes(raw)
	}
	if raw, ok := read("memory.swap.max"); ok {
		memory.SwapMaxBytes = parseCgroupV2MemoryLimit(raw)
	}
	if raw, ok := read("memory.events"); ok {
		memory.Events = parseCgroupMemoryEvents(raw)
	}
	if raw, ok := read("memory.stat"); ok {
		memory.Stat = parseCgroupMemoryStat(raw)
	}
	if memorySeen {
		cgroup.Memory = memory
	}
	pids := &CgroupPids{}
	pidsSeen := false
	if raw, ok := read("pids.current"); ok {
		pids.Current = parseCgroupPidsCurrent(raw)
		pidsSeen = true
	}
	if raw, ok := read("pids.max"); ok {
		pids.Max = parseCgroupPidsMax(raw)
		pidsSeen = true
	}
	if pidsSeen {
		cgroup.Pids = pids
	}
	cgroup.Available = cpuSeen || memorySeen || pidsSeen
	cgroup.Partial = partial
	if !cgroup.Available {
		appendProbeError(errs, ProbeError{Section: "cgroup", Path: base, Error: "cgroup v2 文件不可读"})
	}
}

func collectCgroupV1(ctx context.Context, probe *Probe, reader Reader, cgroup *CgroupSection, controllerPaths map[string]string, errs *[]ProbeError) {
	partial := len(controllerPaths) == 0
	cpuDir := cgroupV1ControllerDir(reader, []string{"/sys/fs/cgroup/cpu,cpuacct", "/sys/fs/cgroup/cpu"}, controllerPaths, "cpu")
	cpuacctDir := cgroupV1ControllerDir(reader, []string{"/sys/fs/cgroup/cpu,cpuacct", "/sys/fs/cgroup/cpuacct"}, controllerPaths, "cpuacct")
	memoryDir := cgroupV1ControllerDir(reader, []string{"/sys/fs/cgroup/memory"}, controllerPaths, "memory")
	pidsDir := cgroupV1ControllerDir(reader, []string{"/sys/fs/cgroup/pids"}, controllerPaths, "pids")
	read := func(dir, name string) ([]byte, bool) {
		if dir == "" || ctx.Err() != nil {
			partial = true
			return nil, false
		}
		raw, err := reader.ReadFile(filepath.Join(dir, name), limitCgroupFile)
		if err != nil {
			partial = true
			return nil, false
		}
		return raw, true
	}
	cpu := &CgroupCPU{}
	cpuSeen := false
	if raw, ok := read(cpuDir, "cpu.stat"); ok {
		*cpu = parseCgroupV1CPUStat(raw)
		cpuSeen = true
	}
	if raw, ok := read(cpuDir, "cpu.cfs_quota_us"); ok {
		parseCgroupV1CFSQuota(raw, cpu)
	}
	if raw, ok := read(cpuDir, "cpu.cfs_period_us"); ok {
		parseCgroupV1CFSPeriod(raw, cpu)
	}
	if raw, ok := read(cpuacctDir, "cpuacct.usage"); ok {
		parseCgroupV1CPUAcctUsage(raw, cpu)
		cpuSeen = true
	}
	if cpuSeen {
		cgroup.CPU = cpu
	}
	memory := &CgroupMemory{}
	memorySeen := false
	if raw, ok := read(memoryDir, "memory.usage_in_bytes"); ok {
		memory.CurrentBytes = parseUintBytes(raw)
		memorySeen = true
	}
	if raw, ok := read(memoryDir, "memory.max_usage_in_bytes"); ok {
		if value, valid := parseUintValue(strings.TrimSpace(string(raw))); valid {
			memory.PeakBytes = uintPtr(value)
		}
	}
	if raw, ok := read(memoryDir, "memory.memsw.usage_in_bytes"); ok {
		memory.SwapCurrentBytes = parseUintBytes(raw)
	}
	if raw, ok := read(memoryDir, "memory.limit_in_bytes"); ok {
		memory.MaxBytes = parseCgroupV1MemoryLimit(raw)
		memorySeen = true
	}
	if raw, ok := read(memoryDir, "memory.stat"); ok {
		memory.Stat = parseCgroupMemoryStat(raw)
	}
	if memorySeen {
		cgroup.Memory = memory
	}
	pids := &CgroupPids{}
	pidsSeen := false
	if raw, ok := read(pidsDir, "pids.current"); ok {
		pids.Current = parseCgroupPidsCurrent(raw)
		pidsSeen = true
	}
	if raw, ok := read(pidsDir, "pids.max"); ok {
		pids.Max = parseCgroupPidsMax(raw)
		pidsSeen = true
	}
	if pidsSeen {
		cgroup.Pids = pids
	}
	cgroup.Available = cpuSeen || memorySeen || pidsSeen
	cgroup.Partial = partial
	if !cgroup.Available {
		appendProbeError(errs, ProbeError{Section: "cgroup", Path: "/sys/fs/cgroup", Error: "cgroup v1 文件不可读"})
	}
}

func cgroupV1ControllerDir(reader Reader, mountDirs []string, controllerPaths map[string]string, controller string) string {
	relative, ok := controllerPaths[controller]
	if !ok {
		return ""
	}
	for _, dir := range mountDirs {
		candidate := joinCgroupRelative(dir, relative)
		if _, err := reader.ReadDir(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func joinCgroupRelative(dir, relative string) string {
	relative = strings.TrimSpace(relative)
	relative = strings.TrimPrefix(relative, "/")
	if relative == "" || relative == "." {
		return dir
	}
	return filepath.Join(dir, relative)
}

func collectPSI(ctx context.Context, probe *Probe, reader Reader, cgroupBase string, errs *[]ProbeError) *PSISection {
	section := &PSISection{}
	resources := []struct {
		name       string
		systemPath string
		cgroupName string
		target     **PSIResource
	}{
		{"cpu", "/proc/pressure/cpu", "cpu.pressure", &section.CPU},
		{"memory", "/proc/pressure/memory", "memory.pressure", &section.Memory},
		{"io", "/proc/pressure/io", "io.pressure", &section.IO},
	}
	for _, item := range resources {
		resource := &PSIResource{}
		if raw, err := reader.ReadFile(item.systemPath, limitCgroupFile); err == nil {
			resource.System = parsePSI(raw)
		} else {
			appendProbeError(errs, ProbeError{Section: "psi", Path: item.systemPath, Error: err.Error()})
		}
		if cgroupBase != "" {
			path := filepath.Join(cgroupBase, item.cgroupName)
			if raw, err := reader.ReadFile(path, limitCgroupFile); err == nil {
				resource.Cgroup = parsePSI(raw)
			} else if resource.System != nil {
				appendProbeError(errs, ProbeError{Section: "psi", Path: path, Error: err.Error()})
			}
		}
		if resource.System == nil && resource.Cgroup == nil {
			continue
		}
		*item.target = resource
	}
	if section.CPU == nil {
		appendProbeError(errs, ProbeError{Section: "psi", Path: "/proc/pressure/cpu", Error: "未找到 pressure 数据"})
	}
	if section.Memory == nil {
		appendProbeError(errs, ProbeError{Section: "psi", Path: "/proc/pressure/memory", Error: "未找到 pressure 数据"})
	}
	if section.IO == nil {
		appendProbeError(errs, ProbeError{Section: "psi", Path: "/proc/pressure/io", Error: "未找到 pressure 数据"})
	}
	if section.CPU == nil && section.Memory == nil && section.IO == nil {
		return nil
	}
	return section
}

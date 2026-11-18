package system_probe

import (
	"strconv"
	"strings"
)

// parseCgroupVersion 从 /proc/self/cgroup 判断 v2/v1；v2 同时返回相对层路径。
func parseCgroupVersion(data []byte) (string, string) {
	version := "v1"
	path := ""
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), ":", 3)
		if len(fields) != 3 {
			continue
		}
		if fields[1] == "" {
			version = "v2"
			path = fields[2]
		}
		if version == "v1" && path == "" && strings.Contains(fields[1], "cpu") {
			path = fields[2]
		}
	}
	return version, path
}

// parseCgroupV1ControllerPaths 返回 controller 到相对 cgroup 路径的映射；逗号分隔的 controller 逐个展开。
func parseCgroupV1ControllerPaths(data []byte) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), ":", 3)
		if len(fields) != 3 || fields[1] == "" {
			continue
		}
		for _, controller := range strings.Split(fields[1], ",") {
			controller = strings.TrimSpace(controller)
			if controller != "" {
				out[controller] = fields[2]
			}
		}
	}
	return out
}

func parseCgroupKeyValues(data []byte) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		out[fields[0]] = fields[1]
	}
	return out
}

func parseUintValue(value string) (uint64, bool) {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func parseIntValue(value string) (int64, bool) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func uintPtr(value uint64) *uint64 {
	return &value
}

func int64Ptr(value int64) *int64 {
	return &value
}

// parseCgroupV2CPUStat 解析 v2 cpu.stat（已是微秒）。
func parseCgroupV2CPUStat(data []byte) CgroupCPU {
	values := parseCgroupKeyValues(data)
	var out CgroupCPU
	out.UsageUSec = int64FromMap(values, "usage_usec")
	out.UserUSec = int64FromMap(values, "user_usec")
	out.SystemUSec = int64FromMap(values, "system_usec")
	out.NrPeriods = int64FromMap(values, "nr_periods")
	out.NrThrottled = int64FromMap(values, "nr_throttled")
	out.ThrottledUSec = int64FromMap(values, "throttled_usec")
	fillThrottledPercent(&out)
	return out
}

// parseCgroupV1CPUStat 解析 v1 cpu.stat；throttled_time 是纳秒。
func parseCgroupV1CPUStat(data []byte) CgroupCPU {
	values := parseCgroupKeyValues(data)
	var out CgroupCPU
	out.NrPeriods = int64FromMap(values, "nr_periods")
	out.NrThrottled = int64FromMap(values, "nr_throttled")
	if raw, ok := parseIntValue(values["throttled_time"]); ok {
		out.ThrottledUSec = raw / 1000
	}
	fillThrottledPercent(&out)
	return out
}

func fillThrottledPercent(cpu *CgroupCPU) {
	if cpu == nil || cpu.NrPeriods <= 0 {
		return
	}
	percent := float64(cpu.NrThrottled) / float64(cpu.NrPeriods) * 100
	cpu.ThrottledPercent = &percent
}

func int64FromMap(values map[string]string, key string) int64 {
	if value, ok := parseIntValue(values[key]); ok {
		return value
	}
	return 0
}

// parseCgroupV2CPUMax 解析 "max 100000" 或 "50000 100000"。
func parseCgroupV2CPUMax(data []byte, target *CgroupCPU) {
	if target == nil {
		return
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return
	}
	if fields[0] == "max" {
		target.Unlimited = true
		target.QuotaUSec = nil
	} else if quota, ok := parseIntValue(fields[0]); ok {
		target.QuotaUSec = int64Ptr(quota)
	}
	if len(fields) > 1 {
		if period, ok := parseIntValue(fields[1]); ok {
			target.PeriodUSec = int64Ptr(period)
		}
	}
	if target.QuotaUSec != nil && target.PeriodUSec != nil && *target.PeriodUSec > 0 {
		cpus := float64(*target.QuotaUSec) / float64(*target.PeriodUSec)
		target.CPUs = &cpus
	}
}

// parseCgroupV1CFSQuota 解析 cpu.cfs_quota_us；-1 表示不限。
func parseCgroupV1CFSQuota(data []byte, target *CgroupCPU) {
	if target == nil {
		return
	}
	value, ok := parseIntValue(string(data))
	if !ok {
		return
	}
	if value < 0 {
		target.Unlimited = true
		target.QuotaUSec = nil
		return
	}
	target.QuotaUSec = int64Ptr(value)
}

// parseCgroupV1CFSPeriod 解析 cpu.cfs_period_us。
func parseCgroupV1CFSPeriod(data []byte, target *CgroupCPU) {
	if target == nil {
		return
	}
	if value, ok := parseIntValue(string(data)); ok && value > 0 {
		target.PeriodUSec = int64Ptr(value)
	}
	if target.QuotaUSec != nil && target.PeriodUSec != nil {
		cpus := float64(*target.QuotaUSec) / float64(*target.PeriodUSec)
		target.CPUs = &cpus
	}
}

// parseCgroupV1CPUAcctUsage 解析 cpuacct.usage（纳秒）并换算为微秒。
func parseCgroupV1CPUAcctUsage(data []byte, target *CgroupCPU) {
	if target == nil {
		return
	}
	if value, ok := parseIntValue(string(data)); ok {
		target.UsageUSec = value / 1000
	}
}

// parseCgroupV2MemoryLimit 解析 memory.max/high/swap.max；"max" 表示不限。
func parseCgroupV2MemoryLimit(data []byte) *uint64 {
	value := strings.TrimSpace(string(data))
	if value == "" || value == "max" {
		return nil
	}
	if parsed, ok := parseUintValue(value); ok {
		return uintPtr(parsed)
	}
	return nil
}

// parseCgroupV1MemoryLimit 解析 v1 内存上限；>=2^60 视为不限。
func parseCgroupV1MemoryLimit(data []byte) *uint64 {
	value, ok := parseUintValue(string(data))
	if !ok || value >= 1<<60 {
		return nil
	}
	return uintPtr(value)
}

func parseUintBytes(data []byte) uint64 {
	value, _ := parseUintValue(strings.TrimSpace(string(data)))
	return value
}

func parseCgroupMemoryEvents(data []byte) CgroupMemoryEvents {
	values := parseCgroupKeyValues(data)
	return CgroupMemoryEvents{
		Low:          uint64FromMap(values, "low"),
		High:         uint64FromMap(values, "high"),
		Max:          uint64FromMap(values, "max"),
		OOM:          uint64FromMap(values, "oom"),
		OOMKill:      uint64FromMap(values, "oom_kill"),
		OOMGroupKill: uint64FromMap(values, "oom_group_kill"),
	}
}

func uint64FromMap(values map[string]string, key string) uint64 {
	if value, ok := parseUintValue(values[key]); ok {
		return value
	}
	return 0
}

func parseCgroupMemoryStat(data []byte) CgroupMemoryStat {
	values := parseCgroupKeyValues(data)
	return CgroupMemoryStat{
		AnonBytes:         uint64FromMap(values, "anon"),
		FileBytes:         uint64FromMap(values, "file"),
		KernelBytes:       uint64FromMap(values, "kernel"),
		SlabBytes:         uint64FromMap(values, "slab"),
		SockBytes:         uint64FromMap(values, "sock"),
		ShmemBytes:        uint64FromMap(values, "shmem"),
		Pgfault:           uint64FromMap(values, "pgfault"),
		Pgmajfault:        uint64FromMap(values, "pgmajfault"),
		WorkingsetRefault: uint64FromMap(values, "workingset_refault"),
	}
}

func parseCgroupPidsCurrent(data []byte) int64 {
	value, _ := parseIntValue(strings.TrimSpace(string(data)))
	return value
}

func parseCgroupPidsMax(data []byte) *uint64 {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "max" {
		return nil
	}
	if value, ok := parseUintValue(trimmed); ok {
		return uintPtr(value)
	}
	return nil
}

// parsePSI 解析 pressure 文件的 some/full 两行；缺失行为 nil。
func parsePSI(data []byte) *PSIStatePair {
	var pair PSIStatePair
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		state := parsePSIState(fields[1:])
		if state == nil {
			continue
		}
		switch fields[0] {
		case "some":
			pair.Some = state
		case "full":
			pair.Full = state
		}
	}
	if pair.Some == nil && pair.Full == nil {
		return nil
	}
	return &pair
}

func parsePSIState(fields []string) *PSIState {
	state := &PSIState{}
	found := false
	for _, field := range fields {
		key, raw, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch key {
		case "avg10":
			state.Avg10 = parseFloatPtr(raw)
			found = true
		case "avg60":
			state.Avg60 = parseFloatPtr(raw)
			found = true
		case "avg300":
			state.Avg300 = parseFloatPtr(raw)
			found = true
		case "total":
			if value, ok := parseIntValue(raw); ok {
				state.TotalUS = value
				found = true
			}
		}
	}
	if !found {
		return nil
	}
	return state
}

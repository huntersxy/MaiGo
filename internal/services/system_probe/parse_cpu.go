package system_probe

import (
	"errors"
	"strconv"
	"strings"
)

type parsedProcStat struct {
	Aggregate cpuSample
	Stat      CPUStat
	Cores     []CPUCore
	BTime     *float64
}

// parseProcStat 解析 /proc/stat 的聚合行、cpuN、ctxt/processes/btime。
func parseProcStat(data []byte) (*parsedProcStat, error) {
	out := &parsedProcStat{}
	seenAggregate := false
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		switch {
		case name == "cpu":
			sample, ok := parseCPUTicks(fields[1:])
			if !ok {
				continue
			}
			out.Aggregate = sample
			out.Stat = CPUStat{
				UserTicks: sample.User, NiceTicks: sample.Nice, SystemTicks: sample.System,
				IdleTicks: sample.Idle, IOWaitTicks: sample.IOWait, IRQTicks: sample.IRQ,
				SoftIRQTicks: sample.SoftIRQ, StealTicks: sample.Steal,
				GuestTicks: sample.Guest, GuestNiceTicks: sample.GuestNice,
			}
			seenAggregate = true
		case strings.HasPrefix(name, "cpu") && isNumericSuffix(name[3:]):
			coreIndex, err := strconv.Atoi(name[3:])
			if err != nil || coreIndex < 0 {
				continue
			}
			sample, ok := parseCPUTicks(fields[1:])
			if !ok {
				continue
			}
			out.Cores = append(out.Cores, CPUCore{
				Core: coreIndex, UserTicks: sample.User, NiceTicks: sample.Nice,
				SystemTicks: sample.System, IdleTicks: sample.Idle, IOWaitTicks: sample.IOWait,
				IRQTicks: sample.IRQ, SoftIRQTicks: sample.SoftIRQ, StealTicks: sample.Steal,
				GuestTicks: sample.Guest, GuestNiceTicks: sample.GuestNice,
			})
		case name == "ctxt":
			out.Stat.Ctxt = parseUint(fields, 1)
		case name == "processes":
			out.Stat.Processes = parseUint(fields, 1)
		case name == "procs_running":
			out.Stat.ProcsRunning = parseUint(fields, 1)
		case name == "procs_blocked":
			out.Stat.ProcsBlocked = parseUint(fields, 1)
		case name == "btime":
			if len(fields) > 1 {
				if value, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
					boot := float64(value)
					out.BTime = &boot
				}
			}
		}
	}
	if !seenAggregate {
		return nil, errors.New("缺少 cpu 聚合行")
	}
	return out, nil
}

func parseCPUTicks(fields []string) (cpuSample, bool) {
	if len(fields) < 4 {
		return cpuSample{}, false
	}
	values := make([]uint64, 0, 10)
	for i := 0; i < 10; i++ {
		if i >= len(fields) {
			values = append(values, 0)
			continue
		}
		value, err := strconv.ParseUint(fields[i], 10, 64)
		if err != nil {
			return cpuSample{}, false
		}
		values = append(values, value)
	}
	sample := cpuSample{User: values[0], Nice: values[1], System: values[2], Idle: values[3], IOWait: values[4]}
	if len(values) > 5 {
		sample.IRQ = values[5]
	}
	if len(values) > 6 {
		sample.SoftIRQ = values[6]
	}
	if len(values) > 7 {
		sample.Steal = values[7]
	}
	if len(values) > 8 {
		sample.Guest = values[8]
	}
	if len(values) > 9 {
		sample.GuestNice = values[9]
	}
	return sample, true
}

func parseUint(fields []string, index int) uint64 {
	if index < 0 || index >= len(fields) {
		return 0
	}
	value, err := strconv.ParseUint(fields[index], 10, 64)
	if err != nil {
		return 0
	}
	return value
}

func isNumericSuffix(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseLoadavg 解析 /proc/loadavg，缺失字段保持零值。
func parseLoadavg(data []byte) CPULoad {
	var load CPULoad
	fields := strings.Fields(string(data))
	if len(fields) >= 3 {
		load.Load1 = parseFloatPtr(fields[0])
		load.Load5 = parseFloatPtr(fields[1])
		load.Load15 = parseFloatPtr(fields[2])
	}
	if len(fields) >= 4 {
		parts := strings.SplitN(fields[3], "/", 2)
		if len(parts) == 2 {
			if value, err := strconv.ParseInt(parts[0], 10, 64); err == nil {
				load.Runnable = value
			}
			if value, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
				load.TotalProcesses = value
			}
		}
	}
	if len(fields) >= 5 {
		if value, err := strconv.ParseInt(fields[4], 10, 64); err == nil {
			load.LastPID = value
		}
	}
	return load
}

func parseFloatPtr(value string) *float64 {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return nil
	}
	return &parsed
}

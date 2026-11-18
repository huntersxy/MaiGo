package system_probe

import (
	"strconv"
	"strings"
)

// parseMeminfo 按字段名解析 /proc/meminfo；kB 字段统一乘 1024。
func parseMeminfo(data []byte) MemorySystem {
	var out MemorySystem
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := meminfoEntry(line)
		if !ok {
			continue
		}
		switch key {
		case "MemTotal":
			out.MemTotalBytes = value
		case "MemFree":
			out.MemFreeBytes = value
		case "MemAvailable":
			out.MemAvailableBytes = value
		case "Buffers":
			out.BuffersBytes = value
		case "Cached":
			out.CachedBytes = value
		case "SReclaimable":
			out.SReclaimableBytes = value
		case "Shmem":
			out.ShmemBytes = value
		case "AnonPages":
			out.AnonPagesBytes = value
		case "Mapped":
			out.MappedBytes = value
		case "PageTables":
			out.PageTablesBytes = value
		case "Slab":
			out.SlabBytes = value
		case "Dirty":
			out.DirtyBytes = value
		case "Writeback":
			out.WritebackBytes = value
		case "SwapTotal":
			out.SwapTotalBytes = value
		case "SwapFree":
			out.SwapFreeBytes = value
		case "SwapCached":
			out.SwapCachedBytes = value
		case "CommitLimit":
			out.CommitLimitBytes = value
		case "Committed_AS":
			out.CommittedASBytes = value
		case "HugePages_Total":
			out.HugePagesTotal = value
		case "HugePages_Free":
			out.HugePagesFree = value
		case "Hugepagesize":
			out.HugePagesSizeBytes = value
		case "VmallocUsed":
			out.VmallocUsedBytes = value
		}
	}
	return out
}

func meminfoEntry(line string) (string, int64, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", 0, false
	}
	key := strings.TrimSuffix(fields[0], ":")
	number, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || number < 0 {
		return "", 0, false
	}
	bytes := number
	if len(fields) >= 3 && strings.EqualFold(fields[2], "kB") {
		bytes = number * 1024
	}
	return key, bytes, true
}

// calculateMemoryUsage 以 MemTotal 为分母；总内存未知时返回两个 null。
func calculateMemoryUsage(system MemorySystem) MemoryUsage {
	if system.MemTotalBytes <= 0 {
		return MemoryUsage{}
	}
	available := system.MemAvailableBytes
	if available < 0 {
		available = 0
	}
	if available > system.MemTotalBytes {
		available = system.MemTotalBytes
	}
	usedPercent := float64(system.MemTotalBytes-available) / float64(system.MemTotalBytes) * 100
	availablePercent := float64(available) / float64(system.MemTotalBytes) * 100
	return MemoryUsage{UsedPercent: &usedPercent, AvailablePercent: &availablePercent}
}

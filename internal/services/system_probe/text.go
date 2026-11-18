package system_probe

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// sanitizeText 过滤无效 UTF-8 与控制字符，并按字节数截断。
func sanitizeText(value string, maxBytes int) string {
	value = strings.ToValidUTF8(value, " ")
	var builder strings.Builder
	for _, r := range value {
		if r == '\n' || r == '\r' || r == '\t' || (r < 0x20) || (r >= 0x7f && r <= 0x9f) {
			builder.WriteByte(' ')
			continue
		}
		builder.WriteRune(r)
	}
	result, _ := truncateUTF8Bytes(builder.String(), maxBytes)
	return result
}

func truncateUTF8Bytes(value string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value, false
	}
	cut := value[:maxBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut, true
}

// RenderText 把同一份 Snapshot 渲染为固定段序的纯文本。
func RenderText(snapshot *Snapshot) []byte {
	var builder strings.Builder
	truncated := false
	write := func(format string, args ...any) bool {
		line := fmt.Sprintf(format, args...)
		if builder.Len()+len(line)+64 > maxTextBytes {
			truncated = true
			return false
		}
		builder.WriteString(line)
		return true
	}
	header := func(name string) bool {
		return write("[%s]\n", name)
	}
	field := func(key string, format string, args ...any) bool {
		return write("%s=%s\n", key, fmt.Sprintf(format, args...))
	}
	ptrFloat := func(value *float64) string {
		if value == nil {
			return "null"
		}
		return fmt.Sprintf("%.2f", *value)
	}
	ptrUint := func(value *uint64) string {
		if value == nil {
			return "null"
		}
		return fmt.Sprintf("%d", *value)
	}

	write("MaiGo system resource probe\n")
	header("meta")
	if snapshot == nil {
		write("unsupported\n")
		return []byte(builder.String())
	}
	field("schema", "%d", snapshot.Meta.Schema)
	field("generated_at", "%s", sanitizeText(snapshot.Meta.GeneratedAt, 64))
	field("elapsed_ms", "%d", snapshot.Meta.ElapsedMS)
	field("timeout_ms", "%d", snapshot.Meta.TimeoutMS)
	field("window_sec", "%s", ptrFloat(snapshot.Meta.WindowSec))
	field("hostname", "%s", sanitizeText(snapshot.Meta.Hostname, 128))
	field("os", "%s", snapshot.Meta.OS)
	field("arch", "%s", snapshot.Meta.Arch)
	field("go_version", "%s", sanitizeText(snapshot.Meta.GoVersion, 64))
	field("pid", "%d", snapshot.Meta.PID)
	field("instance_dir", "%s", sanitizeText(snapshot.Meta.InstanceDir, 256))
	field("version", "%s", sanitizeText(snapshot.Meta.Version, 64))
	field("uptime_sec", "%s", ptrFloat(snapshot.Meta.UptimeSec))
	field("partial", "%t", snapshot.Meta.Partial)

	header("cpu")
	if snapshot.CPU == nil {
		write("unsupported\n")
	} else {
		cpu := snapshot.CPU
		field("logical_cores", "%d", cpu.LogicalCores)
		field("gomaxprocs", "%d", cpu.GOMAXPROCS)
		field("load1", "%s", ptrFloat(cpu.Load.Load1))
		field("load5", "%s", ptrFloat(cpu.Load.Load5))
		field("load15", "%s", ptrFloat(cpu.Load.Load15))
		field("user_ticks", "%d", cpu.Stat.UserTicks)
		field("system_ticks", "%d", cpu.Stat.SystemTicks)
		field("idle_ticks", "%d", cpu.Stat.IdleTicks)
		field("iowait_ticks", "%d", cpu.Stat.IOWaitTicks)
		field("usage_total_percent", "%s", ptrFloat(cpu.Usage.TotalPercent))
		for _, core := range cpu.PerCore {
			if !write("core=%d user_ticks=%d system_ticks=%d idle_ticks=%d\n",
				core.Core, core.UserTicks, core.SystemTicks, core.IdleTicks) {
				break
			}
		}
	}

	header("memory")
	if snapshot.Memory == nil {
		write("unsupported\n")
	} else {
		memory := snapshot.Memory
		field("mem_total_bytes", "%d", memory.System.MemTotalBytes)
		field("mem_free_bytes", "%d", memory.System.MemFreeBytes)
		field("mem_available_bytes", "%d", memory.System.MemAvailableBytes)
		field("used_percent", "%s", ptrFloat(memory.Usage.UsedPercent))
		field("process_rss_bytes", "%d", memory.Process.RSSBytes)
		field("process_virtual_bytes", "%d", memory.Process.VirtualBytes)
		field("process_threads", "%d", memory.Process.Threads)
	}

	header("disk")
	if snapshot.Disk == nil {
		write("unsupported\n")
	} else {
		for _, mount := range snapshot.Disk.Mounts {
			if !write("mount=%s path=%s total_bytes=%d available_bytes=%d used_percent=%s\n",
				mount.Label, sanitizeText(mount.Path, 256), mount.TotalBytes,
				mount.AvailableBytes, ptrFloat(mount.UsedPercent)) {
				break
			}
		}
		for _, device := range snapshot.Disk.Devices {
			if !write("device=%s read_bytes=%d write_bytes=%d\n",
				sanitizeText(device.Name, 64), device.ReadBytes, device.WriteBytes) {
				break
			}
		}
	}

	header("network")
	if snapshot.Network == nil {
		write("unsupported\n")
	} else {
		for _, iface := range snapshot.Network.Interfaces {
			if !write("interface=%s rx_bytes=%d tx_bytes=%d\n",
				sanitizeText(iface.Name, 64), iface.RXBytes, iface.TXBytes) {
				break
			}
		}
		field("tcp_established", "%d", snapshot.Network.TCPStates.Established)
		field("tcp_listen", "%d", snapshot.Network.TCPStates.Listen)
		field("tcp_scanned", "%d", snapshot.Network.TCPScanned)
	}

	header("processes")
	if snapshot.Processes == nil {
		write("unsupported\n")
	} else {
		processes := snapshot.Processes
		field("limit", "%d", processes.Limit)
		field("scanned", "%d", processes.Scanned)
		field("open_fds", "%d", processes.OpenFDs)
		field("nofile_soft", "%s", ptrUint(processes.NofileSoft))
		for _, process := range processes.List {
			if !write("pid=%d name=%s rss_bytes=%d threads=%d cmdline=%s\n",
				process.PID, sanitizeText(process.Name, procNameBytes), process.RSSBytes,
				process.Threads, sanitizeText(process.Cmdline, procCmdlineBytes)) {
				break
			}
		}
	}

	header("runtime")
	if snapshot.Runtime == nil {
		write("unsupported\n")
	} else {
		runtimeSection := snapshot.Runtime
		field("goroutines", "%d", runtimeSection.Goroutines)
		field("gomaxprocs", "%d", runtimeSection.GOMAXPROCS)
		field("heap_alloc_bytes", "%d", runtimeSection.Memstats.HeapAllocBytes)
		field("heap_objects", "%d", runtimeSection.Memstats.HeapObjects)
		field("gc_cycles", "%d", runtimeSection.Memstats.NumGC)
		field("gc_pause_p95_ns", "%s", ptrFloat(runtimeSection.GC.PauseP95NS))
		field("gc_pause_max_ns", "%s", ptrFloat(runtimeSection.GC.PauseMaxNS))
		field("gogc_percent", "%s", ptrUint(runtimeSection.GC.GOGCPercent))
	}

	header("app")
	if snapshot.App == nil {
		write("unsupported\n")
	} else {
		app := snapshot.App
		field("version", "%s", sanitizeText(app.Version, 64))
		field("db_path", "%s", sanitizeText(app.DB.Path, 256))
		field("db_in_use", "%d", app.DB.Pool.InUse)
		field("counts_inbound", "%d", app.Counts.Inbound)
		field("counts_outbound", "%d", app.Counts.Outbound)
		field("counts_pending_chats", "%d", app.Counts.PendingChats)
		field("scheduler_available", "%t", app.Scheduler.Available)
		field("scheduler_running_tasks", "%d", app.Scheduler.RunningTasks)
		for _, subprocess := range app.Subprocesses {
			if !write("subprocess kind=%s group=%s pid=%d alive=%t phase=%s\n",
				sanitizeText(subprocess.Kind, 32), sanitizeText(subprocess.Group, 32),
				subprocess.PID, subprocess.Alive, sanitizeText(subprocess.Phase, 32)) {
				break
			}
		}
	}

	header("cgroup")
	if snapshot.Cgroup == nil {
		write("unsupported\n")
	} else {
		field("version", "%s", snapshot.Cgroup.Version)
		field("path", "%s", sanitizeText(snapshot.Cgroup.Path, 256))
		field("available", "%t", snapshot.Cgroup.Available)
		field("partial", "%t", snapshot.Cgroup.Partial)
		if snapshot.Cgroup.CPU != nil {
			field("cpu_usage_usec", "%d", snapshot.Cgroup.CPU.UsageUSec)
			field("cpu_throttled_usec", "%d", snapshot.Cgroup.CPU.ThrottledUSec)
		}
		if snapshot.Cgroup.Memory != nil {
			field("memory_current_bytes", "%d", snapshot.Cgroup.Memory.CurrentBytes)
		}
	}

	header("psi")
	if snapshot.PSI == nil {
		write("unsupported\n")
	} else {
		writePSI(&builder, "cpu", snapshot.PSI.CPU, write)
		writePSI(&builder, "memory", snapshot.PSI.Memory, write)
		writePSI(&builder, "io", snapshot.PSI.IO, write)
	}

	header("history")
	field("capacity", "%d", snapshot.History.Capacity)
	for _, sample := range snapshot.History.Samples {
		if !write("timestamp_unix=%d cpu_total_percent=%s goroutines=%d\n",
			sample.TimestampUnix, ptrFloat(sample.CPUTotalPercent), sample.Goroutines) {
			break
		}
	}

	header("errors")
	for _, err := range snapshot.Errors {
		if !write("section=%s path=%s skipped=%t error=%s\n", sanitizeText(err.Section, 32),
			sanitizeText(err.Path, 256), err.Skipped, sanitizeText(err.Error, 256)) {
			break
		}
	}
	if truncated {
		builder.WriteString("[truncated]\ntruncated=true\n")
	}
	return []byte(builder.String())
}

func writePSI(builder *strings.Builder, name string, resource *PSIResource, write func(string, ...any) bool) {
	if resource == nil {
		write("psi_%s=unsupported\n", name)
		return
	}
	for _, scope := range []struct {
		label string
		pair  *PSIStatePair
	}{{"system", resource.System}, {"cgroup", resource.Cgroup}} {
		if scope.pair == nil || scope.pair.Some == nil {
			continue
		}
		state := scope.pair.Some
		avg10 := "null"
		if state.Avg10 != nil {
			avg10 = fmt.Sprintf("%.2f", *state.Avg10)
		}
		write("psi_%s_%s_some_avg10=%s total_us=%d\n", name, scope.label, avg10, state.TotalUS)
	}
}

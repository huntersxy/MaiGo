package system_probe

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
)

type parsedProcessStat struct {
	PID            int
	Name           string
	State          string
	PPID           int
	UserTicks      uint64
	SystemTicks    uint64
	Threads        int
	StartTimeTicks uint64
	VirtualBytes   int64
	RSSPages       int64
}

// parseProcStatFile 解析 /proc/<pid>/stat；comm 用最后一个右括号切分。
func parseProcStatFile(data []byte) (parsedProcessStat, error) {
	var out parsedProcessStat
	open := bytes.IndexByte(data, '(')
	close := bytes.LastIndexByte(data, ')')
	if open < 0 || close <= open {
		return out, errors.New("stat 缺少 comm 字段")
	}
	pidField := strings.TrimSpace(string(data[:open]))
	pid, err := strconv.Atoi(pidField)
	if err != nil {
		return out, errors.New("stat pid 非法")
	}
	out.PID = pid
	out.Name = sanitizeText(string(data[open+1:close]), procNameBytes)
	fields := strings.Fields(string(data[close+1:]))
	if len(fields) > 0 {
		out.State = fields[0]
	}
	if len(fields) > 1 {
		out.PPID, _ = strconv.Atoi(fields[1])
	}
	if len(fields) > 12 {
		out.UserTicks = parseUint(fields, 11)
		out.SystemTicks = parseUint(fields, 12)
	}
	if len(fields) > 17 {
		out.Threads, _ = strconv.Atoi(fields[17])
	}
	if len(fields) > 19 {
		out.StartTimeTicks = parseUint(fields, 19)
	}
	if len(fields) > 20 {
		out.VirtualBytes, _ = strconv.ParseInt(fields[20], 10, 64)
	}
	if len(fields) > 21 {
		out.RSSPages, _ = strconv.ParseInt(fields[21], 10, 64)
	}
	return out, nil
}

type parsedProcStatus struct {
	Memory  ProcessMemory
	UIDs    []int
	Threads int
}

// parseProcStatus 解析 /proc/<pid>/status 的内存与 Uid 字段；畸形行跳过。
func parseProcStatus(data []byte) parsedProcStatus {
	var out parsedProcStatus
	for _, line := range strings.Split(string(data), "\n") {
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		key := line[:colon]
		fields := strings.Fields(line[colon+1:])
		if len(fields) == 0 {
			continue
		}
		if key == "Uid" {
			for _, field := range fields {
				if value, err := strconv.Atoi(field); err == nil {
					out.UIDs = append(out.UIDs, value)
				}
			}
			continue
		}
		if key == "Threads" {
			if value, err := strconv.Atoi(fields[0]); err == nil {
				out.Threads = value
				out.Memory.Threads = value
			}
			continue
		}
		if len(fields) < 2 || fields[1] != "kB" {
			continue
		}
		value, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || value < 0 {
			continue
		}
		bytes := value * 1024
		switch key {
		case "VmRSS":
			out.Memory.RSSBytes = bytes
		case "VmHWM":
			out.Memory.PeakRSSBytes = bytes
		case "VmSize":
			out.Memory.VirtualBytes = bytes
		case "VmPeak":
			out.Memory.PeakVirtualBytes = bytes
		case "VmSwap":
			out.Memory.SwapBytes = bytes
		case "RssAnon":
			out.Memory.AnonymousRSSBytes = bytes
		case "RssFile":
			out.Memory.FileRSSBytes = bytes
		case "RssShmem":
			out.Memory.SharedMemoryRSSBytes = bytes
		case "VmPTE":
			out.Memory.PageTableBytes = bytes
		}
	}
	return out
}

// parseCmdline 把 NUL 分隔的参数拼成单行；返回值已过滤控制字符并截断到 128B。
func parseCmdline(data []byte) (string, bool) {
	truncated := len(data) >= limitProcCmdline
	parts := bytes.Split(data, []byte{0})
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		values = append(values, string(part))
	}
	joined := maskCmdline(strings.Join(values, " "))
	sanitized, cut := truncateUTF8Bytes(sanitizeText(joined, procCmdlineBytes), procCmdlineBytes)
	return sanitized, truncated || cut
}

// maskCmdline 对常见凭据参数做掩码，避免诊断快照泄露 token/DSN。
func maskCmdline(value string) string {
	fields := strings.Fields(value)
	for i := 0; i < len(fields); i++ {
		key, _, hasValue := strings.Cut(fields[i], "=")
		if !sensitiveCommandKey(key) {
			continue
		}
		if hasValue {
			fields[i] = key + "=<redacted>"
			continue
		}
		for j := i + 1; j < len(fields) && !strings.HasPrefix(fields[j], "-"); j++ {
			fields[j] = "<redacted>"
		}
	}
	return strings.Join(fields, " ")
}

func sensitiveCommandKey(key string) bool {
	normalized := strings.ToLower(strings.TrimLeft(strings.TrimSpace(key), "-"))
	if normalized == "" {
		return false
	}
	switch normalized {
	case "token", "api-key", "api_key", "apikey", "access-token", "access_token",
		"auth-token", "auth_token", "dsn", "database-dsn", "database_dsn", "password",
		"passwd", "secret", "client-secret", "client_secret", "key", "p":
		return true
	}
	for _, suffix := range []string{"_token", "-token", "_key", "-key", "_secret", "-secret", "_password", "_passwd", "_dsn", "-dsn"} {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

// processCandidate 是 uid 过滤前的进程记录。
type processCandidate struct {
	PID      int
	UID      int
	Required bool
	Info     ProcessInfo
}

// filterProcessesByUID 只保留 euid 匹配或必须入选的进程。
func filterProcessesByUID(candidates []processCandidate, euid int) []processCandidate {
	out := make([]processCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Required || candidate.UID == euid {
			out = append(out, candidate)
		}
	}
	return out
}

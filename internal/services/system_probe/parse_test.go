package system_probe

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读取夹具 %s 失败: %v", name, err)
	}
	return raw
}

func assertFloat(t *testing.T, value *float64, want float64) {
	t.Helper()
	if value == nil {
		t.Fatalf("期望浮点值 %v，实际为 nil", want)
	}
	if *value != want {
		t.Fatalf("期望浮点值 %v，实际 %v", want, *value)
	}
}

func TestParseProcStat(t *testing.T) {
	parsed, err := parseProcStat(readTestdata(t, "proc_stat.txt"))
	if err != nil {
		t.Fatalf("解析 /proc/stat 失败: %v", err)
	}
	if parsed.Aggregate.User != 100 || parsed.Aggregate.Idle != 800 || parsed.Aggregate.Guest != 3 {
		t.Fatalf("聚合 tick 不符: %+v", parsed.Aggregate)
	}
	if len(parsed.Cores) != 2 || parsed.Cores[1].Core != 1 || parsed.Cores[1].SystemTicks != 20 {
		t.Fatalf("cpuN 解析不符: %+v", parsed.Cores)
	}
	if parsed.Stat.Ctxt != 123456 || parsed.Stat.ProcsBlocked != 1 {
		t.Fatalf("stat 计数不符: %+v", parsed.Stat)
	}
	if parsed.BTime == nil || *parsed.BTime != 1700000000 {
		t.Fatalf("btime 不符: %v", parsed.BTime)
	}
}

func TestParseProcStatMalformed(t *testing.T) {
	if _, err := parseProcStat([]byte("cpuX nope\n")); err == nil {
		t.Fatal("缺少聚合行时应返回错误")
	}
}

func TestParseLoadavg(t *testing.T) {
	load := parseLoadavg(readTestdata(t, "proc_loadavg.txt"))
	assertFloat(t, load.Load1, 0.5)
	assertFloat(t, load.Load15, 0.1)
	if load.Runnable != 3 || load.TotalProcesses != 456 || load.LastPID != 7890 {
		t.Fatalf("loadavg 计数不符: %+v", load)
	}
}

func TestParseMeminfo(t *testing.T) {
	system := parseMeminfo(readTestdata(t, "proc_meminfo.txt"))
	if system.MemTotalBytes != 16384000*1024 || system.MemAvailableBytes != 8000000*1024 {
		t.Fatalf("内存字节换算不符: %+v", system)
	}
	if system.HugePagesTotal != 0 || system.HugePagesSizeBytes != 2048*1024 {
		t.Fatalf("HugePages 字段不符: %+v", system)
	}
	usage := calculateMemoryUsage(system)
	if usage.UsedPercent == nil || usage.AvailablePercent == nil {
		t.Fatal("MemTotal>0 时比率不应为 nil")
	}
	if *usage.UsedPercent+*usage.AvailablePercent < 99.99 || *usage.UsedPercent+*usage.AvailablePercent > 100.01 {
		t.Fatalf("内存占比之和异常: used=%v available=%v", *usage.UsedPercent, *usage.AvailablePercent)
	}
}

func TestParseMeminfoZeroTotal(t *testing.T) {
	system := parseMeminfo(readTestdata(t, "proc_meminfo_zero_total.txt"))
	usage := calculateMemoryUsage(system)
	if usage.UsedPercent != nil || usage.AvailablePercent != nil {
		t.Fatalf("MemTotal=0 时比率应为 nil: %+v", usage)
	}
}

func TestParseDiskstats(t *testing.T) {
	devices := parseDiskstats(readTestdata(t, "proc_diskstats.txt"))
	if len(devices) != 2 {
		t.Fatalf("应过滤 loop 设备并跳过畸形行，实际 %d 项", len(devices))
	}
	if devices[0].Name != "sda" || devices[0].ReadBytes != 20000*512 || devices[0].WriteBytes != 40000*512 {
		t.Fatalf("sda 扇区换算不符: %+v", devices[0])
	}
	if devices[0].ReadTimeMS != 500 || devices[0].IOInProgress != 2 || devices[0].WeightedIOTimeMS != 1500 {
		t.Fatalf("sda 时间字段不符: %+v", devices[0])
	}
	if devices[1].Name != "nvme0n1" {
		t.Fatalf("第二块设备应为 nvme0n1: %+v", devices[1])
	}
}

func TestParseNetDev(t *testing.T) {
	interfaces := parseNetDev(readTestdata(t, "proc_net_dev.txt"))
	if len(interfaces) != 2 {
		t.Fatalf("应跳过表头，实际 %d 个接口", len(interfaces))
	}
	if interfaces[1].Name != "eth0" || interfaces[1].RXBytes != 5000 || interfaces[1].TXCarrier != 11 {
		t.Fatalf("eth0 字段不符: %+v", interfaces[1])
	}
}

func TestParseSockstat(t *testing.T) {
	var sockstat Sockstat
	parseSockstat(readTestdata(t, "proc_net_sockstat.txt"), &sockstat)
	if sockstat.TCPInUse != 10 || sockstat.TCPTW != 3 || sockstat.UDPInUse != 6 || sockstat.RawInUse != 1 {
		t.Fatalf("sockstat 计数不符: %+v", sockstat)
	}
	if sockstat.TCPMemPages != 4 || sockstat.TCPMemBytes != 4*int64(os.Getpagesize()) {
		t.Fatalf("sockstat mem 换算不符: %+v", sockstat)
	}
}

func TestCountTCPStates(t *testing.T) {
	raw := readTestdata(t, "proc_net_tcp.txt")
	states, scanned, truncated, err := countTCPStates(context.Background(), strings.NewReader(string(raw)), 100)
	if err != nil {
		t.Fatalf("TCP 扫描失败: %v", err)
	}
	if truncated || scanned != 4 {
		t.Fatalf("TCP 扫描计数不符: scanned=%d truncated=%v", scanned, truncated)
	}
	if states.Listen != 1 || states.Established != 1 || states.TimeWait != 1 || states.Unknown != 1 {
		t.Fatalf("TCP 状态计数不符: %+v", states)
	}
	states, scanned, truncated, err = countTCPStates(context.Background(), strings.NewReader(string(raw)), 2)
	if err != nil || !truncated || scanned != 2 {
		t.Fatalf("行数截断不符: scanned=%d truncated=%v err=%v", scanned, truncated, err)
	}
	if states.Listen != 1 || states.Established != 1 || states.TimeWait != 0 {
		t.Fatalf("截断后状态计数不符: %+v", states)
	}
}

func TestParseProcStatFileCommWithParens(t *testing.T) {
	stat, err := parseProcStatFile(readTestdata(t, "proc_pid_stat.txt"))
	if err != nil {
		t.Fatalf("解析进程 stat 失败: %v", err)
	}
	if stat.PID != 1234 || stat.Name != "my proc) (x)" {
		t.Fatalf("comm 应保留右括号: pid=%d name=%q", stat.PID, stat.Name)
	}
	if stat.UserTicks != 12 || stat.SystemTicks != 34 || stat.Threads != 7 {
		t.Fatalf("进程 tick/线程不符: %+v", stat)
	}
	if stat.StartTimeTicks != 5000 || stat.VirtualBytes != 123456789 || stat.RSSPages != 256 {
		t.Fatalf("进程时间/内存字段不符: %+v", stat)
	}
}

func TestParseProcStatus(t *testing.T) {
	status := parseProcStatus(readTestdata(t, "proc_pid_status.txt"))
	if len(status.UIDs) != 4 || status.UIDs[0] != 1000 {
		t.Fatalf("Uid 多行解析不符: %+v", status.UIDs)
	}
	if status.Memory.RSSBytes != 40000*1024 || status.Memory.PeakRSSBytes != 50000*1024 {
		t.Fatalf("VmRSS/VmHWM 换算不符: %+v", status.Memory)
	}
	if status.Memory.AnonymousRSSBytes != 30000*1024 || status.Memory.SharedMemoryRSSBytes != 2000*1024 {
		t.Fatalf("RssAnon/RssShmem 换算不符: %+v", status.Memory)
	}
	if status.Threads != 7 || status.Memory.Threads != 7 {
		t.Fatalf("Threads 解析不符: %+v", status)
	}
}

func TestParseCmdline(t *testing.T) {
	value, truncated := parseCmdline([]byte("python\x00script.py\x00--token\x00secret"))
	if truncated || value != "python script.py --token <redacted>" {
		t.Fatalf("cmdline 拼接/脱敏不符: %q truncated=%v", value, truncated)
	}
	if strings.Contains(value, "secret") {
		t.Fatalf("cmdline 泄露 token: %q", value)
	}
	value, _ = parseCmdline([]byte("run\x00--api-key=abc\x00DSN=secret\x00-p\x001234"))
	for _, secret := range []string{"abc", "secret", "1234"} {
		if strings.Contains(value, secret) {
			t.Fatalf("敏感参数未脱敏 %q: %q", secret, value)
		}
	}
	value, truncated = parseCmdline([]byte(strings.Repeat("a", 300)))
	if !truncated || len(value) > procCmdlineBytes {
		t.Fatalf("cmdline 应截断到 128B: len=%d truncated=%v", len(value), truncated)
	}
	value, _ = parseCmdline([]byte("a\nb\tc\x00d"))
	if strings.ContainsAny(value, "\n\t") {
		t.Fatalf("cmdline 控制字符未过滤: %q", value)
	}
}

func TestParsePSI(t *testing.T) {
	pair := parsePSI(readTestdata(t, "proc_pressure_cpu.txt"))
	if pair == nil || pair.Some == nil || pair.Full == nil {
		t.Fatalf("PSI some/full 缺失: %+v", pair)
	}
	assertFloat(t, pair.Some.Avg10, 1.5)
	assertFloat(t, pair.Full.Avg300, 0.1)
	if pair.Some.TotalUS != 12345 || pair.Full.TotalUS != 6789 {
		t.Fatalf("PSI total 不符: %+v", pair)
	}
	if parsePSI(nil) != nil || parsePSI([]byte("garbage")) != nil {
		t.Fatal("缺失 PSI 应返回 nil")
	}
}

func TestParseCgroupV2(t *testing.T) {
	cpu := parseCgroupV2CPUStat(readTestdata(t, "cgroup_cpu_stat_v2.txt"))
	if cpu.UsageUSec != 1234567 || cpu.NrThrottled != 5 || cpu.ThrottledUSec != 2500 {
		t.Fatalf("v2 cpu.stat 不符: %+v", cpu)
	}
	assertFloat(t, cpu.ThrottledPercent, 5)
	parseCgroupV2CPUMax(readTestdata(t, "cgroup_cpu_max.txt"), &cpu)
	if cpu.QuotaUSec == nil || *cpu.QuotaUSec != 50000 || cpu.PeriodUSec == nil || *cpu.PeriodUSec != 100000 {
		t.Fatalf("v2 cpu.max 不符: %+v", cpu)
	}
	assertFloat(t, cpu.CPUs, 0.5)
	unlimited := CgroupCPU{}
	parseCgroupV2CPUMax(readTestdata(t, "cgroup_cpu_max_unlimited.txt"), &unlimited)
	if !unlimited.Unlimited || unlimited.QuotaUSec != nil || unlimited.CPUs != nil {
		t.Fatalf("v2 cpu.max=max 不符: %+v", unlimited)
	}
	if parseUintBytes(readTestdata(t, "cgroup_memory_current.txt")) != 1048576 {
		t.Fatal("v2 memory.current 解析失败")
	}
	if parseCgroupV2MemoryLimit(readTestdata(t, "cgroup_memory_max_unlimited.txt")) != nil {
		t.Fatal("memory.max=max 应为 nil")
	}
	events := parseCgroupMemoryEvents(readTestdata(t, "cgroup_memory_events.txt"))
	if events.OOMKill != 5 || events.OOMGroupKill != 6 {
		t.Fatalf("memory.events 不符: %+v", events)
	}
	stat := parseCgroupMemoryStat(readTestdata(t, "cgroup_memory_stat.txt"))
	if stat.AnonBytes != 100 || stat.WorkingsetRefault != 5 {
		t.Fatalf("memory.stat 不符: %+v", stat)
	}
}

func TestParseCgroupV1(t *testing.T) {
	cpu := parseCgroupV1CPUStat(readTestdata(t, "cgroup_v1_cpu_stat.txt"))
	if cpu.NrPeriods != 200 || cpu.NrThrottled != 10 || cpu.ThrottledUSec != 5000 {
		t.Fatalf("v1 cpu.stat 纳秒换算不符: %+v", cpu)
	}
	assertFloat(t, cpu.ThrottledPercent, 5)
	parseCgroupV1CFSQuota(readTestdata(t, "cgroup_v1_cfs_quota_minus1.txt"), &cpu)
	if !cpu.Unlimited || cpu.QuotaUSec != nil {
		t.Fatalf("v1 quota -1 应视为不限: %+v", cpu)
	}
	parseCgroupV1CFSPeriod(readTestdata(t, "cgroup_v1_cfs_period.txt"), &cpu)
	if cpu.PeriodUSec == nil || *cpu.PeriodUSec != 100000 {
		t.Fatalf("v1 period 不符: %+v", cpu)
	}
	parseCgroupV1CPUAcctUsage(readTestdata(t, "cgroup_v1_cpuacct_usage.txt"), &cpu)
	if cpu.UsageUSec != 1234567 {
		t.Fatalf("v1 cpuacct.usage 换算不符: %+v", cpu)
	}
	if parseUintBytes(readTestdata(t, "cgroup_v1_memory_usage_in_bytes.txt")) != 1048576 {
		t.Fatal("v1 memory.usage_in_bytes 解析失败")
	}
	if value := parseCgroupV1MemoryLimit([]byte("9223372036854771712")); value != nil {
		t.Fatalf("v1 内存上限哨兵应为 nil: %v", *value)
	}
	stat := parseCgroupMemoryStat(readTestdata(t, "cgroup_v1_memory_stat.txt"))
	if stat.FileBytes != 123 || stat.AnonBytes != 456 {
		t.Fatalf("v1 memory.stat 不符: %+v", stat)
	}
	if parseCgroupPidsCurrent(readTestdata(t, "cgroup_v1_pids.txt")) != 12 {
		t.Fatal("pids.current 解析失败")
	}
	if parseCgroupPidsMax([]byte("max")) != nil {
		t.Fatal("pids.max=max 应为 nil")
	}
}

func TestParseCgroupVersion(t *testing.T) {
	version, path := parseCgroupVersion([]byte("0::/docker/abc\n"))
	if version != "v2" || path != "/docker/abc" {
		t.Fatalf("cgroup v2 识别失败: %s %s", version, path)
	}
	version, _ = parseCgroupVersion([]byte("4:cpu,cpuacct:/docker/abc\n"))
	if version != "v1" {
		t.Fatalf("cgroup v1 识别失败: %s", version)
	}
}

func TestParseCgroupV1ControllerPaths(t *testing.T) {
	paths := parseCgroupV1ControllerPaths([]byte(
		"5:cpu,cpuacct:/system.slice/maigo.service\n" +
			"4:memory:/system.slice/maigo.service\n" +
			"3:pids:/system.slice/maigo.service\n",
	))
	for _, controller := range []string{"cpu", "cpuacct", "memory", "pids"} {
		if paths[controller] != "/system.slice/maigo.service" {
			t.Fatalf("controller %s 路径不符: %+v", controller, paths)
		}
	}
	if len(parseCgroupV1ControllerPaths([]byte("0::/unified\n"))) != 0 {
		t.Fatal("v2 行不应产生 v1 controller 路径")
	}
}

func TestFilterProcessesByUID(t *testing.T) {
	candidates := []processCandidate{
		{PID: 1, UID: 1000},
		{PID: 2, UID: 0},
		{PID: 3, UID: 0, Required: true},
		{PID: 4, UID: 1001, Required: true},
	}
	filtered := filterProcessesByUID(candidates, 1000)
	if len(filtered) != 3 {
		t.Fatalf("uid 过滤结果数量不符: %+v", filtered)
	}
	for _, candidate := range filtered {
		if candidate.PID == 2 {
			t.Fatal("非 euid 且非必须的进程不应入选")
		}
	}
}

//go:build linux

package system_probe

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type slowReader struct{}

func (slowReader) Open(string) (io.ReadCloser, error) {
	time.Sleep(40 * time.Millisecond)
	return nil, os.ErrNotExist
}

func (slowReader) ReadFile(string, int64) ([]byte, error) {
	time.Sleep(40 * time.Millisecond)
	return nil, os.ErrNotExist
}

func (slowReader) ReadDir(string) ([]string, error) {
	time.Sleep(40 * time.Millisecond)
	return nil, os.ErrNotExist
}

func (slowReader) OwnerUID(string) (int, error) {
	return os.Geteuid(), nil
}

func TestLinuxSlowReaderTimeout(t *testing.T) {
	probe := New(Options{
		InstanceDir: t.TempDir(),
		Timeout:     80 * time.Millisecond,
		Reader:      slowReader{},
	})
	started := time.Now()
	snapshot := probe.Snapshot(context.Background())
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("慢采集应在超时后迅速返回: %v", elapsed)
	}
	if snapshot == nil || !snapshot.Meta.Partial {
		t.Fatal("超时快照应标记 partial")
	}
	timeoutSections := make(map[string]bool)
	for _, item := range snapshot.Errors {
		text := item.Error
		if timeoutSections[item.Section] {
			continue
		}
		if containsAny(text, "timeout", "deadline") {
			timeoutSections[item.Section] = true
		}
	}
	if !timeoutSections["disk"] || !timeoutSections["memory"] {
		t.Fatalf("errors 应包含 disk/memory 超时项: %+v", snapshot.Errors)
	}

	normal := New(Options{InstanceDir: t.TempDir(), Timeout: 2 * time.Second, ScanLimit: 4})
	recovered := normal.Snapshot(context.Background())
	if recovered == nil || recovered.Processes == nil {
		t.Fatal("正常 Reader 应恢复进程采集")
	}
	if recovered.Processes.Scanned > 4 {
		t.Fatalf("ScanLimit 未生效: %d", recovered.Processes.Scanned)
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if len(needle) > 0 && len(value) >= len(needle) {
			for i := 0; i+len(needle) <= len(value); i++ {
				if value[i:i+len(needle)] == needle {
					return true
				}
			}
		}
	}
	return false
}

type fakeProcReader struct {
	files map[string][]byte
	dirs  map[string][]string
	uids  map[string]int
}

func (f *fakeProcReader) Open(string) (io.ReadCloser, error) {
	return nil, os.ErrNotExist
}

func (f *fakeProcReader) ReadFile(path string, _ int64) ([]byte, error) {
	if raw, ok := f.files[path]; ok {
		return raw, nil
	}
	return nil, os.ErrNotExist
}

func (f *fakeProcReader) ReadDir(path string) ([]string, error) {
	if names, ok := f.dirs[path]; ok {
		return names, nil
	}
	return nil, os.ErrNotExist
}

func (f *fakeProcReader) OwnerUID(path string) (int, error) {
	if uid, ok := f.uids[path]; ok {
		return uid, nil
	}
	return os.Geteuid(), nil
}

func fakeProcessStat(pid int, name string) []byte {
	fields := make([]string, 22)
	fields[0] = "S"
	fields[1] = "1"
	fields[11] = "1"
	fields[12] = "1"
	fields[15] = "20"
	fields[17] = "1"
	fields[19] = "1000"
	fields[20] = "4096"
	fields[21] = "1"
	return []byte(fmt.Sprintf("%d (%s) %s\n", pid, name, strings.Join(fields, " ")))
}

func TestCollectProcessesIncludesRequiredUnderLimit(t *testing.T) {
	selfPID := os.Getpid()
	otherPID := selfPID + 10000
	extraPID := selfPID + 15000
	rootPID := selfPID + 20000
	reader := &fakeProcReader{
		files: map[string][]byte{
			"/proc/self/fd":                    {},
			filepathJoinProc(selfPID, "stat"):  fakeProcessStat(selfPID, "self"),
			filepathJoinProc(otherPID, "stat"): fakeProcessStat(otherPID, "other"),
			filepathJoinProc(extraPID, "stat"): fakeProcessStat(extraPID, "extra"),
			filepathJoinProc(rootPID, "stat"):  fakeProcessStat(rootPID, "root"),
		},
		dirs: map[string][]string{
			"/proc":         {strconv.Itoa(rootPID), strconv.Itoa(otherPID), strconv.Itoa(extraPID), strconv.Itoa(selfPID)},
			"/proc/self/fd": {},
		},
		uids: map[string]int{
			fmt.Sprintf("/proc/%d", rootPID): 0,
		},
	}
	probe := New(Options{ProcessLimit: 1, ScanLimit: 1, Reader: reader})
	section, errs := collectProcesses(context.Background(), probe, reader, nil, nil)
	if len(errs) != 0 {
		t.Fatalf("不期望 errors: %+v", errs)
	}
	if section == nil || len(section.List) != 1 || section.List[0].PID != selfPID {
		t.Fatalf("required self 必须入选且不受 limit 影响: %+v", section)
	}
	if section.List[0].Kind != "self" {
		t.Fatalf("self kind 不符: %+v", section.List[0])
	}
	if section.Scanned != 1 || !section.Truncated {
		t.Fatalf("非 euid 进程不应消耗扫描预算: scanned=%d truncated=%v", section.Scanned, section.Truncated)
	}
}

func filepathJoinProc(pid int, name string) string {
	return fmt.Sprintf("/proc/%d/%s", pid, name)
}

func TestConcurrentSnapshotsDoNotSkipStatfs(t *testing.T) {
	probe := New(Options{
		InstanceDir: t.TempDir(),
		Timeout:     2 * time.Second,
		StatfsFn: func(string) (StatfsResult, error) {
			time.Sleep(10 * time.Millisecond)
			return StatfsResult{Blocks: 100, BFree: 40, BAvail: 30, Files: 10, FFree: 5, BlockSize: 4096}, nil
		},
	})
	var group sync.WaitGroup
	results := make(chan *Snapshot, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			results <- probe.Snapshot(context.Background())
		}()
	}
	group.Wait()
	close(results)
	for snapshot := range results {
		if snapshot == nil || snapshot.Disk == nil {
			t.Fatal("并发 Snapshot 缺少 disk section")
		}
		for _, item := range snapshot.Errors {
			if item.Section == "disk" && item.Skipped {
				t.Fatalf("健康限流器不应丢弃 statfs: %+v", item)
			}
		}
	}
}

func TestCollectCgroupV1UsesControllerPaths(t *testing.T) {
	servicePath := "/system.slice/maigo.service"
	cpuDir := "/sys/fs/cgroup/cpu,cpuacct" + servicePath
	memoryDir := "/sys/fs/cgroup/memory" + servicePath
	pidsDir := "/sys/fs/cgroup/pids" + servicePath
	reader := &fakeProcReader{
		files: map[string][]byte{
			cpuDir + "/cpu.stat":                 []byte("nr_periods 10\nnr_throttled 1\nthrottled_time 1000000\n"),
			cpuDir + "/cpu.cfs_quota_us":         []byte("50000\n"),
			cpuDir + "/cpu.cfs_period_us":        []byte("100000\n"),
			cpuDir + "/cpuacct.usage":            []byte("1000000\n"),
			memoryDir + "/memory.usage_in_bytes": []byte("1048576\n"),
			memoryDir + "/memory.limit_in_bytes": []byte("2097152\n"),
			pidsDir + "/pids.current":            []byte("7\n"),
		},
		dirs: map[string][]string{
			cpuDir:    {"cpu.stat"},
			memoryDir: {"memory.usage_in_bytes"},
			pidsDir:   {"pids.current"},
		},
	}
	probe := New(Options{Reader: reader})
	cgroup := &CgroupSection{Version: "v1", Path: servicePath}
	var errs []ProbeError
	collectCgroupV1(context.Background(), probe, reader, cgroup, map[string]string{
		"cpu":     servicePath,
		"cpuacct": servicePath,
		"memory":  servicePath,
		"pids":    servicePath,
	}, &errs)
	if cgroup.CPU == nil || cgroup.CPU.QuotaUSec == nil || *cgroup.CPU.QuotaUSec != 50000 {
		t.Fatalf("应读取 service 路径的 quota 而非挂载根: %+v", cgroup.CPU)
	}
	if cgroup.CPU.UsageUSec != 1000 {
		t.Fatalf("cpuacct.usage 应来自 service 路径: %+v", cgroup.CPU)
	}
	if cgroup.Memory == nil || cgroup.Memory.CurrentBytes != 1048576 {
		t.Fatalf("memory.usage_in_bytes 应来自 service 路径: %+v", cgroup.Memory)
	}
	if cgroup.Pids == nil || cgroup.Pids.Current != 7 {
		t.Fatalf("pids.current 应来自 service 路径: %+v", cgroup.Pids)
	}
}

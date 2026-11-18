//go:build !linux

package system_probe

import (
	"context"
	"testing"
	"time"
)

func TestOtherPlatformDegradesSections(t *testing.T) {
	probe := New(Options{InstanceDir: t.TempDir(), Timeout: 200 * time.Millisecond})
	snapshot := probe.Snapshot(context.Background())
	if snapshot == nil {
		t.Fatal("非 Linux 也应返回快照")
	}
	if snapshot.CPU != nil || snapshot.Memory != nil || snapshot.Disk != nil ||
		snapshot.Network != nil || snapshot.Processes != nil || snapshot.Cgroup != nil || snapshot.PSI != nil {
		t.Fatal("非 Linux 的 /proc 相关 section 应为 nil")
	}
	if snapshot.Runtime == nil || snapshot.App == nil {
		t.Fatal("runtime/app 应照常采集")
	}
	seen := make(map[string]bool)
	for _, item := range snapshot.Errors {
		seen[item.Section] = true
	}
	for _, section := range []string{"cpu", "memory", "disk", "network", "processes", "cgroup", "psi"} {
		if !seen[section] {
			t.Fatalf("errors[] 缺少 section=%s: %+v", section, snapshot.Errors)
		}
	}
	if !snapshot.Meta.Partial {
		t.Fatalf("非 Linux 应标记 partial: %+v", snapshot.Meta)
	}
	if snapshot.Meta.ErrorsTruncated {
		t.Fatalf("7 条降级错误不应触发截断: %+v", snapshot.Meta)
	}
}

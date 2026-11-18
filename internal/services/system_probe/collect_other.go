//go:build !linux

package system_probe

import "context"

type otherCollector struct{}

func newPlatformCollector() collector {
	return otherCollector{}
}

// collect 在非 Linux 平台只记录降级错误，runtime/app/meta 仍由 Probe 采集。
func (otherCollector) collect(_ context.Context, _ *Probe, _ []Subprocess) platformData {
	sections := []string{"cpu", "memory", "disk", "network", "processes", "cgroup", "psi"}
	errs := make([]ProbeError, 0, len(sections))
	for _, section := range sections {
		errs = append(errs, ProbeError{Section: section, Error: "仅支持 linux"})
	}
	return platformData{Errors: errs}
}

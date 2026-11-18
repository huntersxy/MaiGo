//go:build !linux

package system_probe

// OwnerUID 在非 Linux 平台不可用；进程采集同样不会运行。
func (osFileReader) OwnerUID(path string) (int, error) {
	return 0, ErrUnsupported
}

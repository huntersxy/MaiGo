package system_probe

import (
	"errors"
	"io"
	"os"
)

// ErrUnsupported 表示当前平台不支持该能力。
var ErrUnsupported = errors.New("当前平台不支持")

// Reader 是采集层唯一的平台 I/O 入口，测试可替换路径与行为。
type Reader interface {
	Open(path string) (io.ReadCloser, error)
	ReadFile(path string, limit int64) ([]byte, error)
	ReadDir(path string) ([]string, error)
	OwnerUID(path string) (int, error)
}

// 单文件硬上限，防止异常文件拖垮请求。
const (
	limitProcStat      = 4 << 10
	limitProcStatus    = 16 << 10
	limitProcCmdline   = 129
	limitProcMeminfo   = 256 << 10
	limitProcDiskstats = 256 << 10
	limitCgroupFile    = 64 << 10
	limitProcNetFile   = 32 << 20
	maxProcNetLines    = 200000
	maxProcStatCores   = 16
	maxDiskDevices     = 32
	maxNetworkIfaces   = 16
	maxSubprocesses    = 8
	maxProbeErrors     = 32
	maxTextBytes       = 64 << 10
	procCmdlineBytes   = 128
	procNameBytes      = 64
	clockTicksPerSec   = 100
	defaultProcessShow = 20
	maxProcessShow     = 50
	defaultScanLimit   = 512
)

// osFileReader 是生产环境实现。
type osFileReader struct{}

func (osFileReader) Open(path string) (io.ReadCloser, error) {
	return os.Open(path)
}

func (osFileReader) ReadFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	if limit <= 0 {
		return io.ReadAll(file)
	}
	return io.ReadAll(io.LimitReader(file, limit))
}

func (osFileReader) ReadDir(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

func defaultReader() Reader {
	return osFileReader{}
}

package filesvc

import (
	"time"
)

// Config 控制文件存储配额与传输行为
type Config struct {
	Enabled                bool
	RootDir                string
	MaxFileBytes           int64
	MaxStorageBytes        int64
	MaxTextBytes           int64
	CacheDays              int
	MaxConcurrentTransfers int
	TicketLifetime         time.Duration
	TransferTimeout        time.Duration
	DefaultTransferBaseURL string
	TrustedDownloadOrigins []string
}

// DefaultConfig 返回与 MaiBot FilesConfig 默认值一致的配置
func DefaultConfig(mediaDir string) Config {
	const mib = 1024 * 1024
	return Config{
		Enabled:                true,
		RootDir:                mediaDir,
		MaxFileBytes:           50 * mib,
		MaxStorageBytes:        10 * 1024 * mib,
		MaxTextBytes:           8 * mib,
		CacheDays:              30,
		MaxConcurrentTransfers: 4,
		TicketLifetime:         5 * time.Minute,
		TransferTimeout:        120 * time.Second,
		DefaultTransferBaseURL: "http://127.0.0.1:7468",
	}
}

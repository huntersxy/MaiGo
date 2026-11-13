package maibot

import (
	"fmt"
	"time"
)

const (
	defaultFileMaxMib              = 50
	defaultFileMaxStorageGib       = 10
	defaultFileMaxTextMib          = 8
	defaultFileCacheDays           = 30
	defaultFileMaxConcurrent       = 4
	defaultFileTransferTimeoutSecs = 120
)

// FileMaxBytes 返回单文件大小上限，缺省或非法值回退 MaiBot 默认 50 MiB
func (b BotConfigFile) FileMaxBytes() int64 {
	mib := b.Files.MaxFileMib
	if !b.LoadMeta.HasFilesKey("max_file_mib") || mib < 1 || mib > 2048 {
		mib = defaultFileMaxMib
	}
	return int64(mib) << 20
}

// FileMaxStorageBytes 返回实例文件存储配额，缺省或非法值回退 MaiBot 默认 10 GiB
func (b BotConfigFile) FileMaxStorageBytes() int64 {
	gib := b.Files.MaxStorageGib
	if !b.LoadMeta.HasFilesKey("max_storage_gib") || gib < 1 {
		gib = defaultFileMaxStorageGib
	}
	return int64(gib) << 30
}

// FileMaxTextBytes 返回文本解析大小上限，缺省或非法值回退 MaiBot 默认 8 MiB
func (b BotConfigFile) FileMaxTextBytes() int64 {
	mib := b.Files.MaxTextMib
	if !b.LoadMeta.HasFilesKey("max_text_mib") || mib < 1 || mib > 50 {
		mib = defaultFileMaxTextMib
	}
	return int64(mib) << 20
}

// FileCacheDays 返回平台下载缓存保留天数，缺省或非法值回退 MaiBot 默认 30 天
func (b BotConfigFile) FileCacheDays() int {
	days := b.Files.CacheDays
	if !b.LoadMeta.HasFilesKey("cache_days") || days < 1 {
		days = defaultFileCacheDays
	}
	return days
}

// FileMaxConcurrentTransfers 返回同时上传下载上限，缺省或非法值回退 MaiBot 默认 4
func (b BotConfigFile) FileMaxConcurrentTransfers() int {
	n := b.Files.MaxConcurrentTransfers
	if !b.LoadMeta.HasFilesKey("max_concurrent_transfers") || n < 1 || n > 32 {
		n = defaultFileMaxConcurrent
	}
	return n
}

// FileTransferTimeout 返回单次文件传输超时，缺省或非法值回退 MaiBot 默认 120 秒
func (b BotConfigFile) FileTransferTimeout() time.Duration {
	seconds := b.Files.TransferTimeout
	if !b.LoadMeta.HasFilesKey("transfer_timeout") || seconds < 10 || seconds > 3600 {
		seconds = defaultFileTransferTimeoutSecs
	}
	return time.Duration(seconds) * time.Second
}

// FileTrustedDownloadOrigins 返回允许下载的内网适配器来源
func (b BotConfigFile) FileTrustedDownloadOrigins() []string {
	if len(b.Files.TrustedDownloadOrigins) == 0 {
		return nil
	}
	out := make([]string, len(b.Files.TrustedDownloadOrigins))
	copy(out, b.Files.TrustedDownloadOrigins)
	return out
}

// ValidateFilesConfig 对齐 MaiBot FilesConfig 的 pydantic 范围约束
func ValidateFilesConfig(bot BotConfigFile) error {
	if bot.LoadMeta.HasFilesKey("max_file_mib") {
		v := bot.Files.MaxFileMib
		if v < 1 || v > 2048 {
			return fmt.Errorf("files.max_file_mib 必须在 1～2048 之间")
		}
	}
	if bot.LoadMeta.HasFilesKey("max_storage_gib") {
		if bot.Files.MaxStorageGib < 1 {
			return fmt.Errorf("files.max_storage_gib 必须大于等于 1")
		}
	}
	if bot.LoadMeta.HasFilesKey("max_text_mib") {
		v := bot.Files.MaxTextMib
		if v < 1 || v > 50 {
			return fmt.Errorf("files.max_text_mib 必须在 1～50 之间")
		}
	}
	if bot.LoadMeta.HasFilesKey("cache_days") {
		if bot.Files.CacheDays < 1 {
			return fmt.Errorf("files.cache_days 必须大于等于 1")
		}
	}
	if bot.LoadMeta.HasFilesKey("max_concurrent_transfers") {
		v := bot.Files.MaxConcurrentTransfers
		if v < 1 || v > 32 {
			return fmt.Errorf("files.max_concurrent_transfers 必须在 1～32 之间")
		}
	}
	if bot.LoadMeta.HasFilesKey("transfer_timeout") {
		v := bot.Files.TransferTimeout
		if v < 10 || v > 3600 {
			return fmt.Errorf("files.transfer_timeout 必须在 10～3600 之间")
		}
	}
	return nil
}

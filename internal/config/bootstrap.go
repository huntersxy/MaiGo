package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Bootstrap 是进程启动期不可变的连接与密钥配置
type Bootstrap struct {
	DatabaseDSN string
	WebAdminKey string
	ServerPort  int
	MediaDir    string
	StickerDir  string
	LogLevel    string
	Debug       bool
	SecretKey   string
}

var boot Bootstrap

// LoadBootstrap 从环境变量读取启动配置
func LoadBootstrap() (Bootstrap, error) {
	b := Bootstrap{
		DatabaseDSN: strings.TrimSpace(os.Getenv("MAIBOT_DATABASE_DSN")),
		WebAdminKey: os.Getenv("MAIBOT_WEB_ADMIN_KEY"),
		MediaDir:    strings.TrimSpace(os.Getenv("MAIBOT_MEDIA_DIR")),
		StickerDir:  strings.TrimSpace(os.Getenv("MAIBOT_STICKER_DIR")),
		LogLevel:    strings.TrimSpace(os.Getenv("MAIBOT_LOG_LEVEL")),
		SecretKey:   strings.TrimSpace(os.Getenv("MAIBOT_SECRET_KEY")),
	}
	var err error
	if b.ServerPort, err = intEnv("MAIBOT_SERVER_PORT", 7468); err != nil {
		return b, err
	}
	if b.Debug, err = boolEnv("MAIBOT_DEBUG"); err != nil {
		return b, err
	}
	if b.MediaDir == "" {
		b.MediaDir = "./media"
	}
	if b.StickerDir == "" {
		b.StickerDir = "./stickers"
	}
	if b.LogLevel == "" {
		b.LogLevel = "info"
	}
	// DSN 留空时走 SQLite 默认路径，对齐 Python database.py:33 的
	// `sqlite:///<实例根>/data/MaiBot.db`；postgres:// 会被 OpenDB 拒绝
	if b.DatabaseDSN == "" {
		b.DatabaseDSN = DefaultDatabaseDSN()
	}
	if b.ServerPort <= 0 || b.ServerPort > 65535 {
		return b, fmt.Errorf("MAIBOT_SERVER_PORT 必须在 1 到 65535 之间")
	}
	boot = b
	return b, nil
}

// Boot 返回启动期配置
func Boot() Bootstrap {
	return boot
}

// InstanceRoot 返回实例根目录，对齐 Python src/common/paths.py get_instance_root
func InstanceRoot() string {
	if value := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR")); value != "" {
		return value
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

// DefaultDatabaseDSN 返回默认 SQLite 库路径，对齐 Python database.py:28-33
//
// Python 侧是 `_DB_DIR = 实例根/data`、`_DB_FILE = _DB_DIR/"MaiBot.db"`。
func DefaultDatabaseDSN() string {
	return filepath.ToSlash(filepath.Join(InstanceRoot(), "data", "MaiBot.db"))
}

// intEnv 解析整数环境变量；未设置时使用 fallback，显式设置但非法时返回错误而不是静默回退
func intEnv(name string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s 必须是整数，当前值 %q", name, raw)
	}
	return value, nil
}

// boolEnv 解析布尔环境变量；未设置时为 false，显式设置但非法时返回错误
func boolEnv(name string) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s 必须是布尔值，当前值 %q", name, raw)
	}
	return value, nil
}

package utils

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ShortIDAlphabet 短 ID 的字符集（数字 + 大写字母，共 36 进制）
const ShortIDAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

// TimestampMode 时间戳展示模式，对齐 math_utils.TimestampMode
type TimestampMode string

const (
	TimestampModeNormal      TimestampMode = "normal"
	TimestampModeNormalNoYMD TimestampMode = "normal_no_ymd"
	TimestampModeRelative    TimestampMode = "relative"
)

// NormalizeTimestampMode 按 Python `mode.upper() in TimestampMode.__members__` 解析模式名
//
// 大小写不敏感；未知模式返回错误，对齐 Python 抛 ValueError。
func NormalizeTimestampMode(mode string) (TimestampMode, error) {
	switch TimestampMode(strings.ToLower(strings.TrimSpace(mode))) {
	case TimestampModeNormal:
		return TimestampModeNormal, nil
	case TimestampModeNormalNoYMD:
		return TimestampModeNormalNoYMD, nil
	case TimestampModeRelative:
		return TimestampModeRelative, nil
	default:
		return "", fmt.Errorf("不支持的时间戳转换模式: %s", mode)
	}
}

// TranslateTimestampToHumanReadable 按模式格式化时间戳，对齐 translate_timestamp_to_human_readable
func TranslateTimestampToHumanReadable(timestamp time.Time, mode string) (string, error) {
	return TranslateTimestampToHumanReadableAt(timestamp, mode, time.Now())
}

// TranslateTimestampToHumanReadableAt 用指定 now 计算相对时间，便于测试注入
func TranslateTimestampToHumanReadableAt(timestamp time.Time, mode string, now time.Time) (string, error) {
	resolved, err := NormalizeTimestampMode(mode)
	if err != nil {
		return "", err
	}
	switch resolved {
	case TimestampModeNormal:
		return timestamp.Format("2006-01-02 15:04:05"), nil
	case TimestampModeNormalNoYMD:
		return timestamp.Format("15:04:05"), nil
	default:
		return RelativeTimestampText(now.Sub(timestamp), timestamp), nil
	}
}

// RelativeTimestampText 相对时间文本，对齐 RELATIVE 分支
func RelativeTimestampText(delta time.Duration, absolute time.Time) string {
	seconds := delta.Seconds()
	switch {
	case seconds < 20:
		return "刚刚"
	case seconds < 60:
		return fmt.Sprintf("%d秒前", int(seconds))
	case seconds < 3600:
		return fmt.Sprintf("%d分钟前", int(seconds/60))
	case seconds < 86400:
		return fmt.Sprintf("%d小时前", int(seconds/3600))
	case seconds < 2592000:
		return fmt.Sprintf("%d天前", int(seconds/86400))
	default:
		return absolute.Format("2006-01-02 15:04:05")
	}
}

// NumberToShortID 把数字编号转成定长短 ID（不可逆），对齐
// src/common/utils/math_utils.py 的 number_to_short_id：
// 对 "{originalID}{salt}" 取 SHA-256，取前 max(4, length) 字节按大端转整数，
// 再用 36 进制字符集逐位取模生成。
func NumberToShortID(originalID int64, salt string, length int) string {
	if length <= 0 {
		length = 6
	}
	data := strconv.FormatInt(originalID, 10) + salt
	digest := sha256.Sum256([]byte(data))

	needed := length
	if needed < 4 {
		needed = 4
	}
	if needed > len(digest) {
		needed = len(digest)
	}
	// 按大端把前 needed 字节转成大整数；用逐字节累加避免 big.Int 依赖
	// 只保留 36 进制取模所需的低位：每步先取模再进位，等价于整体取模
	base := int64(len(ShortIDAlphabet))
	var acc int64
	for _, b := range digest[:needed] {
		acc = (acc*256 + int64(b)) % powInt64(base, int64(length))
	}

	out := make([]byte, length)
	for i := length - 1; i >= 0; i-- {
		out[i] = ShortIDAlphabet[acc%base]
		acc /= base
	}
	return string(out)
}

// powInt64 计算 base^exp，用于把大整数截到 length 位可表示的范围
func powInt64(base, exp int64) int64 {
	result := int64(1)
	for i := int64(0); i < exp; i++ {
		if result > (1<<62)/base {
			return 1 << 62
		}
		result *= base
	}
	return result
}

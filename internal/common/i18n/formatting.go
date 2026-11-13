package i18n

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// FormatDatetimeLocalized 对齐 babel.dates.format_datetime(value, format, locale)
// 不支持未知 locale 时回退 DefaultLocale；空 format 等同 medium
func FormatDatetimeLocalized(value time.Time, locale, format string) string {
	out, _ := FormatDatetimeLocalizedE(value, locale, format)
	return out
}

// FormatDatetimeLocalizedE 与 FormatDatetimeLocalized 相同，但返回格式化错误
func FormatDatetimeLocalizedE(value time.Time, locale, format string) (string, error) {
	loc := formatLocaleOrEmpty(locale)
	data := formatDataForLocale(loc)
	pattern := strings.TrimSpace(format)
	if pattern == "" {
		pattern = "medium"
	}
	if key, ok := namedDateFormat(pattern); ok {
		date := formatDatePattern(value, data.dateFormats[key], data)
		tm := formatDatePattern(value, data.timeFormats[key], data)
		combined := strings.ReplaceAll(data.datetimeFormats[key], "{1}", date)
		combined = strings.ReplaceAll(combined, "{0}", tm)
		return combined, nil
	}
	return formatDatePattern(value, pattern, data), nil
}

// FormatNumberLocalized 对齐 babel.numbers.format_number 在 MaiBot 中的用法
func FormatNumberLocalized(value any, locale string) string {
	return FormatDecimalLocalized(value, locale)
}

// FormatDecimalLocalized 对齐 babel.numbers.format_decimal
func FormatDecimalLocalized(value any, locale string) string {
	out, _ := FormatDecimalLocalizedE(value, locale)
	return out
}

// FormatDecimalLocalizedE 与 FormatDecimalLocalized 相同，但返回解析错误
func FormatDecimalLocalizedE(value any, locale string) (string, error) {
	data := formatDataForLocale(formatLocaleOrEmpty(locale))
	if special, ok := specialNumber(value); ok {
		return special, nil
	}
	rat, negative, err := decimalRat(value)
	if err != nil {
		return "", err
	}
	return formatDecimalRat(rat, negative, data), nil
}

// SelectPluralCategory 对齐 formatting.select_plural_category
func SelectPluralCategory(locale string, count float64) string {
	return selectPluralCategory(locale, count)
}

// FormatDatetimeLocalized 使用 Manager 当前 locale 作为空 locale 的默认值
func (m *Manager) FormatDatetimeLocalized(value time.Time, locale, format string) string {
	return FormatDatetimeLocalized(value, m.emptyLocale(locale), format)
}

// FormatNumberLocalized 使用 Manager 当前 locale 作为空 locale 的默认值
func (m *Manager) FormatNumberLocalized(value any, locale string) string {
	return FormatNumberLocalized(value, m.emptyLocale(locale))
}

// FormatDecimalLocalized 使用 Manager 当前 locale 作为空 locale 的默认值
func (m *Manager) FormatDecimalLocalized(value any, locale string) string {
	return FormatDecimalLocalized(value, m.emptyLocale(locale))
}

func (m *Manager) emptyLocale(locale string) string {
	if strings.TrimSpace(locale) != "" {
		return locale
	}
	if m == nil {
		return DefaultLocale()
	}
	return m.Locale()
}

func formatLocaleOrEmpty(locale string) string {
	if strings.TrimSpace(locale) == "" {
		return DefaultLocale()
	}
	return NormalizeLocale(locale)
}

func namedDateFormat(format string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "full":
		return formatFull, true
	case "long":
		return formatLong, true
	case "medium":
		return formatMedium, true
	case "short":
		return formatShort, true
	default:
		return 0, false
	}
}

func specialNumber(value any) (string, bool) {
	switch v := value.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return specialFloat(v), true
		}
	case float32:
		f := float64(v)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return specialFloat(f), true
		}
	}
	return "", false
}

func specialFloat(value float64) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "∞"
	case math.IsInf(value, -1):
		return "-∞"
	default:
		return ""
	}
}

func formatDatePattern(value time.Time, pattern string, data localeDateData) string {
	var out strings.Builder
	runes := []rune(pattern)
	for i := 0; i < len(runes); {
		ch := runes[i]
		if ch == '\'' {
			if i+1 < len(runes) && runes[i+1] == '\'' {
				out.WriteRune('\'')
				i += 2
				continue
			}
			i++
			for i < len(runes) {
				if runes[i] == '\'' {
					if i+1 < len(runes) && runes[i+1] == '\'' {
						out.WriteRune('\'')
						i += 2
						continue
					}
					i++
					break
				}
				out.WriteRune(runes[i])
				i++
			}
			continue
		}
		if !isDatePatternLetter(ch) {
			out.WriteRune(ch)
			i++
			continue
		}
		j := i
		for j < len(runes) && runes[j] == ch {
			j++
		}
		out.WriteString(formatDateToken(value, ch, j-i, data))
		i = j
	}
	return out.String()
}

func isDatePatternLetter(ch rune) bool {
	return ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}

func formatDateToken(value time.Time, letter rune, count int, data localeDateData) string {
	weekday := (int(value.Weekday()) + 6) % 7
	switch letter {
	case 'G':
		return "AD"
	case 'y', 'Y':
		if count == 2 {
			return fmt.Sprintf("%02d", value.Year()%100)
		}
		return strconv.Itoa(value.Year())
	case 'M', 'L':
		switch {
		case count == 1:
			return strconv.Itoa(int(value.Month()))
		case count == 2:
			return fmt.Sprintf("%02d", int(value.Month()))
		case count == 3:
			return data.monthsAbbr[value.Month()-1]
		default:
			return data.monthsWide[value.Month()-1]
		}
	case 'd':
		if count == 1 {
			return strconv.Itoa(value.Day())
		}
		return fmt.Sprintf("%02d", value.Day())
	case 'E':
		switch {
		case count <= 3:
			return data.daysAbbr[weekday]
		case count == 4:
			return data.daysWide[weekday]
		default:
			return data.daysNarrow[weekday]
		}
	case 'e', 'c':
		return strconv.Itoa(weekday + 1)
	case 'a', 'b', 'B':
		if value.Hour() < 12 {
			return data.am
		}
		return data.pm
	case 'H':
		if count == 1 {
			return strconv.Itoa(value.Hour())
		}
		return fmt.Sprintf("%02d", value.Hour())
	case 'h':
		hour := value.Hour() % 12
		if hour == 0 {
			hour = 12
		}
		if count == 1 {
			return strconv.Itoa(hour)
		}
		return fmt.Sprintf("%02d", hour)
	case 'K':
		if count == 1 {
			return strconv.Itoa(value.Hour() % 12)
		}
		return fmt.Sprintf("%02d", value.Hour()%12)
	case 'k':
		hour := value.Hour()
		if hour == 0 {
			hour = 24
		}
		if count == 1 {
			return strconv.Itoa(hour)
		}
		return fmt.Sprintf("%02d", hour)
	case 'm':
		if count == 1 {
			return strconv.Itoa(value.Minute())
		}
		return fmt.Sprintf("%02d", value.Minute())
	case 's':
		if count == 1 {
			return strconv.Itoa(value.Second())
		}
		return fmt.Sprintf("%02d", value.Second())
	case 'S':
		nanos := fmt.Sprintf("%09d", value.Nanosecond())
		if count >= len(nanos) {
			return nanos
		}
		return nanos[:count]
	case 'z', 'v', 'V':
		return localizedZoneName(value, data, count >= 4)
	case 'Z':
		return zoneOffset(value, false)
	case 'X', 'x':
		return zoneOffsetISO(value, count)
	case 'Q', 'q':
		quarter := (int(value.Month())-1)/3 + 1
		if count <= 1 {
			return strconv.Itoa(quarter)
		}
		return fmt.Sprintf("Q%d", quarter)
	case 'D':
		return strconv.Itoa(value.YearDay())
	case 'w':
		_, week := value.ISOWeek()
		return strconv.Itoa(week)
	case 'W':
		return strconv.Itoa((value.Day()-1)/7 + 1)
	default:
		return strings.Repeat(string(letter), count)
	}
}

func localizedZoneName(value time.Time, data localeDateData, long bool) string {
	zoneName, offset := value.Zone()
	name := value.Location().String()
	index := localeIndexForName(data)
	if entry, ok := localeZoneNames[name]; ok {
		if long {
			if value.IsDST() {
				return entry.daylight[index]
			}
			return entry.standard[index]
		}
		if name == "America/New_York" && value.IsDST() {
			if index == 1 {
				return "EDT"
			}
			return fmt.Sprintf("%+03d%02d", offset/3600, absInt(offset%3600/60))
		}
		return entry.short[index]
	}
	if strings.EqualFold(zoneName, "UTC") || offset == 0 && strings.Contains(strings.ToUpper(name), "UTC") {
		return "UTC"
	}
	if long {
		if zoneName != "" && zoneName != "Local" {
			return zoneName
		}
		return fmt.Sprintf("GMT%+03d:%02d", offset/3600, absInt(offset%3600/60))
	}
	return fmt.Sprintf("%+03d%02d", offset/3600, absInt(offset%3600/60))
}

func localeIndexForName(data localeDateData) int {
	return data.zoneIndex
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func zoneOffset(value time.Time, colon bool) string {
	_, offset := value.Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	hours := offset / 3600
	minutes := offset % 3600 / 60
	if colon {
		return fmt.Sprintf("%s%02d:%02d", sign, hours, minutes)
	}
	return fmt.Sprintf("%s%02d%02d", sign, hours, minutes)
}

func zoneOffsetISO(value time.Time, count int) string {
	if count == 1 {
		_, offset := value.Zone()
		if offset == 0 {
			return "Z"
		}
	}
	return zoneOffset(value, count >= 3)
}

func decimalRat(value any) (*big.Rat, bool, error) {
	negative := false
	var text string
	switch v := value.(type) {
	case json.Number:
		text = string(v)
	case string:
		text = strings.TrimSpace(v)
	case int:
		text = strconv.FormatInt(int64(v), 10)
	case int8:
		text = strconv.FormatInt(int64(v), 10)
	case int16:
		text = strconv.FormatInt(int64(v), 10)
	case int32:
		text = strconv.FormatInt(int64(v), 10)
	case int64:
		text = strconv.FormatInt(v, 10)
	case uint:
		text = strconv.FormatUint(uint64(v), 10)
	case uint8:
		text = strconv.FormatUint(uint64(v), 10)
	case uint16:
		text = strconv.FormatUint(uint64(v), 10)
	case uint32:
		text = strconv.FormatUint(uint64(v), 10)
	case uint64:
		text = strconv.FormatUint(v, 10)
	case float32:
		negative = math.Signbit(float64(v))
		text = strconv.FormatFloat(math.Abs(float64(v)), 'f', -1, 32)
	case float64:
		negative = math.Signbit(v)
		text = strconv.FormatFloat(math.Abs(v), 'f', -1, 64)
	default:
		return nil, false, fmt.Errorf("i18n: 不支持的数字类型 %T", value)
	}
	if text == "" {
		return nil, false, fmt.Errorf("i18n: 数字为空")
	}
	if strings.HasPrefix(text, "-") {
		negative = true
		text = strings.TrimPrefix(text, "-")
	} else if strings.HasPrefix(text, "+") {
		text = strings.TrimPrefix(text, "+")
	}
	rat, ok := new(big.Rat).SetString(text)
	if !ok {
		return nil, false, fmt.Errorf("i18n: 无法解析数字 %q", text)
	}
	return rat, negative, nil
}

func formatDecimalRat(rat *big.Rat, negative bool, data localeDateData) string {
	scaled := new(big.Rat).Mul(rat, new(big.Rat).SetInt64(1000))
	quotient := new(big.Int).Quo(scaled.Num(), scaled.Denom())
	remainder := new(big.Int).Rem(scaled.Num(), scaled.Denom())
	twiceRemainder := new(big.Int).Lsh(new(big.Int).Abs(remainder), 1)
	comparison := twiceRemainder.Cmp(scaled.Denom())
	if comparison > 0 || comparison == 0 && quotient.Bit(0) == 1 {
		quotient.Add(quotient, big.NewInt(1))
	}
	digits := quotient.String()
	if len(digits) <= 3 {
		digits = strings.Repeat("0", 4-len(digits)) + digits
	}
	integerPart := digits[:len(digits)-3]
	fractionPart := strings.TrimRight(digits[len(digits)-3:], "0")
	var out strings.Builder
	if negative {
		out.WriteString("-")
	}
	out.WriteString(groupDigits(integerPart, data.groupSep))
	if fractionPart != "" {
		out.WriteString(data.decimalSep)
		out.WriteString(fractionPart)
	}
	return out.String()
}

func groupDigits(digits string, separator string) string {
	if len(digits) <= 3 {
		return digits
	}
	first := len(digits) % 3
	if first == 0 {
		first = 3
	}
	var out strings.Builder
	out.WriteString(digits[:first])
	for i := first; i < len(digits); i += 3 {
		out.WriteString(separator)
		out.WriteString(digits[i : i+3])
	}
	return out.String()
}

package i18n

// localeDateData 保存对齐 Babel/CLDR 的日期时间模式与名称
type localeDateData struct {
	dateFormats     [4]string
	timeFormats     [4]string
	datetimeFormats [4]string
	monthsWide      [12]string
	monthsAbbr      [12]string
	daysWide        [7]string
	daysAbbr        [7]string
	daysNarrow      [7]string
	zoneIndex       int
	am              string
	pm              string
	decimalSep      string
	groupSep        string
}

const (
	formatFull = iota
	formatLong
	formatMedium
	formatShort
)

var localeDateFormats = map[string]localeDateData{
	"zh-CN": {
		dateFormats:     [4]string{"y年M月d日EEEE", "y年M月d日", "y年M月d日", "y/M/d"},
		timeFormats:     [4]string{"zzzz HH:mm:ss", "z HH:mm:ss", "HH:mm:ss", "HH:mm"},
		datetimeFormats: [4]string{"{1} {0}", "{1} {0}", "{1} {0}", "{1} {0}"},
		monthsWide:      [12]string{"一月", "二月", "三月", "四月", "五月", "六月", "七月", "八月", "九月", "十月", "十一月", "十二月"},
		monthsAbbr:      [12]string{"1月", "2月", "3月", "4月", "5月", "6月", "7月", "8月", "9月", "10月", "11月", "12月"},
		daysWide:        [7]string{"星期一", "星期二", "星期三", "星期四", "星期五", "星期六", "星期日"},
		daysAbbr:        [7]string{"周一", "周二", "周三", "周四", "周五", "周六", "周日"},
		daysNarrow:      [7]string{"一", "二", "三", "四", "五", "六", "日"},
		zoneIndex:       0,
		am:              "上午",
		pm:              "下午",
		decimalSep:      ".",
		groupSep:        ",",
	},
	"en-US": {
		dateFormats:     [4]string{"EEEE, MMMM d, y", "MMMM d, y", "MMM d, y", "M/d/yy"},
		timeFormats:     [4]string{"h:mm:ss\u202fa zzzz", "h:mm:ss\u202fa z", "h:mm:ss\u202fa", "h:mm\u202fa"},
		datetimeFormats: [4]string{"{1}, {0}", "{1}, {0}", "{1}, {0}", "{1}, {0}"},
		monthsWide:      [12]string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"},
		monthsAbbr:      [12]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"},
		daysWide:        [7]string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"},
		daysAbbr:        [7]string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"},
		daysNarrow:      [7]string{"M", "T", "W", "T", "F", "S", "S"},
		zoneIndex:       1,
		am:              "AM",
		pm:              "PM",
		decimalSep:      ".",
		groupSep:        ",",
	},
	"ja-JP": {
		dateFormats:     [4]string{"y年M月d日EEEE", "y年M月d日", "y/MM/dd", "y/MM/dd"},
		timeFormats:     [4]string{"H時mm分ss秒 zzzz", "H:mm:ss z", "H:mm:ss", "H:mm"},
		datetimeFormats: [4]string{"{1} {0}", "{1} {0}", "{1} {0}", "{1} {0}"},
		monthsWide:      [12]string{"1月", "2月", "3月", "4月", "5月", "6月", "7月", "8月", "9月", "10月", "11月", "12月"},
		monthsAbbr:      [12]string{"1月", "2月", "3月", "4月", "5月", "6月", "7月", "8月", "9月", "10月", "11月", "12月"},
		daysWide:        [7]string{"月曜日", "火曜日", "水曜日", "木曜日", "金曜日", "土曜日", "日曜日"},
		daysAbbr:        [7]string{"月", "火", "水", "木", "金", "土", "日"},
		daysNarrow:      [7]string{"月", "火", "水", "木", "金", "土", "日"},
		zoneIndex:       2,
		am:              "午前",
		pm:              "午後",
		decimalSep:      ".",
		groupSep:        ",",
	},
	"ko-KR": {
		dateFormats:     [4]string{"y년 MMMM d일 EEEE", "y년 MMMM d일", "y. M. d.", "yy. M. d."},
		timeFormats:     [4]string{"a h시 m분 s초 zzzz", "a h시 m분 s초 z", "a h:mm:ss", "a h:mm"},
		datetimeFormats: [4]string{"{1} {0}", "{1} {0}", "{1} {0}", "{1} {0}"},
		monthsWide:      [12]string{"1월", "2월", "3월", "4월", "5월", "6월", "7월", "8월", "9월", "10월", "11월", "12월"},
		monthsAbbr:      [12]string{"1월", "2월", "3월", "4월", "5월", "6월", "7월", "8월", "9월", "10월", "11월", "12월"},
		daysWide:        [7]string{"월요일", "화요일", "수요일", "목요일", "금요일", "토요일", "일요일"},
		daysAbbr:        [7]string{"월", "화", "수", "목", "금", "토", "일"},
		daysNarrow:      [7]string{"월", "화", "수", "목", "금", "토", "일"},
		zoneIndex:       3,
		am:              "AM",
		pm:              "PM",
		decimalSep:      ".",
		groupSep:        ",",
	},
}

// formatDataForLocale 返回格式化数据；未知 locale 回退 defaultLocale
func formatDataForLocale(locale string) localeDateData {
	normalized := NormalizeLocale(locale)
	if data, ok := localeDateFormats[normalized]; ok {
		return data
	}
	// Python locales 目录使用 ko/zh/ja/en 等短标签，Babel 也接受这些别名
	switch normalized {
	case "ko":
		return localeDateFormats["ko-KR"]
	case "zh":
		return localeDateFormats["zh-CN"]
	case "ja":
		return localeDateFormats["ja-JP"]
	case "en":
		return localeDateFormats["en-US"]
	default:
		return localeDateFormats[defaultLocale]
	}
}

// localeZoneNames 常见时区的本地化名称；未覆盖的时区回退到 Go/偏移
type localeZoneName struct {
	short    [4]string
	standard [4]string
	daylight [4]string
}

var localeZoneNames = map[string]localeZoneName{
	"UTC": {
		short:    [4]string{"UTC", "UTC", "UTC", "UTC"},
		standard: [4]string{"协调世界时", "Coordinated Universal Time", "協定世界時", "협정 세계시"},
		daylight: [4]string{"协调世界时", "Coordinated Universal Time", "協定世界時", "협정 세계시"},
	},
	"Asia/Shanghai": {
		short:    [4]string{"+0800", "+0800", "+0800", "+0800"},
		standard: [4]string{"中国标准时间", "China Standard Time", "中国標準時", "중국 표준시"},
		daylight: [4]string{"中国标准时间", "China Standard Time", "中国標準時", "중국 표준시"},
	},
	"Asia/Tokyo": {
		short:    [4]string{"+0900", "+0900", "+0900", "+0900"},
		standard: [4]string{"日本标准时间", "Japan Standard Time", "日本標準時", "일본 표준시"},
		daylight: [4]string{"日本标准时间", "Japan Standard Time", "日本標準時", "일본 표준시"},
	},
	"America/New_York": {
		short:    [4]string{"EST", "EST", "EST", "EST"},
		standard: [4]string{"北美东部标准时间", "Eastern Standard Time", "アメリカ東部標準時", "미 동부 표준시"},
		daylight: [4]string{"北美东部夏令时间", "Eastern Daylight Time", "アメリカ東部夏時間", "미 동부 하절기 시간"},
	},
}

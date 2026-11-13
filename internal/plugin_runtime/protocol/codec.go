package protocol

import (
	"fmt"
	"sync"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

// DatetimeExtCode / DateExtCode 对齐 Python protocol.codec DATETIME_EXT_CODE / DATE_EXT_CODE
const (
	DatetimeExtCode int8 = 42
	DateExtCode     int8 = 43
)

// Codec 消息编解码器抽象，对齐 protocol.codec.Codec
type Codec interface {
	EncodeEnvelope(envelope Envelope) ([]byte, error)
	DecodeEnvelope(data []byte) (Envelope, error)
	Encode(obj map[string]any) ([]byte, error)
	Decode(data []byte) (map[string]any, error)
}

// MsgPackCodec MsgPack 编解码器
type MsgPackCodec struct{}

var registerMsgPackExtOnce sync.Once

// datetimeExt / dateExt 仅用于插件协议 Ext 往返，避免全局覆盖 msgpack 默认 time.Time(-1)
type datetimeExt struct{ Time time.Time }

type dateExt struct {
	Year  int
	Month time.Month
	Day   int
}

func (t datetimeExt) MarshalMsgpack() ([]byte, error) {
	return []byte(pyISOFormatDatetime(t.Time)), nil
}

// pyISOFormatDatetime 对齐 Python datetime.isoformat()
//
// 微秒为 0 时不带小数部分，否则固定 6 位；naive（UTC 视为 naive）不带时区后缀，
// 其它时区追加 ±HH:MM。Go 的 RFC3339Nano 会裁掉尾部零并输出 Z，与 Python 字节不同。
func pyISOFormatDatetime(value time.Time) string {
	base := value.Format("2006-01-02T15:04:05")
	if micro := value.Nanosecond() / 1000; micro != 0 {
		base += fmt.Sprintf(".%06d", micro)
	}
	if _, offsetSeconds := value.Zone(); offsetSeconds != 0 {
		base += value.Format("-07:00")
	}
	return base
}

func (t *datetimeExt) UnmarshalMsgpack(b []byte) error {
	parsed, err := parseDatetimeExtISO(string(b))
	if err != nil {
		return err
	}
	t.Time = parsed
	return nil
}

func parseDatetimeExtISO(text string) (time.Time, error) {
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04:05.999999",
	}
	var last error
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, text)
		if err == nil {
			return parsed, nil
		}
		last = err
	}
	return time.Time{}, last
}

func (d dateExt) MarshalMsgpack() ([]byte, error) {
	return []byte(time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC).Format("2006-01-02")), nil
}

func (d *dateExt) UnmarshalMsgpack(b []byte) error {
	parsed, err := time.Parse("2006-01-02", string(b))
	if err != nil {
		return err
	}
	d.Year, d.Month, d.Day = parsed.Year(), parsed.Month(), parsed.Day()
	return nil
}

func ensureMsgPackExtRegistered() {
	registerMsgPackExtOnce.Do(func() {
		msgpack.RegisterExt(DatetimeExtCode, (*datetimeExt)(nil))
		msgpack.RegisterExt(DateExtCode, (*dateExt)(nil))
	})
}

func wrapMsgPackExtValues(value any) any {
	switch typed := value.(type) {
	case time.Time:
		return &datetimeExt{Time: typed}
	case *time.Time:
		if typed == nil {
			return nil
		}
		return &datetimeExt{Time: *typed}
	case datetimeExt:
		copy := typed
		return &copy
	case *datetimeExt:
		return typed
	case dateExt:
		copy := typed
		return &copy
	case *dateExt:
		return typed
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = wrapMsgPackExtValues(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = wrapMsgPackExtValues(item)
		}
		return out
	default:
		return value
	}
}

func unwrapMsgPackExtValues(value any) any {
	switch typed := value.(type) {
	case *datetimeExt:
		if typed == nil {
			return nil
		}
		return typed.Time
	case datetimeExt:
		return typed.Time
	case *dateExt:
		if typed == nil {
			return nil
		}
		return time.Date(typed.Year, typed.Month, typed.Day, 0, 0, 0, 0, time.UTC)
	case dateExt:
		return time.Date(typed.Year, typed.Month, typed.Day, 0, 0, 0, 0, time.UTC)
	case map[string]any:
		for key, item := range typed {
			typed[key] = unwrapMsgPackExtValues(item)
		}
		return typed
	case []any:
		for i, item := range typed {
			typed[i] = unwrapMsgPackExtValues(item)
		}
		return typed
	default:
		return value
	}
}

func (MsgPackCodec) Encode(obj map[string]any) ([]byte, error) {
	ensureMsgPackExtRegistered()
	wrapped, _ := wrapMsgPackExtValues(obj).(map[string]any)
	return msgpack.Marshal(wrapped)
}

func (MsgPackCodec) Decode(data []byte) (map[string]any, error) {
	ensureMsgPackExtRegistered()
	var raw map[string]any
	if err := msgpack.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("期望解码为 map，实际为空")
	}
	unwrapped, _ := unwrapMsgPackExtValues(raw).(map[string]any)
	return unwrapped, nil
}

func (c MsgPackCodec) EncodeEnvelope(envelope Envelope) ([]byte, error) {
	var asMap map[string]any
	b, err := msgpack.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	if err := msgpack.Unmarshal(b, &asMap); err != nil {
		return nil, err
	}
	return c.Encode(asMap)
}

func (c MsgPackCodec) DecodeEnvelope(data []byte) (Envelope, error) {
	raw, err := c.Decode(data)
	if err != nil {
		return Envelope{}, err
	}
	b, err := msgpack.Marshal(raw)
	if err != nil {
		return Envelope{}, err
	}
	var env Envelope
	if err := msgpack.Unmarshal(b, &env); err != nil {
		return Envelope{}, err
	}
	if env.Payload == nil {
		env.Payload = map[string]any{}
	}
	return env, nil
}

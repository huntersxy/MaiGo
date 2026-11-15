package test

import (
	"testing"

	"MaiGo/internal/plugin_runtime/protocol"

	"github.com/vmihailenco/msgpack/v5"
)

// TestMsgPackDecodeNormalizesNumbers 确认协议解码把 msgpack 的最小宽度整数
// 统一成 int64/float64，避免宿主各处的 webUIInt 之类取值函数因类型不匹配失败。
func TestMsgPackDecodeNormalizesNumbers(t *testing.T) {
	payload := map[string]any{
		"schema_version": 2,   // Python 打包后是 positive fixint
		"count":          300, // uint16
		"delta":          -1,  // int8
		"ratio":          float32(0.5),
		"nested":         map[string]any{"level": 7},
		"items":          []any{1, 2},
	}
	encoded, err := msgpack.Marshal(payload)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	decoded, err := protocol.MsgPackCodec{}.Decode(encoded)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	for _, key := range []string{"schema_version", "count", "delta"} {
		if _, ok := decoded[key].(int64); !ok {
			t.Fatalf("%s 应归一化为 int64，实际 %T", key, decoded[key])
		}
	}
	if _, ok := decoded["ratio"].(float64); !ok {
		t.Fatalf("ratio 应归一化为 float64，实际 %T", decoded["ratio"])
	}
	nested, _ := decoded["nested"].(map[string]any)
	if _, ok := nested["level"].(int64); !ok {
		t.Fatalf("嵌套整数应归一化为 int64，实际 %T", nested["level"])
	}
}

package snowluma

import "testing"

func TestDecodeJSONPayloadKeepsIntegers(t *testing.T) {
	raw := []byte(`{
		"status": "ok",
		"retcode": 0,
		"data": [
			{"user_id": 123456789, "nickname": "a"},
			{"user_id": 123456789.0, "nickname": "b"}
		]
	}`)
	payload, err := decodeJSONPayload(raw)
	if err != nil {
		t.Fatalf("decodeJSONPayload: %v", err)
	}
	list := normalizePayloadList(payload["data"])
	if len(list) != 2 {
		t.Fatalf("成员数 = %d", len(list))
	}
	// 整数字面量与 Python json.loads 一致解析成 int，插件 str(user_id) 不会带 ".0"
	if _, ok := list[0]["user_id"].(int64); !ok {
		t.Fatalf("整数字面量应为 int64，实际 %T", list[0]["user_id"])
	}
	if got := asString(list[0]["user_id"]); got != "123456789" {
		t.Fatalf("user_id 展示为 %q", got)
	}
	// 小数字面量保持 float64，与 Python 一致
	if _, ok := list[1]["user_id"].(float64); !ok {
		t.Fatalf("小数字面量应为 float64，实际 %T", list[1]["user_id"])
	}
}

func TestDecodeJSONPayloadRejectsTrailingData(t *testing.T) {
	if _, err := decodeJSONPayload([]byte(`{"a":1}{"b":2}`)); err == nil {
		t.Fatal("帧尾多余内容应报错")
	}
}

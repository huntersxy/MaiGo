package memory

import "testing"

func TestRawJSONScan(t *testing.T) {
	cases := []struct {
		name    string
		value   any
		want    string
		wantErr bool
	}{
		{name: "text", value: `{"a":1}`, want: `{"a":1}`},
		{name: "blob", value: []byte(`{"b":2}`), want: `{"b":2}`},
		{name: "nil", value: nil, want: ""},
		{name: "unsupported", value: 3, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got RawJSON
			err := got.Scan(tc.value)
			if tc.wantErr {
				if err == nil {
					t.Fatal("应返回错误")
				}
				return
			}
			if err != nil {
				t.Fatalf("扫描失败: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("扫描结果不符: got=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestRawJSONValue(t *testing.T) {
	value, err := RawJSON(`{"a":1}`).Value()
	if err != nil || value != `{"a":1}` {
		t.Fatalf("Value 应写回 JSON 文本: value=%v err=%v", value, err)
	}
	empty, err := RawJSON(nil).Value()
	if err != nil || empty != "null" {
		t.Fatalf("空值应写回 null: value=%v err=%v", empty, err)
	}
}

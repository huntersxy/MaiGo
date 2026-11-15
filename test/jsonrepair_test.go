package test

import (
	"testing"

	"maibot-go/internal/common/jsonrepair"
)

func TestJSONRepairStrictJSONPassesThrough(t *testing.T) {
	value, err := jsonrepair.RepairPythonParity(`{"a": 1, "b": [true, null]}`)
	if err != nil {
		t.Fatalf("RepairPythonParity failed: %v", err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected object, got %T", value)
	}
	if object["a"] != int64(1) && object["a"] != float64(1) {
		t.Fatalf("unexpected a: %#v", object["a"])
	}
}

func TestJSONRepairPythonNoneStaysString(t *testing.T) {
	// Python json_repair 0.60.1 只识别 true/false/null；裸 None 应保留为字符串。
	value, err := jsonrepair.RepairPythonParity(`{"a": None}`)
	if err != nil {
		t.Fatalf("RepairPythonParity failed: %v", err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected object, got %T", value)
	}
	if object["a"] != "None" {
		t.Fatalf("expected None to stay a string, got %#v", object["a"])
	}
}

func TestJSONRepairStripsComments(t *testing.T) {
	value, err := jsonrepair.RepairPythonParity("{\n  // comment\n  \"a\": 1\n}")
	if err != nil {
		t.Fatalf("RepairPythonParity failed: %v", err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected object, got %T", value)
	}
	if _, ok := object["a"]; !ok {
		t.Fatalf("expected key a, got %#v", object)
	}
}

func TestJSONRepairRepairsTrailingComma(t *testing.T) {
	value, err := jsonrepair.RepairPythonParity(`{"a": 1,}`)
	if err != nil {
		t.Fatalf("RepairPythonParity failed: %v", err)
	}
	if _, ok := value.(map[string]any); !ok {
		t.Fatalf("expected object, got %T", value)
	}
}

func TestJSONRepairPlainTextIsNotObject(t *testing.T) {
	// Python repair_json("not json at all") 返回带引号的 JSON 字符串，
	// json.loads 后是 string；调用方的 map 断言会失败并走兜底，而不是报错。
	value, err := jsonrepair.RepairPythonParity("not json at all")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := value.(map[string]any); ok {
		t.Fatalf("expected non-object for plain text, got %#v", value)
	}
}

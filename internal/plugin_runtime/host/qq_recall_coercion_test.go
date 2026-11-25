package host

import (
	"encoding/json"
	"testing"
)

func TestCoerceToolStringArgHandlesNumbersAndStrings(t *testing.T) {
	cases := []struct {
		name string
		raw  any
		want string
	}{
		{name: "nil", raw: nil, want: ""},
		{name: "string", raw: " 1962381557 ", want: "1962381557"},
		{name: "int", raw: 1962381557, want: "1962381557"},
		{name: "int64", raw: int64(1962381557), want: "1962381557"},
		{name: "float64", raw: float64(1962381557), want: "1962381557"},
		{name: "json_number", raw: json.Number("1962381557"), want: "1962381557"},
	}
	for _, tc := range cases {
		if got := coerceToolStringArg(tc.raw); got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

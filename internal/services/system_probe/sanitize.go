package system_probe

import (
	"math"
	"reflect"
)

// sanitizeSnapshot 把 Snapshot 中的非有限浮点值置空，保证 JSON 编码不会遇到 NaN/Inf。
func sanitizeSnapshot(snapshot *Snapshot) {
	if snapshot == nil {
		return
	}
	walkFinite(reflect.ValueOf(snapshot).Elem())
}

func walkFinite(value reflect.Value) {
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return
		}
		if value.Type().Elem().Kind() == reflect.Float64 {
			finite := value.Interface().(*float64)
			if finite == nil || math.IsNaN(*finite) || math.IsInf(*finite, 0) {
				value.Set(reflect.Zero(value.Type()))
				return
			}
			return
		}
		walkFinite(value.Elem())
	case reflect.Interface:
		if value.IsNil() {
			return
		}
		walkFinite(value.Elem())
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			field := value.Field(i)
			if field.CanSet() || field.Kind() == reflect.Struct || field.Kind() == reflect.Pointer ||
				field.Kind() == reflect.Slice {
				walkFinite(field)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			walkFinite(value.Index(i))
		}
	case reflect.Float64:
		if value.CanSet() {
			number := value.Float()
			if math.IsNaN(number) || math.IsInf(number, 0) {
				value.SetFloat(0)
			}
		}
	}
}

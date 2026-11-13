package maibot

import (
	"reflect"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// unknownTopLevelKeys 返回 TOML 根表中未在目标结构体声明、且不是 [inner] 的键
// 对齐 Python ConfigBase.from_dict 的 redundant_attributes 检测，Go 侧只告警不静默丢弃文件内容
func unknownTopLevelKeys(raw []byte, target any) []string {
	var data map[string]any
	if err := toml.Unmarshal(raw, &data); err != nil {
		return nil
	}
	known := tomlFieldNames(target)
	var unknown []string
	for key := range data {
		if key == "inner" {
			continue
		}
		if _, ok := known[key]; ok {
			continue
		}
		unknown = append(unknown, key)
	}
	sort.Strings(unknown)
	return unknown
}

func tomlFieldNames(target any) map[string]struct{} {
	out := make(map[string]struct{})
	typ := reflect.TypeOf(target)
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return out
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}
		tag := field.Tag.Get("toml")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name != "" {
			out[name] = struct{}{}
		}
	}
	return out
}

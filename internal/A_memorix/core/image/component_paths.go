package imagememory

import (
	"reflect"
	"strconv"
	"strings"
)

// ImageComponentRef 表示遍历到的图片组件
type ImageComponentRef struct {
	Path      string
	Component any
}

// BuildChatExternalRef 对齐 Python build_chat_external_ref
func BuildChatExternalRef(chatID string, messageID string, componentPath string) string {
	return "chat:" + chatID + ":" + messageID + ":" + componentPath
}

// BuildPackageExternalRef 对齐 Python build_package_external_ref
func BuildPackageExternalRef(installationID string, occurrenceID string) string {
	return "package:" + installationID + ":" + occurrenceID
}

// IterMessageImageComponents 深度遍历组件中的图片，visit 返回 false 时停止
func IterMessageImageComponents(components any, includeEmoji bool, prefix string, visit func(ImageComponentRef) bool) {
	if visit == nil {
		return
	}
	walkImageComponents(reflect.ValueOf(components), includeEmoji, prefix, visit)
}

// CollectMessageImageComponents 收集组件中的图片引用
func CollectMessageImageComponents(components any, includeEmoji bool) []ImageComponentRef {
	collected := []ImageComponentRef{}
	IterMessageImageComponents(components, includeEmoji, "", func(reference ImageComponentRef) bool {
		collected = append(collected, reference)
		return true
	})
	return collected
}

// walkImageComponents 递归遍历序列，对齐 Python iter_message_image_components
func walkImageComponents(sequence reflect.Value, includeEmoji bool, prefix string, visit func(ImageComponentRef) bool) bool {
	sequence = indirectReflectValue(sequence)
	if !sequence.IsValid() {
		return true
	}
	if sequence.Kind() != reflect.Slice && sequence.Kind() != reflect.Array {
		return true
	}
	if sequence.Kind() == reflect.Slice && sequence.Type().Elem().Kind() == reflect.Uint8 {
		return true
	}
	for index := 0; index < sequence.Len(); index++ {
		component := sequence.Index(index).Interface()
		path := strconv.Itoa(index)
		if prefix != "" {
			path = prefix + "." + strconv.Itoa(index)
		}
		if isMessageImageComponent(component, includeEmoji) {
			if !visit(ImageComponentRef{Path: path, Component: component}) {
				return false
			}
		}
		if nested, ok := nestedComponentSequence(component); ok {
			if !walkImageComponents(nested, includeEmoji, path, visit) {
				return false
			}
		}
	}
	return true
}

// isMessageImageComponent 判断组件是否为图片或表情图片
func isMessageImageComponent(component any, includeEmoji bool) bool {
	className := componentClassName(component)
	typeText := componentTypeText(component)
	if typeText == "image" || className == "imagecomponent" {
		return true
	}
	if !includeEmoji {
		return false
	}
	return typeText == "emoji" || strings.Contains(className, "emoji")
}

// componentClassName 取动态类型名的小写形式
func componentClassName(component any) string {
	componentType := reflect.TypeOf(component)
	for componentType != nil && componentType.Kind() == reflect.Ptr {
		componentType = componentType.Elem()
	}
	if componentType == nil {
		return ""
	}
	return strings.ToLower(componentType.Name())
}

// componentTypeText 对齐 getattr(component, "type", "") 的小写处理
func componentTypeText(component any) string {
	value := reflect.ValueOf(component)
	if field, ok := componentField(value, "Type"); ok {
		field = indirectReflectValue(field)
		if !field.IsValid() {
			return ""
		}
		if field.Kind() == reflect.String {
			return strings.ToLower(field.String())
		}
		if field.CanInterface() {
			return strings.ToLower(pythonStr(field.Interface()))
		}
		return ""
	}
	value = indirectReflectValue(value)
	if value.IsValid() {
		method := value.MethodByName("FormatName")
		if method.IsValid() && method.Type().NumIn() == 0 && method.Type().NumOut() == 1 && method.Type().Out(0).Kind() == reflect.String {
			return strings.ToLower(method.Call(nil)[0].String())
		}
	}
	return ""
}

// nestedComponentSequence 依次查找 forward_components、components、content
func nestedComponentSequence(component any) (reflect.Value, bool) {
	value := indirectReflectValue(reflect.ValueOf(component))
	if !value.IsValid() {
		return reflect.Value{}, false
	}
	for _, name := range []string{"ForwardComponents", "Components", "Content"} {
		field, ok := componentField(value, name)
		if !ok {
			continue
		}
		field = indirectReflectValue(field)
		if !field.IsValid() {
			// Python 对 None 的属性继续尝试下一个字段
			continue
		}
		if (field.Kind() == reflect.Slice || field.Kind() == reflect.Array) && field.Type().Elem().Kind() != reflect.Uint8 {
			return field, true
		}
		return reflect.Value{}, false
	}
	return reflect.Value{}, false
}

// componentField 读取结构体字段、字符串键映射或零参单返回方法
func componentField(value reflect.Value, name string) (reflect.Value, bool) {
	value = indirectReflectValue(value)
	if !value.IsValid() {
		return reflect.Value{}, false
	}
	if value.Kind() == reflect.Map && value.Type().Key().Kind() == reflect.String {
		entry := value.MapIndex(reflect.ValueOf(name).Convert(value.Type().Key()))
		if !entry.IsValid() {
			return reflect.Value{}, false
		}
		return entry, true
	}
	if value.Kind() == reflect.Struct {
		field := value.FieldByName(name)
		if field.IsValid() && field.CanInterface() {
			return field, true
		}
	}
	method := value.MethodByName(name)
	if method.IsValid() && method.Type().NumIn() == 0 && method.Type().NumOut() == 1 {
		return method.Call(nil)[0], true
	}
	return reflect.Value{}, false
}

// indirectReflectValue 解引用指针与接口
func indirectReflectValue(value reflect.Value) reflect.Value {
	for value.IsValid() && (value.Kind() == reflect.Ptr || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return reflect.Value{}
		}
		value = value.Elem()
	}
	return value
}

package onebot

import (
	"strconv"
	"strings"
)

const (
	// QFaceMarker QQ 表情标记的起始，完整形式是 %qface:名称或编号%
	QFaceMarker = "%qface:"
	// QFaceMarkerEnd QQ 表情标记的结束
	QFaceMarkerEnd = "%"
)

// QFaceText 把 QQ 表情编号渲染成 %qface:名称% 标记，编号不在目录内时退回编号
func QFaceText(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if number, err := strconv.Atoi(id); err == nil {
		if name := QQFaceName(number); name != "" {
			return QFaceMarker + name + QFaceMarkerEnd
		}
	}
	return QFaceMarker + id + QFaceMarkerEnd
}

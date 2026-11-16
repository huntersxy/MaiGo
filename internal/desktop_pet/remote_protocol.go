package desktoppet

// 远端桌宠的数据边界校验，对齐 desktop_pet/remote_protocol.py 与 remote_server.py 的校验部分

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"math"
	"regexp"
	"strings"
	"time"
)

const (
	// RemoteProtocolVersion 远端桌宠协议版本
	RemoteProtocolVersion = 1
	// RemoteHeartbeatInterval 心跳间隔（秒）
	RemoteHeartbeatInterval = 20
	// RemoteHeartbeatTimeout 心跳超时（秒）
	RemoteHeartbeatTimeout = 60
	// RemoteMaxEventBytes WebSocket 事件上限
	RemoteMaxEventBytes = 24 * 1024 * 1024
	// RemoteMaxControlBytes 控制消息上限
	RemoteMaxControlBytes = 64 * 1024
	remoteMaxImageBytes   = 8 * 1024 * 1024
)

var (
	remoteRequestIDPattern = regexp.MustCompile("^[0-9a-f]{32}$")
	remoteClientIDPattern  = regexp.MustCompile("^[A-Za-z0-9_-]{1,64}$")
)

// RemoteScreenshot 远端截图
type RemoteScreenshot struct {
	Data       []byte
	CapturedAt string
	Width      int
	Height     int
	SHA256     string
}

// DecodeRemoteObject 解析 JSON 对象；NaN/Inf 由编码层直接拒绝
func DecodeRemoteObject(raw string) (map[string]any, error) {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, fmt.Errorf("请求必须是 JSON 对象")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("请求必须是 JSON 对象")
	}
	return object, nil
}

// ValidateRemoteHello 校验握手事件并返回客户端身份与能力
func ValidateRemoteHello(event map[string]any) (string, map[string]bool, error) {
	if asString(event["type"]) != "hello" {
		return "", nil, fmt.Errorf("远端桌宠协议版本不兼容，需要版本 1")
	}
	version, ok := asInt(event["version"])
	if !ok || version != RemoteProtocolVersion {
		return "", nil, fmt.Errorf("远端桌宠协议版本不兼容，需要版本 1")
	}
	clientID := asString(event["client_id"])
	if !remoteClientIDPattern.MatchString(clientID) {
		return "", nil, fmt.Errorf("客户端 ID 必须为 1～64 位字母、数字、下划线或短横线")
	}
	capabilities, ok := event["capabilities"].(map[string]any)
	if !ok {
		return "", nil, fmt.Errorf("客户端必须明确声明 screen_read 能力")
	}
	screenRead, ok := capabilities["screen_read"].(bool)
	if !ok {
		return "", nil, fmt.Errorf("客户端必须明确声明 screen_read 能力")
	}
	return clientID, map[string]bool{"screen_read": screenRead}, nil
}

// ValidateRemoteEvent 先检查网络事件字段，再交给事件分发器
func ValidateRemoteEvent(event map[string]any) error {
	kind := asString(event["type"])
	switch kind {
	case "ready", "clicked", "buttons_refresh":
		return nil
	case "ack":
		if err := remoteTextField(event, "id", 128, false); err != nil {
			return err
		}
		if raw, exists := event["error"]; exists && raw != nil {
			if _, ok := raw.(string); !ok {
				return fmt.Errorf("ack.error 必须为文本")
			}
		}
		return nil
	case "button_click":
		if err := remoteTextField(event, "plugin_id", 200, false); err != nil {
			return err
		}
		if err := remoteTextField(event, "button_id", 64, false); err != nil {
			return err
		}
		if raw, exists := event["action_id"]; exists && raw != nil {
			if err := remoteTextField(event, "action_id", 64, false); err != nil {
				return err
			}
		}
		return nil
	case "drag_started", "drag_ended":
		if err := remoteTextField(event, "gesture_id", 128, false); err != nil {
			return err
		}
		if kind == "drag_started" {
			for _, name := range []string{"offset_x", "offset_y", "scale"} {
				value, ok := asNumber(event[name])
				if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
					return fmt.Errorf("%s 必须为有限数值", name)
				}
				if name == "scale" && (value <= 0 || value > 20) {
					return fmt.Errorf("scale 必须在 0～20 范围内")
				}
			}
		}
		return nil
	case "screen_action":
		action, err := remoteTextFieldValue(event, "action", 10, false)
		if err != nil {
			return err
		}
		if action != "read" && action != "start" && action != "stop" {
			return fmt.Errorf("未知桌面读取操作")
		}
		return nil
	case "todo_request":
		if err := remoteTextField(event, "request_id", 64, false); err != nil {
			return err
		}
		command, ok := event["command"].(map[string]any)
		if !ok {
			return fmt.Errorf("待办请求缺少 command")
		}
		action := asString(command["action"])
		switch action {
		case "list", "image", "add", "update", "complete", "delete":
		default:
			return fmt.Errorf("不允许窗口调用此事项操作")
		}
		return ValidateRemoteTodoPayload(action, command)
	case "exploration_tasks":
		if err := remoteTextField(event, "task_id", 64, true); err != nil {
			return err
		}
		return remoteTextField(event, "request_id", 64, false)
	case "explore":
		return remoteTextField(event, "goal", 2000, false)
	default:
		return fmt.Errorf("未知的桌宠事件: %s", kind)
	}
}

// ValidateRemoteTodoPayload 校验远端待办命令
func ValidateRemoteTodoPayload(action string, payload map[string]any) error {
	if action == "list" {
		if raw, exists := payload["include_completed"]; exists {
			if _, ok := raw.(bool); !ok {
				return fmt.Errorf("include_completed 必须为布尔值")
			}
		}
		return nil
	}
	if action == "image" {
		return remoteTextField(payload, "image_id", 64, false)
	}
	if action == "update" || action == "complete" || action == "delete" {
		if err := remoteTextField(payload, "todo_id", 128, false); err != nil {
			return err
		}
	}
	if action == "add" {
		if err := remoteTextField(payload, "text", 200, false); err != nil {
			return err
		}
	} else if action == "update" && payload["text"] != nil {
		if err := remoteTextField(payload, "text", 200, false); err != nil {
			return err
		}
	}
	options := map[string]any{}
	if raw, exists := payload["options"]; exists {
		if raw != nil {
			parsed, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("options 必须为 JSON 对象")
			}
			options = parsed
		}
	}
	for _, name := range []string{"condition_type", "condition", "kind"} {
		if raw, exists := options[name]; exists && raw != nil {
			if _, ok := raw.(string); !ok {
				return fmt.Errorf("%s 必须为文本", name)
			}
		}
	}
	return nil
}

// ValidateRemoteChatRequest 校验图文请求，返回带真实 MIME 的规范化图片
func ValidateRemoteChatRequest(payload map[string]any) (map[string]any, error) {
	text, ok := payload["text"].(string)
	if !ok {
		return nil, fmt.Errorf("图文请求必须包含 text 字符串和 images 列表")
	}
	images, ok := payload["images"].([]any)
	if !ok {
		return nil, fmt.Errorf("图文请求必须包含 text 字符串和 images 列表")
	}
	text = strings.TrimSpace(text)
	if len([]rune(text)) > 10000 || len(images) > 4 || (text == "" && len(images) == 0) {
		return nil, fmt.Errorf("最多 10000 字、4 张图片，消息不能为空")
	}
	normalized := make([]any, 0, len(images))
	for _, raw := range images {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("图片必须包含 base64 数据")
		}
		encoded, ok := item["base64"].(string)
		if !ok {
			return nil, fmt.Errorf("图片必须包含 base64 数据")
		}
		if len(encoded) > (remoteMaxImageBytes+2)/3*4 {
			return nil, fmt.Errorf("每张图片不能超过 8 MiB")
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("图片数据无效")
		}
		if len(data) > remoteMaxImageBytes {
			return nil, fmt.Errorf("每张图片不能超过 8 MiB")
		}
		config, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("图片数据无效")
		}
		if format != "png" && format != "jpeg" && format != "webp" && format != "gif" {
			return nil, fmt.Errorf("仅支持 PNG、JPEG、WebP 或 GIF 图片")
		}
		if config.Width*config.Height > 25_000_000 {
			return nil, fmt.Errorf("图片不能超过 2500 万像素")
		}
		normalized = append(normalized, map[string]any{"base64": encoded, "mime_type": imageMIME(format)})
	}
	return map[string]any{"text": text, "images": normalized}, nil
}

// ValidateRemoteScreenshot 校验真实 PNG 与捕获时间，摘要和尺寸由服务端重新计算
func ValidateRemoteScreenshot(payload map[string]any) (RemoteScreenshot, error) {
	encoded, ok := payload["base64"].(string)
	if !ok || len(encoded) > 24*1024*1024 {
		return RemoteScreenshot{}, fmt.Errorf("远端截图超过大小限制")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return RemoteScreenshot{}, fmt.Errorf("远端截图数据无效")
	}
	if len(data) > 16*1024*1024 {
		return RemoteScreenshot{}, fmt.Errorf("远端截图不能超过 16 MiB")
	}
	capturedAt, ok := payload["captured_at"].(string)
	if !ok || len(capturedAt) > 64 {
		return RemoteScreenshot{}, fmt.Errorf("远端截图时间无效")
	}
	parsed, err := time.Parse(time.RFC3339Nano, capturedAt)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, capturedAt)
	}
	if err != nil {
		return RemoteScreenshot{}, fmt.Errorf("远端截图时间必须包含时区")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return RemoteScreenshot{}, fmt.Errorf("远端截图数据无效")
	}
	if format != "png" || maxInt(config.Width, config.Height) > 1920 {
		return RemoteScreenshot{}, fmt.Errorf("远端截图必须为最长边不超过 1920 像素的 PNG")
	}
	sum := sha256.Sum256(data)
	return RemoteScreenshot{
		Data: data, CapturedAt: parsed.Format(time.RFC3339Nano),
		Width: config.Width, Height: config.Height, SHA256: hex.EncodeToString(sum[:]),
	}, nil
}

func remoteTextField(payload map[string]any, name string, limit int, allowEmpty bool) error {
	_, err := remoteTextFieldValue(payload, name, limit, allowEmpty)
	return err
}

func remoteTextFieldValue(payload map[string]any, name string, limit int, allowEmpty bool) (string, error) {
	value, ok := payload[name].(string)
	if !ok || len([]rune(value)) > limit || (!allowEmpty && strings.TrimSpace(value) == "") {
		prefix := "1～"
		if allowEmpty {
			prefix = "最多"
		}
		return "", fmt.Errorf("%s 必须为%s %d 字的文本", name, prefix, limit)
	}
	return value, nil
}

func asInt(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	default:
		return 0, false
	}
}

func imageMIME(format string) string {
	switch format {
	case "png":
		return "image/png"
	case "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	default:
		return ""
	}
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

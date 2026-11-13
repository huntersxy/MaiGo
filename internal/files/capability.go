package filesvc

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// 本文件对应 MaiBot src/files/capability.py：插件能力入口。
//
// 权限声明之外还必须持有当前工具调用的聊天授权——能力本身只校验
// file_scope 作用域凭证，作用域里的 session_id 才决定能访问哪些附件。

const (
	CapFilesList     = "files.list"
	CapFilesReadText = "files.read_text"
	CapFilesDownload = "files.download"
	CapFilesUpload   = "files.upload"
	CapFilesSend     = "files.send"
)

// FileCapability 执行插件文件能力，返回未包装的业务结果
func (s *Service) FileCapability(ctx context.Context, pluginID, capability string, args map[string]any) (any, error) {
	if s == nil {
		return nil, fmt.Errorf("文件服务未就绪")
	}
	if args == nil {
		args = map[string]any{}
	}
	operation, _ := optionalTextArg(args, "operation")
	cancelling := capability == CapFilesUpload && operation == "cancel"
	// 取消上传不要求文件功能处于开启状态，避免关闭能力后残留的预留空间无法释放
	if !cancelling {
		if err := s.requireEnabled(); err != nil {
			return nil, err
		}
	}
	token, err := textArg(args, "file_scope")
	if err != nil {
		return nil, fmt.Errorf("缺少文件作用域")
	}
	sessionID, err := s.ResolvePluginScope(token, pluginID)
	if err != nil {
		return nil, err
	}
	if cancelling {
		uploadToken, err := textArg(args, "upload_token")
		if err != nil {
			return nil, err
		}
		if err := s.PluginCancelUpload(uploadToken, token, pluginID); err != nil {
			return nil, err
		}
		return map[string]any{"cancelled": true}, nil
	}
	switch capability {
	case CapFilesList:
		// 对齐 Python：offset 直接 int(args.get("offset", 0))，不设上界，由 store 分页处理
		offset, err := boundedIntArg(args, "offset", 0, 0, math.MaxInt32)
		if err != nil {
			return nil, err
		}
		return s.PluginList(sessionID, offset)
	case CapFilesReadText:
		fileRef, err := textArg(args, "file_ref")
		if err != nil {
			return nil, err
		}
		offset, err := boundedIntArg(args, "offset", 0, 0, 100000000)
		if err != nil {
			return nil, err
		}
		limit, err := boundedIntArg(args, "limit", 12000, 1, 32000)
		if err != nil {
			return nil, err
		}
		encoding, _ := optionalTextArg(args, "encoding")
		return s.PluginReadText(fileRef, sessionID, offset, limit, encoding)
	case CapFilesDownload:
		fileRef, err := textArg(args, "file_ref")
		if err != nil {
			return nil, err
		}
		return s.PluginDownload(fileRef, sessionID, token)
	case CapFilesUpload:
		name, err := textArg(args, "name")
		if err != nil {
			return nil, err
		}
		mimeType, _ := optionalTextArg(args, "mime_type")
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		return s.PluginBeginUpload(sessionID, name, mimeType, token, pluginID)
	case CapFilesSend:
		fileRef, err := textArg(args, "file_ref")
		if err != nil {
			return nil, err
		}
		return s.PluginSend(ctx, fileRef, sessionID)
	default:
		return nil, fmt.Errorf("未知文件能力")
	}
}

func textArg(args map[string]any, key string) (string, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return "", fmt.Errorf("缺少必要参数 %s", key)
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s 必须是字符串", key)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s 不能为空", key)
	}
	return value, nil
}

func optionalTextArg(args map[string]any, key string) (string, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s 必须是字符串", key)
	}
	return strings.TrimSpace(value), nil
}

func boundedIntArg(args map[string]any, key string, fallback, minValue, maxValue int) (int, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return fallback, nil
	}
	var value int
	switch typed := raw.(type) {
	case float64:
		value = int(typed)
	case float32:
		value = int(typed)
	case int:
		value = typed
	case int64:
		value = int(typed)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, fmt.Errorf("%s 必须是整数", key)
		}
		value = parsed
	default:
		return 0, fmt.Errorf("%s 必须是整数", key)
	}
	if value < minValue || value > maxValue {
		return 0, fmt.Errorf("%s 超出范围", key)
	}
	return value, nil
}

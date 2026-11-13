package memorywriteback

// 图片记忆写回共享实现：组件树遍历、图片二进制读取与单图回填
// 对齐 Python src/A_memorix/core/image/component_paths.py:14-36 与 src/services/memory_flow_service.py:928-991

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"maibot-go/internal/A_memorix"
	imagememory "maibot-go/internal/A_memorix/core/image"
	"maibot-go/internal/common/data_models/messagecomponent"
	"maibot-go/internal/common/database"
)

// ImageWritebackJournalFile 图片写回任务库文件名，对齐 Python image_writeback_journal.py 的 data/image_writeback.sqlite3
const ImageWritebackJournalFile = "image_writeback.sqlite3"

// 图片回填分类，沿用 webui 历史回填接口的既有取值
const (
	ImageWritebackReady         = "ready"
	ImageWritebackMissingSource = "missing_source"
	ImageWritebackMissingChat   = "missing_chat"
	ImageWritebackMissingFile   = "missing_file"
	ImageWritebackInvalidImage  = "invalid_image"
	ImageWritebackWriteFailed   = "write_failed"
	ImageWritebackProcessed     = "processed"
)

// ImageWritebackJournalPath 解析图片写回任务库路径
// Python 侧默认为 ROOT_PATH/data/image_writeback.sqlite3
func ImageWritebackJournalPath(instanceDir string) string {
	root := strings.TrimSpace(instanceDir)
	if root == "" {
		root = memorix.RepoRoot()
	}
	return filepath.Join(root, "data", ImageWritebackJournalFile)
}

// ImageWritebackComponent 持久化消息树中的图片组件
type ImageWritebackComponent struct {
	Path    string
	Hash    string
	Content string
}

// CollectImageWritebackComponents 按持久化组件树生成稳定下标路径
func CollectImageWritebackComponents(components []messagecomponent.Component) []ImageWritebackComponent {
	out := []ImageWritebackComponent{}
	collectImageWritebackComponentsInto(&out, components, "")
	return out
}

// collectImageWritebackComponentsInto 逐层拼接下标，转发节点再按节点下标展开
// 对齐 component_paths.py:22-36 的 prefix 规则
func collectImageWritebackComponentsInto(out *[]ImageWritebackComponent, components []messagecomponent.Component, prefix string) {
	for index, component := range components {
		path := strconv.Itoa(index)
		if prefix != "" {
			path = prefix + "." + strconv.Itoa(index)
		}
		switch typed := component.(type) {
		case messagecomponent.ImageComponent:
			*out = append(*out, ImageWritebackComponent{Path: path, Hash: typed.Hash, Content: typed.Content})
		case *messagecomponent.ImageComponent:
			*out = append(*out, ImageWritebackComponent{Path: path, Hash: typed.Hash, Content: typed.Content})
		case messagecomponent.ForwardNodeComponent:
			for nodeIndex, node := range typed.ForwardComponents {
				collectImageWritebackComponentsInto(out, node.Content, path+"."+strconv.Itoa(nodeIndex))
			}
		case *messagecomponent.ForwardNodeComponent:
			for nodeIndex, node := range typed.ForwardComponents {
				collectImageWritebackComponentsInto(out, node.Content, path+"."+strconv.Itoa(nodeIndex))
			}
		}
	}
}

// ExplicitMessageText 只收集显式文本组件，对齐 Python _explicit_user_text（排除 VLM 描述）
func ExplicitMessageText(components []messagecomponent.Component) string {
	parts := []string{}
	for _, component := range components {
		switch typed := component.(type) {
		case messagecomponent.TextComponent:
			if text := strings.TrimSpace(typed.Text); text != "" {
				parts = append(parts, text)
			}
		case *messagecomponent.TextComponent:
			if text := strings.TrimSpace(typed.Text); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, " ")
}

// ImageWritebackCachedDescription 返回图片的缓存描述，用于生成「[图片：…]」写回文本
type ImageWritebackCachedDescription func(ctx context.Context, imageHash string) string

// BackfillImageComponent 将单张图片写入记忆并回写描述
// preview 为真时只做可写入性检查；cachedDescription 为空时退回组件自带描述
func BackfillImageComponent(
	ctx context.Context,
	runtime *imagememory.ImageMemoryRuntime,
	record *memory.MessageLog,
	sessionID string,
	messageID string,
	component ImageWritebackComponent,
	userStatement string,
	preview bool,
	cachedDescription ImageWritebackCachedDescription,
) (string, string) {
	if messageID == "" {
		return ImageWritebackMissingSource, "缺少来源消息ID"
	}
	if sessionID == "" {
		return ImageWritebackMissingChat, "无法解析已注册的真实聊天流"
	}
	imageBytes, err := ReadMessageImageBytes(record, component.Hash)
	if err != nil {
		return ImageWritebackMissingFile, "图片缓存不存在或无法读取"
	}
	inspected, err := runtime.AssetStore().Inspect(imageBytes)
	if err != nil {
		return ImageWritebackInvalidImage, err.Error()
	}
	if inspected.FrameCount > 1 {
		return ImageWritebackInvalidImage, "仅支持静态图片"
	}
	if preview {
		return ImageWritebackReady, ""
	}
	occurredAt := float64(record.Timestamp.UnixNano()) / 1e9
	result, err := runtime.Ingest(ctx, imagememory.IngestRequest{
		ImageBytes:    imageBytes,
		SourceKind:    "chat",
		ExternalRef:   imagememory.BuildChatExternalRef(sessionID, messageID, component.Path),
		ScopeType:     "chat",
		ChatID:        sessionID,
		MessageID:     messageID,
		ComponentPath: component.Path,
		OccurredAt:    &occurredAt,
		UserStatement: userStatement,
	})
	if err != nil {
		return ImageWritebackWriteFailed, err.Error()
	}
	if !imageWritebackTruthy(result["success"]) {
		failure := imageWritebackString(result["error"])
		if failure == "" {
			failure = "图片记忆写入失败"
		}
		return ImageWritebackWriteFailed, failure
	}
	description := ""
	if cachedDescription != nil {
		if cached := strings.TrimSpace(cachedDescription(ctx, component.Hash)); cached != "" {
			description = "[图片：" + cached + "]"
		}
	}
	if description == "" {
		description = strings.TrimSpace(component.Content)
	}
	if description != "" {
		contentHash := imageWritebackString(result["content_hash"])
		if contentHash == "" {
			contentHash = strings.TrimSpace(component.Hash)
		}
		if _, err := runtime.ApplyModelDescription(contentHash, description); err != nil {
			return ImageWritebackWriteFailed, err.Error()
		}
	}
	return ImageWritebackProcessed, ""
}

// ReadMessageImageBytes 按媒体引用读取图片缓存文件
func ReadMessageImageBytes(record *memory.MessageLog, hash string) ([]byte, error) {
	if record == nil || strings.TrimSpace(hash) == "" {
		return nil, os.ErrNotExist
	}
	for _, ref := range record.MediaRefs {
		if !strings.EqualFold(strings.TrimSpace(ref.Hash), strings.TrimSpace(hash)) {
			continue
		}
		path := strings.TrimSpace(ref.Path)
		if path == "" {
			continue
		}
		return os.ReadFile(path)
	}
	return nil, os.ErrNotExist
}

// imageWritebackTruthy 对齐 Python bool(result.get("success"))
func imageWritebackTruthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case nil:
		return false
	case string:
		trimmed := strings.ToLower(strings.TrimSpace(typed))
		return trimmed != "" && trimmed != "0" && trimmed != "false" && trimmed != "no"
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	default:
		return true
	}
}

// imageWritebackString 取任意值的字符串形式
func imageWritebackString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	case error:
		return typed.Error()
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", value))
	}
}

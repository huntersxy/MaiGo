package visual

import (
	"strings"
	"sync"

	"go.uber.org/zap"

	maisaka "MaiGo/internal/maisaka"
)

// 视觉占位文本
const (
	PendingImagePlaceholder = "[图片，识别中.....]"
	PendingEmojiPlaceholder = "[表情包]"
)

// ImageLookup 图片识别缓存查询端口，由宿主提供 images 表与会话状态
type ImageLookup interface {
	IsVLMTaskConfigured() bool
	LookupCachedImageDescription(imageHash string) string
	LookupCachedEmojiDescription(emojiHash string) string
	IsImageDescriptionPending(imageHash string) bool
	IsEmojiDescriptionPending(emojiHash string) bool
}

// Refresher 视觉占位刷新器；每次刷新读取最新识别结果回填历史组件
type Refresher struct {
	Lookup                ImageLookup
	RebuildHistoryMessage func(message maisaka.ContextMessage) (maisaka.ContextMessage, bool)
}

var (
	plannerPendingImageHashesMu sync.Mutex
	plannerPendingImageHashes   = map[string]struct{}{}
	monitorImageRefreshersMu    sync.Mutex
	monitorImageRefreshers      = map[string][]func(string){}
)

// RefreshChatHistoryVisualPlaceholders 在进入新一轮规划前，尝试用已完成的识图结果刷新历史占位
func (r Refresher) RefreshChatHistoryVisualPlaceholders(chatHistory []maisaka.ContextMessage) int {
	if r.Lookup == nil {
		return 0
	}
	refreshed := 0
	for index, historyMessage := range chatHistory {
		imagesUpdated := r.refreshPendingVisualComponents(historyMessage.Images)
		emojisUpdated := r.refreshPendingEmojiComponents(historyMessage.Emojis)
		if !imagesUpdated && !emojisUpdated {
			continue
		}
		if r.RebuildHistoryMessage != nil {
			if rebuilt, ok := r.RebuildHistoryMessage(historyMessage); ok {
				chatHistory[index] = rebuilt
				refreshed++
				continue
			}
		}
		chatHistory[index] = historyMessage
		refreshed++
	}
	return refreshed
}

func (r Refresher) refreshPendingEmojiComponents(emojis []maisaka.ImageComponent) bool {
	refreshed := false
	for index := range emojis {
		emoji := &emojis[index]
		if !ShouldRefreshEmojiComponent(emoji.Description, emoji.Placeholder) {
			continue
		}
		description := strings.TrimSpace(r.Lookup.LookupCachedEmojiDescription(emoji.RecognitionRef()))
		if description == "" {
			continue
		}
		emoji.Description = EmojiComponentText(emoji.MediaRef, description, "")
		emoji.Placeholder = ""
		refreshed = true
	}
	return refreshed
}

func (r Refresher) refreshPendingVisualComponents(images []maisaka.ImageComponent) bool {
	refreshed := false
	for index := range images {
		image := &images[index]
		if !shouldRefreshImageComponent(*image) {
			continue
		}
		description := strings.TrimSpace(r.Lookup.LookupCachedImageDescription(image.RecognitionRef()))
		if description == "" {
			continue
		}
		image.Description = description
		image.Placeholder = ""
		refreshed = true
	}
	return refreshed
}

// HasPendingImageRecognition 判断历史中是否仍有可等待的图片或表情包识别任务
func (r Refresher) HasPendingImageRecognition(chatHistory []maisaka.ContextMessage) bool {
	if r.Lookup == nil {
		return false
	}
	for _, historyMessage := range chatHistory {
		for _, image := range historyMessage.Images {
			if shouldRefreshImageComponent(image) && r.isImageComponentPending(image) {
				return true
			}
		}
		for _, emoji := range historyMessage.Emojis {
			if ShouldRefreshEmojiComponent(emoji.Description, emoji.Placeholder) && r.Lookup.IsEmojiDescriptionPending(emoji.RecognitionRef()) {
				return true
			}
		}
	}
	return false
}

// LogPendingImageRecognitionBeforeTextPlanner 记录非多模态 planner 开始前仍在等待识别的图片数量
func (r Refresher) LogPendingImageRecognitionBeforeTextPlanner(chatHistory []maisaka.ContextMessage, logPrefix string) int {
	if r.Lookup == nil {
		return 0
	}
	pending := map[string]struct{}{}
	for _, historyMessage := range chatHistory {
		for _, image := range historyMessage.Images {
			if shouldRefreshImageComponent(image) && r.isImageComponentPending(image) {
				pending[image.RecognitionRef()] = struct{}{}
			}
		}
	}
	if len(pending) == 0 {
		return 0
	}
	plannerPendingImageHashesMu.Lock()
	for hash := range pending {
		plannerPendingImageHashes[hash] = struct{}{}
	}
	plannerPendingImageHashesMu.Unlock()
	zap.L().Info("非多模态 planner 开始前仍有图片正在等待识别",
		zap.String("log_prefix", logPrefix),
		zap.Int("pending_count", len(pending)))
	return len(pending)
}

// LogTrackedImageRecognitionCompleted 当 planner 已遇到的待识别图片完成识别时记录一次日志
func LogTrackedImageRecognitionCompleted(imageHash string) {
	if imageHash == "" {
		return
	}
	plannerPendingImageHashesMu.Lock()
	if _, ok := plannerPendingImageHashes[imageHash]; ok {
		delete(plannerPendingImageHashes, imageHash)
		zap.L().Info("非多模态 planner 等待中的图片已完成识别", zap.String("image_hash", imageHash))
	}
	plannerPendingImageHashesMu.Unlock()

	monitorImageRefreshersMu.Lock()
	refreshers := monitorImageRefreshers[imageHash]
	delete(monitorImageRefreshers, imageHash)
	monitorImageRefreshersMu.Unlock()
	for _, refresher := range refreshers {
		refresher(imageHash)
	}
}

// RegisterMonitorImagePlaceholderRefresh 登记图片识别完成后的监控消息刷新回调
func RegisterMonitorImagePlaceholderRefresh(imageHash string, refresher func(string)) {
	if imageHash == "" || refresher == nil {
		return
	}
	monitorImageRefreshersMu.Lock()
	monitorImageRefreshers[imageHash] = append(monitorImageRefreshers[imageHash], refresher)
	monitorImageRefreshersMu.Unlock()
}

func (r Refresher) isImageComponentPending(image maisaka.ImageComponent) bool {
	ref := image.RecognitionRef()
	if ref == "" {
		return false
	}
	if r.Lookup.IsImageDescriptionPending(ref) {
		return true
	}
	// 对齐 Python：只有本地持有字节且无缓存描述时才算可等待
	return image.HasBinary && r.Lookup.LookupCachedImageDescription(ref) == ""
}

func shouldRefreshImageComponent(image maisaka.ImageComponent) bool {
	if strings.TrimSpace(image.Description) != "" {
		return false
	}
	placeholder := strings.TrimSpace(image.Placeholder)
	return placeholder == "" || placeholder == PendingImagePlaceholder
}

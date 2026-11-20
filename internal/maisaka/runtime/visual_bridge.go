package runtime

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"MaiGo/internal/chat/image_system"
	"MaiGo/internal/chat/replyer"
	"MaiGo/internal/emoji_system"
	"MaiGo/internal/maisaka"
	"MaiGo/internal/maisaka/visual"
	"MaiGo/internal/prompt"
)

// agentVisualBridge 把 Maisaka 视觉刷新端口接到 images 表与 VLM 描述管理器
type agentVisualBridge struct {
	agent *Agent

	once      sync.Once
	manager   *imagesystem.Manager
	refresher visual.Refresher
	err       error
	hashCache sync.Map
	download  func(context.Context, string) ([]byte, error)
}

// agentVisual 返回进程内共享的视觉桥接器；首次调用时按需初始化
func (a *Agent) agentVisual() *agentVisualBridge {
	if a == nil {
		return nil
	}
	a.visualOnce.Do(func() {
		bridge := &agentVisualBridge{agent: a}
		bridge.initialize()
		a.visual = bridge
	})
	return a.visual
}

func (b *agentVisualBridge) initialize() {
	if b == nil || b.agent == nil || b.agent.memory == nil {
		return
	}
	db := b.agent.memory.GetDB()
	if db == nil {
		b.err = errAgentVisualNoDB
		return
	}
	store := imagesystem.NewGORMStore(db)
	if err := store.AutoMigrate(); err != nil {
		zap.L().Warn("初始化视觉桥接时迁移 images 表失败", zap.Error(err))
	}
	prompts, err := prompt.NewManager()
	if err != nil {
		b.err = err
		zap.L().Warn("初始化视觉桥接时加载提示词失败", zap.Error(err))
		return
	}
	if instance := b.agent.instanceConfigSnapshot(); instance != nil && strings.TrimSpace(instance.Dir) != "" {
		if err := prompts.LoadFromDir(prompt.CustomPromptsRoot(instance.Dir)); err != nil {
			zap.L().Warn("加载实例图片描述提示词失败", zap.Error(err))
		}
	}
	root := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR"))
	if root == "" {
		root, _ = os.Getwd()
	}
	manager, err := imagesystem.NewManager(imagesystem.ManagerOptions{
		Store:             store,
		Root:              root,
		Describer:         &imagesystem.RouterDescriber{Router: b.agent.llmRouterSnapshot(), Prompts: prompts},
		SkipLegacyCleanup: true,
	})
	if err != nil {
		b.err = err
		zap.L().Warn("初始化图片管理器失败", zap.Error(err))
		return
	}
	b.manager = manager
	b.refresher = visual.Refresher{Lookup: b}
}

// IsVLMTaskConfigured 是否配置了可用的视觉模型
func (b *agentVisualBridge) IsVLMTaskConfigured() bool {
	return b != nil && b.manager != nil && b.manager.VLMAvailable()
}

// LookupCachedImageDescription 读取图片描述，入参支持 hash、base64、data URL 与文件路径
func (b *agentVisualBridge) LookupCachedImageDescription(imageHash string) string {
	if b == nil || b.manager == nil {
		return ""
	}
	hash := b.imageHash(imageHash)
	if hash == "" {
		return ""
	}
	return b.manager.GetCachedImageDescription(context.Background(), hash)
}

// LookupCachedEmojiDescription 读取表情描述
func (b *agentVisualBridge) LookupCachedEmojiDescription(emojiHash string) string {
	if b == nil || b.manager == nil {
		return ""
	}
	hash := b.imageHash(emojiHash)
	if hash == "" {
		return ""
	}
	return b.manager.GetCachedEmojiDescription(context.Background(), hash)
}

func (b *agentVisualBridge) IsEmojiDescriptionPending(ref string) bool {
	return b != nil && emojisystem.DefaultRegistry().IsDescriptionPending(b.imageHash(ref))
}

// IsImageDescriptionPending 图片是否仍在等待识别
func (b *agentVisualBridge) IsImageDescriptionPending(imageHash string) bool {
	if b == nil || b.manager == nil {
		return false
	}
	hash := b.imageHash(imageHash)
	if hash == "" {
		return false
	}
	return b.manager.IsImageDescriptionPending(context.Background(), hash)
}

// RefreshChatHistoryVisualPlaceholders 刷新 Maisaka 历史中的图片占位
func (b *agentVisualBridge) RefreshChatHistoryVisualPlaceholders(chatHistory []maisaka.ContextMessage) int {
	if b == nil {
		return 0
	}
	return b.refresher.RefreshChatHistoryVisualPlaceholders(chatHistory)
}

// HasPendingImageRecognition 历史中是否仍有可等待的图片识别
func (b *agentVisualBridge) HasPendingImageRecognition(chatHistory []maisaka.ContextMessage) bool {
	if b == nil {
		return false
	}
	return b.refresher.HasPendingImageRecognition(chatHistory)
}

// ImageRecognitionWaitSeconds 文本 planner 进入前等待识图的秒数
func (b *agentVisualBridge) ImageRecognitionWaitSeconds(waitForTextModel bool) float64 {
	if b == nil || b.agent == nil {
		return 0
	}
	if !waitForTextModel && b.visualPlannerEnabled() {
		return 0
	}
	instance := b.agent.instanceConfigSnapshot()
	if instance == nil {
		return 0
	}
	seconds := instance.Bot.Visual.WaitImageRecognizeMaxTime
	if seconds < 0 {
		return 0
	}
	return seconds
}

func (b *agentVisualBridge) visualPlannerEnabled() bool {
	if b == nil {
		return false
	}
	return b.agent.plannerVisualEnabled()
}

// plannerVisualEnabled 对齐 resolve_enable_visual_planner，只依赖实例模型配置不触碰视觉管理器
func (a *Agent) plannerVisualEnabled() bool {
	if a == nil {
		return false
	}
	instance := a.instanceConfigSnapshot()
	if instance == nil {
		return false
	}
	models := make([]visual.ModelInfo, 0, len(instance.Model.Models))
	for _, model := range instance.Model.Models {
		models = append(models, visual.ModelInfo{Name: model.Name, Visual: model.Visual})
	}
	return visual.ResolveEnableVisualPlanner(instance.Model.ModelTaskConfig.Planner.ModelList, models)
}

// ScheduleToolResultMediaImageRecognition 为工具结果图片登记后台识图
func (b *agentVisualBridge) ScheduleToolResultMediaImageRecognition(images []maisaka.ImageComponent, mediaIndex string) {
	if b == nil || b.manager == nil {
		return
	}
	for _, image := range images {
		ref := strings.TrimSpace(image.MediaRef)
		if ref == "" {
			continue
		}
		data := decodeAgentImageData(ref)
		hash := ""
		if len(data) > 0 {
			hash = imageSHA256(data)
			b.hashCache.Store(ref, hash)
		}
		if len(data) == 0 && isHTTPImageRef(ref) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			downloaded, err := b.downloadImage(ctx, ref)
			cancel()
			if err != nil {
				zap.L().Debug("下载工具结果图片失败",
					zap.String("media_index", mediaIndex), zap.String("url", ref), zap.Error(err))
				continue
			}
			data = downloaded
			hash = imageSHA256(data)
			b.hashCache.Store(ref, hash)
		}
		if hash == "" {
			hash = b.imageHash(ref)
		}
		if hash == "" {
			continue
		}
		if description := b.manager.GetCachedImageDescription(context.Background(), hash); strings.TrimSpace(description) != "" {
			continue
		}
		if _, err := b.manager.GetImageDescription(context.Background(), hash, data, false); err != nil {
			zap.L().Debug("登记工具结果图片识别失败",
				zap.String("media_index", mediaIndex), zap.String("hash", hash), zap.Error(err))
		}
	}
}

// ScheduleToolResultMediaEmojiRecognition 为工具结果表情登记后台识别
func (b *agentVisualBridge) ScheduleToolResultMediaEmojiRecognition(emojis []maisaka.ImageComponent, mediaIndex string) {
	if b == nil {
		return
	}
	registry := emojisystem.DefaultRegistry()
	for _, emoji := range emojis {
		ref := strings.TrimSpace(emoji.MediaRef)
		if ref == "" {
			continue
		}
		data := decodeAgentImageData(ref)
		hash := ""
		if len(data) > 0 {
			hash = imageSHA256(data)
			b.hashCache.Store(ref, hash)
		}
		if len(data) == 0 && isHTTPImageRef(ref) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			downloaded, err := b.downloadImage(ctx, ref)
			cancel()
			if err != nil {
				zap.L().Debug("下载工具结果表情失败",
					zap.String("media_index", mediaIndex), zap.String("url", ref), zap.Error(err))
				continue
			}
			data = downloaded
			hash = imageSHA256(data)
			b.hashCache.Store(ref, hash)
		}
		if hash == "" {
			hash = b.imageHash(ref)
		}
		if hash == "" {
			continue
		}
		if _, _, err := registry.GetEmojiDescription(context.Background(), data, hash, false); err != nil {
			zap.L().Debug("登记工具结果表情识别失败",
				zap.String("media_index", mediaIndex), zap.String("hash", hash), zap.Error(err))
		}
	}
}

// refreshReplyerVisualPlaceholders 回复生成前刷新 Replyer 历史里的图片/表情描述；文本模型会等待识图完成
func (b *agentVisualBridge) refreshReplyerVisualPlaceholders(ctx context.Context, history []replyer.ChatMessage) int {
	if b == nil || !b.IsVLMTaskConfigured() {
		return 0
	}
	refreshed := b.refreshReplyerVisualPlaceholdersOnce(history)
	waitSeconds := b.ImageRecognitionWaitSeconds(true)
	if waitSeconds <= 0 || !b.hasPendingReplyerVisual(history) {
		return refreshed
	}
	deadline := time.Now().Add(time.Duration(waitSeconds * float64(time.Second)))
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining > 200*time.Millisecond {
			remaining = 200 * time.Millisecond
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return refreshed
		case <-timer.C:
		}
		if changed := b.refreshReplyerVisualPlaceholdersOnce(history); changed > 0 {
			refreshed += changed
		}
		if !b.hasPendingReplyerVisual(history) {
			break
		}
	}
	return refreshed
}

func (b *agentVisualBridge) refreshReplyerVisualPlaceholdersOnce(history []replyer.ChatMessage) int {
	refreshed := 0
	for index := range history {
		parts := history[index].Parts
		for partIndex := range parts {
			part := &parts[partIndex]
			if part.Kind != replyer.PartImage && part.Kind != replyer.PartEmoji {
				continue
			}
			ref := firstNonEmptyString(part.ImageRecognitionHash, part.ImageBase64)
			var description string
			if part.Kind == replyer.PartEmoji {
				description = strings.TrimSpace(b.LookupCachedEmojiDescription(ref))
			} else {
				description = strings.TrimSpace(b.LookupCachedImageDescription(ref))
			}
			if description == "" {
				continue
			}
			if part.Kind == replyer.PartEmoji {
				if !visual.ShouldRefreshEmojiComponent(part.Description, "") {
					continue
				}
				part.Description = visual.EmojiComponentText("", description, "")
				refreshed++
				continue
			}
			current := strings.TrimSpace(part.Description)
			if current != "" && current != visual.PendingImagePlaceholder && current != "[图片]" {
				continue
			}
			part.Description = description
			refreshed++
		}
		history[index].Parts = parts
	}
	return refreshed
}

func (b *agentVisualBridge) hasPendingReplyerVisual(history []replyer.ChatMessage) bool {
	for _, message := range history {
		for _, part := range message.Parts {
			if part.Kind != replyer.PartImage && part.Kind != replyer.PartEmoji {
				continue
			}
			if strings.TrimSpace(part.Description) != "" {
				continue
			}
			if part.Kind == replyer.PartEmoji {
				if b.IsEmojiDescriptionPending(firstNonEmptyString(part.ImageRecognitionHash, part.ImageBase64)) {
					return true
				}
				continue
			} else if b.LookupCachedImageDescription(firstNonEmptyString(part.ImageRecognitionHash, part.ImageBase64)) != "" {
				continue
			}
			return true
		}
	}
	return false
}

var errAgentVisualNoDB = &agentVisualError{message: "视觉桥接缺少数据库连接"}

type agentVisualError struct{ message string }

func (e *agentVisualError) Error() string { return e.message }

// imageHash 优先读取下载后缓存的真实内容 hash，未命中时回退到引用归一化
func (b *agentVisualBridge) imageHash(ref string) string {
	if b != nil {
		if cached, ok := b.hashCache.Load(strings.TrimSpace(ref)); ok {
			if hash, ok := cached.(string); ok && hash != "" {
				return hash
			}
		}
	}
	return agentVisualHash(ref)
}

func isHTTPImageRef(ref string) bool {
	return strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://")
}

// downloadImage 下载工具结果图片，支持测试注入
func (b *agentVisualBridge) downloadImage(ctx context.Context, ref string) ([]byte, error) {
	if b != nil && b.download != nil {
		return b.download(ctx, ref)
	}
	return downloadAgentImage(ctx, ref)
}

const maxAgentImageDownloadBytes = 20 << 20

func downloadAgentImage(ctx context.Context, ref string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, ref, nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("下载图片失败：HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxAgentImageDownloadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAgentImageDownloadBytes {
		return nil, fmt.Errorf("图片超过 %d 字节上限", maxAgentImageDownloadBytes)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("图片内容为空")
	}
	return data, nil
}

// agentVisualHash 把内容引用归一化为 images 表使用的 sha256
func agentVisualHash(ref string) string {
	raw := strings.TrimSpace(ref)
	if raw == "" {
		return ""
	}
	if len(raw) == 64 {
		if _, err := hex.DecodeString(raw); err == nil {
			return strings.ToLower(raw)
		}
	}
	if strings.HasPrefix(raw, "data:") {
		if comma := strings.Index(raw, ","); comma >= 0 {
			raw = raw[comma+1:]
		}
	}
	raw = strings.TrimPrefix(raw, "base64://")
	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil && len(decoded) > 0 {
		return imageSHA256(decoded)
	}
	if data, err := os.ReadFile(raw); err == nil && len(data) > 0 {
		return imageSHA256(data)
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return imageSHA256([]byte(raw))
	}
	return ""
}

// decodeAgentImageData 解码 base64/data URL，无法解码时返回 nil
func decodeAgentImageData(ref string) []byte {
	raw := strings.TrimSpace(ref)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "data:") {
		if comma := strings.Index(raw, ","); comma >= 0 {
			raw = raw[comma+1:]
		}
	}
	raw = strings.TrimPrefix(raw, "base64://")
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(decoded) == 0 {
		return nil
	}
	return decoded
}

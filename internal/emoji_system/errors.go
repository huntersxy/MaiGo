package emojisystem

import "errors"

var (
	errMemoryUnavailable      = errors.New("emojisystem: 记忆管理器未注入")
	errInvalidHash            = errors.New("emojisystem: 表情哈希无效")
	errEmojiNotFound          = errors.New("emojisystem: 表情包记录不存在")
	errEmptyPool              = errors.New("emojisystem: 表情包库为空")
	errEmptyEmotion           = errors.New("emojisystem: 情绪标签为空")
	errNoMatch                = errors.New("emojisystem: 无匹配表情")
	errVLMNotConfigured       = errors.New("emojisystem: 未配置 VLM 任务模型")
	errVLMUnavailable         = errors.New("emojisystem: VLM 路由器未注入")
	errPromptUnavailable      = errors.New("emojisystem: Prompt 管理器未注入")
	errDescribeDeps           = errors.New("emojisystem: VLM 描述依赖未就绪（sticker 目录 / prompt / router）")
	errStickerFileMissing     = errors.New("emojisystem: 表情本地文件不存在")
	errEmptyVLMDescription    = errors.New("emojisystem: VLM 返回空描述")
	errEmptyReviewResponse    = errors.New("emojisystem: VLM 审核返回空内容")
	errEmojiTaskNotConfigured = errors.New("emojisystem: 未配置 emoji/planner 任务模型")
	errNoEmojiAvailable       = errors.New("emojisystem: 当前表情包库中没有可用表情")

	errDescriptionHookAborted = errors.New("emojisystem: 表情包描述被 Hook 中止注册")
	errHookEmptyDescription   = errors.New("emojisystem: Hook 返回空描述，拒绝注册")
)

// ErrVLMNotConfigured 未配置 model_task_config.vlm 时 Describe 返回此错误
var ErrVLMNotConfigured = errVLMNotConfigured

// ErrEmojiInvalidImage 上传内容不是可识别的图片
var ErrEmojiInvalidImage = errors.New("emojisystem: 无效的图片文件")

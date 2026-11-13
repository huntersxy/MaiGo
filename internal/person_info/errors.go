package personinfo

import "errors"

var (
	// ErrMemoryUnavailable 记忆管理器未注入
	ErrMemoryUnavailable = errors.New("personinfo: 记忆管理器未注入")
	errMemoryUnavailable = ErrMemoryUnavailable
	// ErrPersonNotFound 未找到 person_info 行
	ErrPersonNotFound       = errors.New("personinfo: 未找到人物记录")
	errInvalidRegisterInput = errors.New("personinfo: platform、user_id、nickname 为必填")
	errPersonNotFound       = ErrPersonNotFound
	// ErrMemorixUnavailable Memorix 宿主未接线
	ErrMemorixUnavailable = errors.New("personinfo: Memorix 宿主未接线")
	// ErrProfileAdminUnavailable 宿主不支持画像管理
	ErrProfileAdminUnavailable = errors.New("personinfo: Memorix 画像管理未接线")
	// ErrProfileQueryTargetMissing 画像查询缺少目标
	ErrProfileQueryTargetMissing = errors.New("personinfo: 画像查询至少需要 person_id 或 person_name")
	// ErrWritebackQueueFull 人物事实写回队列已满
	ErrWritebackQueueFull = errors.New("personinfo: 人物事实写回队列已满")
	// ErrWritebackQueueClosed 人物事实写回队列已关闭
	ErrWritebackQueueClosed = errors.New("personinfo: 人物事实写回队列已关闭")
)

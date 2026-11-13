package memruntime

// A_memorix 运行时数据模型，对齐 Python core/runtime/models.py

// KernelSearchRequest 对应内核搜索请求
type KernelSearchRequest struct {
	Query         string
	Limit         int
	Mode          string
	ChatID        string
	SharedChatIDs []string
	PersonID      string
	TimeStart     any
	TimeEnd       any
	// RespectFilter 为空表示未设置，按 Python 默认 true 处理
	RespectFilter *bool
	UserID        string
	GroupID       string
}

// NewKernelSearchRequest 返回与 Python dataclass 默认值一致的请求
func NewKernelSearchRequest() KernelSearchRequest {
	enabled := true
	return KernelSearchRequest{Limit: 5, Mode: "search", RespectFilter: &enabled}
}

// NormalizedSearchTimeWindow 对应 _NormalizedSearchTimeWindow
type NormalizedSearchTimeWindow struct {
	NumericStart *float64
	NumericEnd   *float64
	QueryStart   *string
	QueryEnd     *string
}

// RespectFilterEnabled 返回过滤开关，未设置时按 Python 默认 true
func (r KernelSearchRequest) RespectFilterEnabled() bool {
	if r.RespectFilter == nil {
		return true
	}
	return *r.RespectFilter
}

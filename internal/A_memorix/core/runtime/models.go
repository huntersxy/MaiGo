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
	RespectFilter bool
	UserID        string
	GroupID       string
}

// NewKernelSearchRequest 返回与 Python dataclass 默认值一致的请求
func NewKernelSearchRequest() KernelSearchRequest {
	return KernelSearchRequest{Limit: 5, Mode: "search", RespectFilter: true}
}

// NormalizedSearchTimeWindow 对应 _NormalizedSearchTimeWindow
type NormalizedSearchTimeWindow struct {
	NumericStart *float64
	NumericEnd   *float64
	QueryStart   *string
	QueryEnd     *string
}

package focus

import "github.com/google/uuid"

// newRunID 生成一次运行或等待的随机编号
func newRunID() string {
	return uuid.NewString()
}

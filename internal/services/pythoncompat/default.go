package pythoncompat

import "sync"

var (
	defaultOnce   sync.Once
	defaultClient *Client
)

// DefaultClient 进程级侧车客户端，按 MAIBOT_COMPAT_* 环境变量构造一次。
//
// 返回的 client 即使未配置也可安全调用（方法返回 ErrNotConfigured），
// 用 Configured() 区分「可用」与「未配置」。
func DefaultClient() *Client {
	defaultOnce.Do(func() {
		defaultClient = NewClient(LoadConfigFromEnv())
	})
	return defaultClient
}

// SetDefaultClient 覆盖进程级客户端；测试注入用，传 nil 恢复按环境变量构造
func SetDefaultClient(client *Client) {
	defaultClient = client
}

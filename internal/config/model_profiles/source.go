package modelprofiles

import "context"

// Source 供 llm.Router 解析模型档案（Store 或 CLI 静态档案）
type Source interface {
	Active(ctx context.Context) (Profile, error)
	List(ctx context.Context) ([]Profile, error)
	DecryptKey(row Profile) (string, error)
}

var _ Source = (*Store)(nil)

package native

// 服务层端口适配，对齐 services.Base 期望的 Metadata / GraphStore 契约
// 只处理与 storage 类型签名不一致的两个方法，其余方法由内嵌指针提升

import (
	"maibot-go/internal/A_memorix/core/runtime/services"
	"maibot-go/internal/A_memorix/core/storage"
)

// metadataPort 把元数据存储适配为 services.Metadata
type metadataPort struct {
	*storage.Store
}

// RebuildRelationHashAliases 把 storage 内部结果类型收敛为 any
func (p metadataPort) RebuildRelationHashAliases(conn *storage.Conn) (any, error) {
	if p.Store == nil {
		return nil, nil
	}
	return p.Store.RebuildRelationHashAliases(conn)
}

// graphPort 把图存储适配为 services.GraphStore
type graphPort struct {
	*storage.GraphStore
}

// Clear 对齐 services.GraphStore 的整型返回值
func (p graphPort) Clear() int {
	if p.GraphStore == nil {
		return 0
	}
	p.GraphStore.Clear()
	return 0
}

// newMetadataPort 返回服务层元数据端口，nil 存储返回 nil 接口
func newMetadataPort(store *storage.Store) services.Metadata {
	if store == nil {
		return nil
	}
	return metadataPort{Store: store}
}

// newGraphPort 返回服务层图存储端口，nil 存储返回 nil 接口
func newGraphPort(store *storage.GraphStore) services.GraphStore {
	if store == nil {
		return nil
	}
	return graphPort{GraphStore: store}
}

package memruntime

// 向量重建与向量空间状态的读写入口，供原生服务依赖复用
// 字段本身由内核持有，随内核生命周期存续

import "sync"

// SetEmbeddingDimension 写入当前 embedding 维度
func (k *Kernel) SetEmbeddingDimension(dimension int) {
	if dimension > 0 {
		k.stateMu.Lock()
		k.embeddingDimension = dimension
		k.stateMu.Unlock()
	}
}

// VectorRebuildLock 返回向量重建互斥锁
func (k *Kernel) VectorRebuildLock() *sync.Mutex {
	return &k.vectorRebuildLock
}

// SetVectorPersistBlockedUntilRebuild 标记向量持久化是否等待重建
func (k *Kernel) SetVectorPersistBlockedUntilRebuild(blocked bool) {
	k.stateMu.Lock()
	k.vectorPersistBlocked = blocked
	k.stateMu.Unlock()
}

// VectorPersistBlockedUntilRebuild 读取向量持久化阻塞标记
func (k *Kernel) VectorPersistBlockedUntilRebuild() bool {
	k.stateMu.RLock()
	defer k.stateMu.RUnlock()
	return k.vectorPersistBlocked
}

// SetVectorRebuildSourceDimension 记录重建前的已存向量维度
func (k *Kernel) SetVectorRebuildSourceDimension(dimension int) {
	value := dimension
	k.stateMu.Lock()
	k.vectorRebuildSourceDim = &value
	k.stateMu.Unlock()
}

// VectorRebuildSourceDimension 读取重建前的已存向量维度
func (k *Kernel) VectorRebuildSourceDimension() *int {
	k.stateMu.RLock()
	defer k.stateMu.RUnlock()
	if k.vectorRebuildSourceDim == nil {
		return nil
	}
	value := *k.vectorRebuildSourceDim
	return &value
}

// ActiveVectorSpaceID 读取当前活动向量空间 id
func (k *Kernel) ActiveVectorSpaceID() string {
	k.stateMu.RLock()
	defer k.stateMu.RUnlock()
	return k.activeVectorSpaceID
}

// SetActiveVectorSpaceID 写入当前活动向量空间 id
func (k *Kernel) SetActiveVectorSpaceID(spaceID string) {
	k.stateMu.Lock()
	k.activeVectorSpaceID = spaceID
	k.stateMu.Unlock()
}

// TargetVectorSpaceID 读取正在同步的目标向量空间 id，对齐 _target_vector_space_id
func (k *Kernel) TargetVectorSpaceID() string {
	k.stateMu.RLock()
	defer k.stateMu.RUnlock()
	return k.targetVectorSpaceID
}

// SetTargetVectorSpaceID 写入正在同步的目标向量空间 id
func (k *Kernel) SetTargetVectorSpaceID(spaceID string) {
	k.stateMu.Lock()
	k.targetVectorSpaceID = spaceID
	k.stateMu.Unlock()
}

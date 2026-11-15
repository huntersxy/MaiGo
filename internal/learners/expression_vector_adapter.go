package learners

// 表达向量索引的适配层。
//
// 索引实现位于 internal/chat/replyer/expressionvector，与 MaiBot
// src/chat/replyer/expression_vector_index.py 的归属一致。
// 这里保留 learners 的导出名，同时把 Registry 的能力以 Port 形式提供给
// 向量包，避免反向依赖。

import (
	"context"
	"fmt"
	"strings"

	"MaiGo/internal/chat/replyer/expressionvector"
)

// 索引类型的对外别名，保持 learners 既有 API 不变
type (
	// ExpressionVectorStore 向量索引存储
	ExpressionVectorStore = expressionvector.ExpressionVectorStore
	// ExpressionVectorFilter TopK 检索的会话范围
	ExpressionVectorFilter = expressionvector.ExpressionVectorFilter
	// ExpressionVectorHit 一次向量检索的候选命中
	ExpressionVectorHit = expressionvector.ExpressionVectorHit
	// ExpressionVectorMetadata 写入索引时随附的表达元数据
	ExpressionVectorMetadata = expressionvector.ExpressionVectorMetadata
	// ExpressionVectorSyncStatus 索引同步状态
	ExpressionVectorSyncStatus = expressionvector.ExpressionVectorSyncStatus
	// ExpressionVectorRuntimeProfile 运行期 profile
	ExpressionVectorRuntimeProfile = expressionvector.ExpressionVectorRuntimeProfile
	// ExpressionVectorSpacesOptions 空间列表选项
	ExpressionVectorSpacesOptions = expressionvector.ExpressionVectorSpacesOptions
	// ExpressionVectorSpaceDeleteStatus 空间删除状态
	ExpressionVectorSpaceDeleteStatus = expressionvector.ExpressionVectorSpaceDeleteStatus
	// ExpressionBackfillOptions 历史回填选项
	ExpressionBackfillOptions = expressionvector.ExpressionBackfillOptions
	// ExpressionEmbeddingProfile 嵌入后端标记
	ExpressionEmbeddingProfile = expressionvector.ExpressionEmbeddingProfile
	// PersistentExpressionVectorStore 持久化索引
	PersistentExpressionVectorStore = expressionvector.PersistentExpressionVectorStore
)

// 索引构造与工具函数转发
var (
	// NewInMemoryExpressionVectorStore 构造内存索引
	NewInMemoryExpressionVectorStore = expressionvector.NewInMemoryExpressionVectorStore
	// LoadExpressionVectorSpacePayload 读取向量空间负载
	LoadExpressionVectorSpacePayload = expressionvector.LoadExpressionVectorSpacePayload
	// DeleteExpressionVectorSpaceFile 删除向量空间文件
	DeleteExpressionVectorSpaceFile = expressionvector.DeleteExpressionVectorSpaceFile
	// BuildExpressionVectorSpaces 构建空间列表
	BuildExpressionVectorSpaces = expressionvector.BuildExpressionVectorSpaces
)

// selectExpressionsByVector 在候选集上做向量召回，供 MatchExpressions 使用
//
// valid 是 learners 的表达式记录，转换成向量包的 Record 后调用。
func selectExpressionsByVector(
	ctx context.Context,
	reg *Registry,
	valid []ExpressionRecord,
	query string,
	filter ExpressionVectorFilter,
	candidatePoolSize ...int,
) []ExpressionRecord {
	if reg == nil {
		return nil
	}
	records := make([]expressionvector.Record, 0, len(valid))
	for _, item := range valid {
		modifiedBy := ""
		if item.ModifiedBy != nil {
			modifiedBy = string(*item.ModifiedBy)
		}
		records = append(records, expressionvector.Record{
			ID: item.ID, Situation: item.Situation, Style: item.Style,
			Count: item.Count, Checked: item.Checked, SessionID: item.SessionID,
			ModifiedBy: modifiedBy,
		})
	}
	picked := expressionvector.SelectExpressionsByVector(ctx, reg, records, query, filter, candidatePoolSize...)
	out := make([]ExpressionRecord, 0, len(picked))
	byID := make(map[uint]ExpressionRecord, len(valid))
	for _, item := range valid {
		byID[item.ID] = item
	}
	for _, item := range picked {
		if original, ok := byID[item.ID]; ok {
			out = append(out, original)
		}
	}
	return out
}

// IndexExpressionRecord 写入单条表达方式的向量
//
// 把 learners 的表达式记录转成向量包的 Record 后调用。
func IndexExpressionRecord(ctx context.Context, reg *Registry, rec ExpressionRecord) {
	if reg == nil {
		return
	}
	modifiedBy := ""
	if rec.ModifiedBy != nil {
		modifiedBy = string(*rec.ModifiedBy)
	}
	expressionvector.IndexExpressionRecord(ctx, reg, expressionvector.Record{
		ID: rec.ID, Situation: rec.Situation, Style: rec.Style,
		Count: rec.Count, Checked: rec.Checked, SessionID: rec.SessionID,
		ModifiedBy: modifiedBy,
	})
}

// RemoveExpressionVector 从索引删除表达方式
func RemoveExpressionVector(reg *Registry, id uint) {
	if reg == nil {
		return
	}
	expressionvector.RemoveExpressionVector(reg, id)
}

// TextEmbeddingProbe 一条探针的嵌入结果与来源身份
type TextEmbeddingProbe = expressionvector.TextEmbeddingProbe

// TextEmbeddingProfiler embedding 客户端可返回模型身份与探针向量
type TextEmbeddingProfiler = expressionvector.TextEmbeddingProfiler

// StartExpressionVectorBackfill 启动表达向量历史补建后台循环
func (r *Registry) StartExpressionVectorBackfill(parent context.Context, opts ExpressionBackfillOptions) error {
	if r == nil {
		return fmt.Errorf("learners registry 为空")
	}
	if parent == nil {
		parent = context.Background()
	}
	settings := r.Settings()
	if !settings.UseVectorExpression {
		_ = r.StopExpressionVectorBackfill(context.Background())
		return nil
	}
	settingsSignature := expressionvector.SettingsSignatureOf(settings)
	routeSignature := strings.TrimSpace(opts.RouteSignature)
	r.mu.Lock()
	if runner := r.expressionBackfill; runner != nil {
		changed := runner.SettingsSignature() != settingsSignature ||
			(routeSignature != "" && runner.RouteSignature() != routeSignature)
		if changed {
			runner.UpdateSignatures(settingsSignature, routeSignature)
		}
		r.mu.Unlock()
		if changed {
			runner.Notify()
		}
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	runner := expressionvector.NewBackfillRunnerWith(
		r, opts, ctx, cancel, make(chan struct{}), make(chan struct{}, 1),
		settingsSignature, routeSignature,
	)
	r.expressionBackfill = runner
	r.mu.Unlock()
	go runner.Run()
	return nil
}

// StopExpressionVectorBackfill 取消后台循环并等待退出
func (r *Registry) StopExpressionVectorBackfill(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	runner := r.expressionBackfill
	r.expressionBackfill = nil
	r.mu.Unlock()
	if runner == nil {
		return nil
	}
	runner.Cancel()
	if ctx == nil {
		<-runner.Done()
		return nil
	}
	select {
	case <-runner.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// NotifyExpressionVectorBackfill 通知配置或模型已变化，唤醒并重新标定 profile
func (r *Registry) NotifyExpressionVectorBackfill() {
	if r == nil {
		return
	}
	r.mu.RLock()
	runner := r.expressionBackfill
	r.mu.RUnlock()
	if runner != nil {
		runner.Notify()
	}
}

// LearningSettingsToVectorSettings 把 learners 配置转成向量包配置
func LearningSettingsToVectorSettings(settings LearningSettings) expressionvector.Settings {
	return expressionvector.Settings{
		UseVectorExpression:       settings.UseVectorExpression,
		ExpressionVectorIndexPath: settings.ExpressionVectorIndexPath,
		VectorCandidatePoolSize:   settings.VectorCandidatePoolSize,
	}
}

// VectorSettingsProvider 返回向量包可直接使用的配置加载器
func (r *Registry) VectorSettingsProvider() func() (expressionvector.Settings, error) {
	return func() (expressionvector.Settings, error) {
		settings, err := r.LearningSettingsWithError()
		if err != nil {
			return expressionvector.Settings{}, err
		}
		return LearningSettingsToVectorSettings(settings), nil
	}
}

// 向量空间相关常量与状态，保持 learners 既有 API
const (
	ExpressionVectorSpaceDeleted  = expressionvector.ExpressionVectorSpaceDeleted
	ExpressionVectorSpaceInUse    = expressionvector.ExpressionVectorSpaceInUse
	ExpressionVectorSpaceNotFound = expressionvector.ExpressionVectorSpaceNotFound
)

// ExpressionVectorCurrentProfile 返回运行期持久化索引当前使用的 profile
func ExpressionVectorCurrentProfile(reg *Registry) (ExpressionVectorRuntimeProfile, bool) {
	if reg == nil {
		reg = DefaultRegistry()
	}
	return expressionvector.ExpressionVectorCurrentProfile(reg)
}

// ExpressionVectorSyncStatus 返回索引同步任务状态
func (r *Registry) ExpressionVectorSyncStatus() ExpressionVectorSyncStatus {
	if r == nil {
		return ExpressionVectorSyncStatus{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return ExpressionVectorSyncStatus{Running: r.expressionSyncRunning, LastError: r.expressionSyncLastError}
}

// NewPersistentExpressionVectorStore 构造持久化索引
var NewPersistentExpressionVectorStore = expressionvector.NewPersistentExpressionVectorStore

// BackfillExpressionVectorIndex 为库内表达补齐向量，返回本次写入数量
func BackfillExpressionVectorIndex(ctx context.Context, reg *Registry) (int, error) {
	if reg == nil {
		return 0, nil
	}
	return expressionvector.BackfillExpressionVectorIndex(ctx, reg)
}

// SyncExpressionVectorIndex 同步表达向量索引
func SyncExpressionVectorIndex(ctx context.Context, reg *Registry) (int, error) {
	if reg == nil {
		return 0, nil
	}
	return expressionvector.SyncExpressionVectorIndex(ctx, reg)
}

// ExpressionVectorProgressRow 补建进度所需的表达行
type ExpressionVectorProgressRow struct {
	ID        uint
	Situation string
	Style     string
}

// ListExpressionsForVectorProgress 遍历全表取补建进度所需的表达行
//
// 对齐 Python _load_expression_rows_snapshot 的口径：不做聊天范围过滤，
// 空 situation/style 由调用方跳过。
func (s *gormWebUIStore) ListExpressionsForVectorProgress(ctx context.Context) ([]ExpressionVectorProgressRow, error) {
	var rows []ExpressionVectorProgressRow
	if err := s.db.WithContext(ctx).Model(&Expression{}).
		Select("id, situation, style").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ExpressionFingerprint 暴露索引指纹算法，供 WebUI 校验完成度
func ExpressionFingerprint(id uint, situation, style string) string {
	return expressionvector.ExpressionFingerprint(id, situation, style)
}

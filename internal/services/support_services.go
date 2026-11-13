package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// InternalChatProfileService 对齐 MaiBot internal_chat_profile 的原子 JSON 昵称存储
type InternalChatProfileService struct {
	mu   sync.RWMutex
	path string
}

const internalChatDefaultUserName = "人类"

func NewInternalChatProfileService(instanceDir string) (*InternalChatProfileService, error) {
	root := strings.TrimSpace(instanceDir)
	if root == "" {
		return nil, errors.New("实例目录未配置，无法定位内部聊天昵称文件")
	}
	return &InternalChatProfileService{path: filepath.Join(root, "data", "internal_chat_profile.json")}, nil
}

func validateInternalChatName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	length := len([]rune(trimmed))
	if length < 1 || length > 64 {
		return "", errors.New("内部聊天昵称长度必须为 1～64 个字符")
	}
	return trimmed, nil
}

// Get 读取昵称；文件缺失时返回默认值，初始昵称仅在缺失时写入
func (s *InternalChatProfileService) Get(initialName string) (string, error) {
	if s == nil {
		return "", errors.New("内部聊天昵称服务未初始化")
	}
	s.mu.RLock()
	path := s.path
	s.mu.RUnlock()
	raw, err := os.ReadFile(path)
	if err == nil {
		var payload struct {
			UserName string `json:"user_name"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return "", fmt.Errorf("读取内部聊天昵称失败: %w", err)
		}
		return validateInternalChatName(payload.UserName)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("读取内部聊天昵称失败: %w", err)
	}
	if strings.TrimSpace(initialName) != "" {
		return s.Set(initialName)
	}
	return internalChatDefaultUserName, nil
}

// Set 原子写入昵称
func (s *InternalChatProfileService) Set(name string) (string, error) {
	if s == nil {
		return "", errors.New("内部聊天昵称服务未初始化")
	}
	normalized, err := validateInternalChatName(name)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建内部聊天昵称目录失败: %w", err)
	}
	payload, err := json.Marshal(map[string]any{"user_name": normalized})
	if err != nil {
		return "", fmt.Errorf("编码内部聊天昵称失败: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".internal-chat-*.tmp")
	if err != nil {
		return "", fmt.Errorf("写入内部聊天昵称失败: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.Write(payload); err != nil {
		temp.Close()
		return "", fmt.Errorf("写入内部聊天昵称失败: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("写入内部聊天昵称失败: %w", err)
	}
	if err := os.Rename(tempName, s.path); err != nil {
		return "", fmt.Errorf("保存内部聊天昵称失败: %w", err)
	}
	return normalized, nil
}

// TaskConfig 是可用模型任务的最小描述
type TaskConfig struct {
	Name string
}

// ServiceTaskResolver 对齐 MaiBot service_task_resolver 的任务名解析
type ServiceTaskResolver struct {
	Tasks []TaskConfig
}

func NewServiceTaskResolver(tasks []TaskConfig) *ServiceTaskResolver {
	return &ServiceTaskResolver{Tasks: append([]TaskConfig(nil), tasks...)}
}

// ResolveTaskName 空任务名取第一项，未知任务返回真实错误
func (r *ServiceTaskResolver) ResolveTaskName(taskName string) (string, error) {
	if r == nil || len(r.Tasks) == 0 {
		return "", errors.New("没有可用的模型配置")
	}
	target := strings.TrimSpace(taskName)
	if target == "" {
		return r.Tasks[0].Name, nil
	}
	for _, task := range r.Tasks {
		if task.Name == target {
			return task.Name, nil
		}
	}
	return "", fmt.Errorf("未找到名为 `%s` 的模型配置", target)
}

// TelemetryAdapterDescriptor 对齐 Platform IO 驱动描述中的遥测字段
type TelemetryAdapterDescriptor struct {
	DriverID string
	Platform string
	Kind     string
	Metadata map[string]any
}

// TelemetryAdapter 是上报给统计服务的平台协议条目
type TelemetryAdapter struct {
	Platform string `json:"platform"`
	Protocol string `json:"protocol"`
}

// TelemetryPlatformService 对齐 MaiBot telemetry_platform_service 的适配器收集规则
type TelemetryPlatformService struct {
	Adapters func() []TelemetryAdapterDescriptor
}

func NewTelemetryPlatformService(provider func() []TelemetryAdapterDescriptor) *TelemetryPlatformService {
	return &TelemetryPlatformService{Adapters: provider}
}

func (s *TelemetryPlatformService) CollectAdapters() ([]TelemetryAdapter, error) {
	if s == nil || s.Adapters == nil {
		return nil, errors.New("Platform IO 驱动列表未接线")
	}
	seen := map[TelemetryAdapter]struct{}{}
	for _, adapter := range s.Adapters() {
		kind := strings.ToLower(strings.TrimSpace(adapter.Kind))
		if kind == "local" {
			continue
		}
		if kind == "plugin" {
			pluginType, _ := adapter.Metadata["plugin_type"].(string)
			if strings.TrimSpace(pluginType) != "adapter" {
				continue
			}
		}
		protocol := ""
		if adapter.Metadata != nil {
			if raw, ok := adapter.Metadata["protocol"]; ok {
				text, ok := raw.(string)
				if !ok {
					return nil, errors.New("驱动 metadata.protocol 必须是字符串")
				}
				protocol = text
			}
		}
		platform := normalizePlatform(adapter.Platform)
		if platform == "" {
			continue
		}
		seen[TelemetryAdapter{Platform: platform, Protocol: protocol}] = struct{}{}
	}
	out := make([]TelemetryAdapter, 0, len(seen))
	for item := range seen {
		out = append(out, item)
	}
	sortTelemetryAdapters(out)
	return out, nil
}

func sortTelemetryAdapters(items []TelemetryAdapter) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0; j-- {
			left, right := items[j-1], items[j]
			if left.Platform < right.Platform || (left.Platform == right.Platform && left.Protocol <= right.Protocol) {
				break
			}
			items[j-1], items[j] = items[j], items[j-1]
		}
	}
}

// ModelTaskUsage 对齐 MaiBot model_task_usage 表
type ModelTaskUsage struct {
	BucketTime      time.Time `gorm:"primaryKey;column:bucket_time" json:"bucket_time"`
	TaskName        string    `gorm:"primaryKey;size:100;column:task_name" json:"task_name"`
	ModelName       string    `gorm:"primaryKey;size:200;column:model_name" json:"model_name"`
	ModelIdentifier string    `gorm:"primaryKey;size:200;column:model_identifier" json:"model_identifier"`
	ProviderName    string    `gorm:"primaryKey;size:100;column:provider_name" json:"provider_name"`
	RequestCount    int64     `gorm:"not null;default:0;column:request_count" json:"request_count"`
	FailureCount    int64     `gorm:"not null;default:0;column:failure_count" json:"failure_count"`
	SuccessDuration float64   `gorm:"not null;default:0;column:success_duration" json:"success_duration"`
}

func (ModelTaskUsage) TableName() string { return "model_task_usage" }

// ModelTaskUsageModel 是查询返回的单模型聚合
type ModelTaskUsageModel struct {
	ModelName       string   `json:"model_name"`
	ModelIdentifier string   `json:"model_identifier"`
	ProviderName    string   `json:"provider_name"`
	RequestCount    int64    `json:"request_count"`
	FailureCount    int64    `json:"failure_count"`
	AvgDuration     *float64 `json:"avg_duration"`
	FailureRate     float64  `json:"failure_rate"`
}

// ModelTaskUsageSummary 对齐 Python get_model_task_usage 返回结构
type ModelTaskUsageSummary struct {
	TaskName     string                `json:"task_name"`
	Hours        int                   `json:"hours"`
	RequestCount int64                 `json:"request_count"`
	Models       []ModelTaskUsageModel `json:"models"`
}

// ModelTaskUsageService 对齐 MaiBot model_task_usage_service
type ModelTaskUsageService struct {
	DB *gorm.DB
}

func NewModelTaskUsageService(db *gorm.DB) *ModelTaskUsageService {
	return &ModelTaskUsageService{DB: db}
}

func (s *ModelTaskUsageService) ensure(ctx context.Context) (*gorm.DB, error) {
	if s == nil || s.DB == nil {
		return nil, errors.New("模型任务统计数据库未接线")
	}
	db := s.DB.WithContext(ctx)
	if err := db.AutoMigrate(&ModelTaskUsage{}); err != nil {
		return nil, fmt.Errorf("初始化模型任务统计表失败: %w", err)
	}
	return db, nil
}

// RecordModelTaskUsage 按分钟桶累加调用统计并清理七天前数据
func (s *ModelTaskUsageService) RecordModelTaskUsage(ctx context.Context, taskName, modelName, modelIdentifier, providerName string, duration time.Duration, succeeded bool, recordedAt time.Time) error {
	db, err := s.ensure(ctx)
	if err != nil {
		return err
	}
	bucket := recordedAt.Truncate(time.Minute)
	row := ModelTaskUsage{
		BucketTime: bucket, TaskName: strings.TrimSpace(taskName), ModelName: strings.TrimSpace(modelName),
		ModelIdentifier: strings.TrimSpace(modelIdentifier), ProviderName: strings.TrimSpace(providerName),
		RequestCount: 1,
	}
	if !succeeded {
		row.FailureCount = 1
	} else {
		row.SuccessDuration = duration.Seconds()
	}
	updates := map[string]any{
		"request_count":    gorm.Expr("model_task_usage.request_count + 1"),
		"failure_count":    gorm.Expr("model_task_usage.failure_count + ?", row.FailureCount),
		"success_duration": gorm.Expr("model_task_usage.success_duration + ?", row.SuccessDuration),
	}
	if err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "bucket_time"}, {Name: "task_name"}, {Name: "model_name"}, {Name: "model_identifier"}, {Name: "provider_name"}},
		DoUpdates: clause.Assignments(updates),
	}).Create(&row).Error; err != nil {
		return fmt.Errorf("写入模型任务统计失败: %w", err)
	}
	cutoff := bucket.Add(-7 * 24 * time.Hour)
	if err := db.Where("bucket_time < ?", cutoff).Delete(&ModelTaskUsage{}).Error; err != nil {
		return fmt.Errorf("清理模型任务统计失败: %w", err)
	}
	return nil
}

// TrackModelTaskUsage 对齐 track_model_task_usage：计时执行并记录结果；存储失败只记录，不影响业务返回值
func (s *ModelTaskUsageService) TrackModelTaskUsage(ctx context.Context, taskName, modelName, modelIdentifier, providerName string, run func() error) error {
	startedAt := time.Now()
	runErr := run()
	succeeded := runErr == nil
	// Python 侧仅记录写入异常而不向业务抛出，这里保持同样的吞错语义
	_ = s.RecordModelTaskUsage(ctx, taskName, modelName, modelIdentifier, providerName, time.Since(startedAt), succeeded, time.Now())
	return runErr
}

// GetModelTaskUsage 查询指定小时窗口的模型聚合
func (s *ModelTaskUsageService) GetModelTaskUsage(ctx context.Context, taskName string, hours int) (ModelTaskUsageSummary, error) {
	return s.GetModelTaskUsageAt(ctx, taskName, hours, time.Now())
}

// GetModelTaskUsageAt 按给定时刻查询模型聚合；end 取完整时刻，start 截到分钟
func (s *ModelTaskUsageService) GetModelTaskUsageAt(ctx context.Context, taskName string, hours int, now time.Time) (ModelTaskUsageSummary, error) {
	if hours <= 0 {
		hours = 24
	}
	summary := ModelTaskUsageSummary{TaskName: taskName, Hours: hours}
	db, err := s.ensure(ctx)
	if err != nil {
		return summary, err
	}
	end := now
	start := end.Add(-time.Duration(hours) * time.Hour).Truncate(time.Minute)
	var rows []struct {
		ModelName       string
		ModelIdentifier string
		ProviderName    string
		RequestCount    int64
		FailureCount    int64
		SuccessDuration float64
	}
	query := db.Model(&ModelTaskUsage{}).Select("model_name, model_identifier, provider_name, SUM(request_count) AS request_count, SUM(failure_count) AS failure_count, SUM(success_duration) AS success_duration").
		Where("task_name = ? AND bucket_time >= ? AND bucket_time <= ?", taskName, start, end).
		Group("model_name, model_identifier, provider_name").
		Order("SUM(request_count) DESC, model_name ASC")
	if err := query.Scan(&rows).Error; err != nil {
		return summary, fmt.Errorf("查询模型任务统计失败: %w", err)
	}
	for _, row := range rows {
		summary.RequestCount += row.RequestCount
		item := ModelTaskUsageModel{ModelName: row.ModelName, ModelIdentifier: row.ModelIdentifier, ProviderName: row.ProviderName, RequestCount: row.RequestCount, FailureCount: row.FailureCount}
		successes := row.RequestCount - row.FailureCount
		if successes > 0 {
			avg := row.SuccessDuration / float64(successes)
			item.AvgDuration = &avg
		}
		if row.RequestCount > 0 {
			item.FailureRate = float64(row.FailureCount) / float64(row.RequestCount)
		}
		summary.Models = append(summary.Models, item)
	}
	return summary, nil
}

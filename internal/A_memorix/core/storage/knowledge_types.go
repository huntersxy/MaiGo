package storage

// A_memorix 知识类型与导入策略，对齐 Python core/storage/knowledge_types.py

import (
	"fmt"
	"strings"
)

// KnowledgeType 持久化到 paragraphs.knowledge_type 的合法类型
type KnowledgeType string

const (
	KnowledgeStructured KnowledgeType = "structured"
	KnowledgeNarrative  KnowledgeType = "narrative"
	KnowledgeFactual    KnowledgeType = "factual"
	KnowledgeQuote      KnowledgeType = "quote"
	KnowledgeMixed      KnowledgeType = "mixed"
)

// ImportStrategy 文本导入阶段的策略选择
type ImportStrategy string

const (
	ImportAuto      ImportStrategy = "auto"
	ImportNarrative ImportStrategy = "narrative"
	ImportFactual   ImportStrategy = "factual"
	ImportQuote     ImportStrategy = "quote"
)

// AllowedKnowledgeTypeValues 返回合法落库类型顺序（与 Python 枚举顺序一致）
func AllowedKnowledgeTypeValues() []string {
	return []string{
		string(KnowledgeStructured),
		string(KnowledgeNarrative),
		string(KnowledgeFactual),
		string(KnowledgeQuote),
		string(KnowledgeMixed),
	}
}

// AllowedImportStrategyValues 返回合法导入策略顺序
func AllowedImportStrategyValues() []string {
	return []string{
		string(ImportAuto),
		string(ImportNarrative),
		string(ImportFactual),
		string(ImportQuote),
	}
}

// ParseKnowledgeType 从字符串解析知识类型，非法返回 false
func ParseKnowledgeType(value any) (KnowledgeType, bool) {
	s, ok := value.(string)
	if !ok {
		return "", false
	}
	normalized := strings.ToLower(strings.TrimSpace(s))
	for _, item := range AllowedKnowledgeTypeValues() {
		if item == normalized {
			return KnowledgeType(normalized), true
		}
	}
	return "", false
}

// ParseImportStrategy 从字符串解析导入策略，非法返回 false
func ParseImportStrategy(value any) (ImportStrategy, bool) {
	s, ok := value.(string)
	if !ok {
		return "", false
	}
	normalized := strings.ToLower(strings.TrimSpace(s))
	for _, item := range AllowedImportStrategyValues() {
		if item == normalized {
			return ImportStrategy(normalized), true
		}
	}
	return "", false
}

// ParseImportStrategyOrDefault 解析导入策略，非法值直接报错
func ParseImportStrategyOrDefault(value any, def ImportStrategy) (ImportStrategy, error) {
	if value == nil || pyFalsyImportValue(value) {
		return def, nil
	}
	if strategy, ok := value.(ImportStrategy); ok {
		return strategy, nil
	}
	raw := strings.ToLower(strings.TrimSpace(stringValue(value)))
	if raw == "" {
		return def, nil
	}
	strategy, ok := ParseImportStrategy(raw)
	if !ok {
		return "", fmt.Errorf("strategy_override 必须为 %s", strings.Join(AllowedImportStrategyValues(), "/"))
	}
	return strategy, nil
}

// pyFalsyImportValue 对齐 Python `value or ""` 的真值判断
func pyFalsyImportValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case bool:
		return !typed
	case int:
		return typed == 0
	case int64:
		return typed == 0
	case float64:
		return typed == 0
	case float32:
		return typed == 0
	case string:
		return typed == ""
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}

// ValidateStoredKnowledgeType 校验写库 knowledge_type
func ValidateStoredKnowledgeType(value any) (KnowledgeType, error) {
	if kt, ok := value.(KnowledgeType); ok {
		return kt, nil
	}
	if kt, ok := ParseKnowledgeType(value); ok {
		return kt, nil
	}
	return "", fmt.Errorf("knowledge_type 必须为 %s", strings.Join(AllowedKnowledgeTypeValues(), "/"))
}

// ResolveStoredKnowledgeType 将策略/字符串/旧值解析为合法落库类型
// allowLegacy 仅供迁移使用
func ResolveStoredKnowledgeType(value any, content string, allowLegacy bool, unknownFallback *KnowledgeType) (KnowledgeType, error) {
	if kt, ok := value.(KnowledgeType); ok {
		return kt, nil
	}

	if strategy, ok := value.(ImportStrategy); ok {
		if strategy == ImportAuto {
			if strings.TrimSpace(content) == "" {
				return "", fmt.Errorf("knowledge_type=auto 需要 content 才能推断")
			}
			return DetectKnowledgeType(content), nil
		}
		return KnowledgeType(strategy), nil
	}

	raw := strings.TrimSpace(stringValue(value))
	if raw == "" {
		if strings.TrimSpace(content) != "" {
			return DetectKnowledgeType(content), nil
		}
		return "", fmt.Errorf("knowledge_type 不能为空")
	}

	if direct, ok := ParseKnowledgeType(raw); ok {
		return direct, nil
	}
	if strategy, ok := ParseImportStrategy(raw); ok {
		return ResolveStoredKnowledgeType(strategy, content, false, nil)
	}

	if allowLegacy {
		if strings.ToLower(raw) == "imported" {
			return KnowledgeFactual, nil
		}
		if strings.TrimSpace(content) != "" {
			return DetectKnowledgeType(content), nil
		}
		if unknownFallback != nil {
			return *unknownFallback, nil
		}
	}

	return "", fmt.Errorf("非法 knowledge_type: %s（仅允许 %s）", raw, strings.Join(AllowedKnowledgeTypeValues(), "/"))
}

// ShouldExtractRelations 判断是否应该做关系抽取
func ShouldExtractRelations(kt KnowledgeType) bool {
	switch kt {
	case KnowledgeStructured, KnowledgeFactual, KnowledgeMixed:
		return true
	default:
		return false
	}
}

// DefaultChunkSize 获取默认分块大小
func DefaultChunkSize(kt KnowledgeType) int {
	switch kt {
	case KnowledgeStructured:
		return 300
	case KnowledgeNarrative:
		return 800
	case KnowledgeFactual:
		return 500
	case KnowledgeQuote:
		return 400
	case KnowledgeMixed:
		return 500
	default:
		return 500
	}
}

// TypeDisplayName 获取知识类型中文名称
func TypeDisplayName(kt KnowledgeType) string {
	switch kt {
	case KnowledgeStructured:
		return "结构化知识"
	case KnowledgeNarrative:
		return "叙事性文本"
	case KnowledgeFactual:
		return "事实陈述"
	case KnowledgeQuote:
		return "引用文本"
	case KnowledgeMixed:
		return "混合类型"
	default:
		return "未知类型"
	}
}

// stringValue 对齐 Python str(value or "")
func stringValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case KnowledgeType:
		return string(v)
	case ImportStrategy:
		return string(v)
	case bool:
		if v {
			return "True"
		}
		return "False"
	default:
		return fmt.Sprint(value)
	}
}

package llm

import (
	"math/rand"
	"strings"
)

// SelectionStrategy 对齐 MaiBot TaskConfig.selection_strategy，并支持 first（仅首模型、不 failover）
type SelectionStrategy string

const (
	StrategyBalance    SelectionStrategy = "balance"
	StrategyRandom     SelectionStrategy = "random"
	StrategySequential SelectionStrategy = "sequential"
	StrategyFirst      SelectionStrategy = "first"
)

var embeddingTasks = map[TaskName]bool{
	TaskEmbedding:      true,
	TaskImageEmbedding: true,
}

// NormalizeSelectionStrategy 解析任务上的模型选择策略；嵌入任务强制 sequential
func NormalizeSelectionStrategy(task TaskName, strategy string) SelectionStrategy {
	if embeddingTasks[task] {
		return StrategySequential
	}
	s := SelectionStrategy(strings.ToLower(strings.TrimSpace(strategy)))
	switch s {
	case StrategyRandom, StrategySequential, StrategyFirst:
		return s
	case "":
		return StrategyBalance
	default:
		return StrategyBalance
	}
}

// MaxModelAttempts 本次客户端创建最多尝试几个 model_list 条目
func MaxModelAttempts(strategy SelectionStrategy, modelCount int) int {
	if modelCount <= 0 {
		return 0
	}
	if strategy == StrategyFirst {
		return 1
	}
	return modelCount
}

// ModelUsage 对齐 MaiBot 的 (total_tokens, penalty, usage_penalty)
type ModelUsage struct {
	TotalTokens  int
	Penalty      int
	UsagePenalty int
}

// BalanceScore 与 MaiBot 负载均衡公式一致
func (u ModelUsage) BalanceScore() int {
	return u.TotalTokens + u.Penalty*300 + u.UsagePenalty*1000
}

// ModelSelectInput 单次从候选列表中选一个尚未排除的模型
type ModelSelectInput struct {
	Task     TaskName
	Models   []string
	Strategy SelectionStrategy
	Excluded map[string]struct{}
	// Usage 仅为兼容旧调用保留的惩罚计数；Score 非空时优先
	Usage    map[string]int
	Score    func(modelName string) int
	RandIntn func(n int) int
}

// PickNextModel 按策略选一个可用模型名；无候选时 ok=false
func PickNextModel(in ModelSelectInput) (name string, ok bool) {
	available := filterAvailableModels(in.Models, in.Excluded)
	if len(available) == 0 {
		return "", false
	}
	rng := in.RandIntn
	if rng == nil {
		rng = rand.Intn
	}
	switch in.Strategy {
	case StrategyFirst, StrategySequential:
		return available[0], true
	case StrategyRandom:
		return available[rng(len(available))], true
	case StrategyBalance:
		return pickBalanceModel(available, in), true
	default:
		return pickBalanceModel(available, in), true
	}
}

func filterAvailableModels(models []string, excluded map[string]struct{}) []string {
	out := make([]string, 0, len(models))
	for _, name := range models {
		if _, skip := excluded[name]; skip {
			continue
		}
		out = append(out, name)
	}
	return out
}

func pickBalanceModel(available []string, in ModelSelectInput) string {
	best := available[0]
	bestScore := balanceScore(best, in)
	for _, name := range available[1:] {
		if score := balanceScore(name, in); score < bestScore {
			best = name
			bestScore = score
		}
	}
	return best
}

// balanceScore 优先使用完整 usage 计分，否则回退到旧的惩罚计数
func balanceScore(modelName string, in ModelSelectInput) int {
	if in.Score != nil {
		return in.Score(modelName)
	}
	penalty := 0
	if in.Usage != nil {
		penalty = in.Usage[modelName]
	}
	return penalty * 1000
}

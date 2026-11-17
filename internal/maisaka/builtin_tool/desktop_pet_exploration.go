package tools

// 主人聊天的探索任务、产物检索、经验复用与候选图式工具，对齐 desktop_pet/exploration_tools.py。

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"MaiGo/internal/common/database"
	"MaiGo/internal/desktop_pet"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

// ExplorationTaskInput 探索任务工具参数
type ExplorationTaskInput struct {
	Action   string         `json:"action" jsonschema:"description=list 查任务；get 读取资源与经验；progress 记录步骤；complete 提交结果与产物；fail 记录失败,enum=list,enum=get,enum=progress,enum=complete,enum=fail"`
	TaskID   string         `json:"task_id,omitempty" jsonschema:"description=除 list 外必填，使用探索提示或 list 中的真实 ID"`
	Summary  string         `json:"summary,omitempty"`
	Result   string         `json:"result,omitempty" jsonschema:"description=complete 必填，仅写精简结论"`
	Products map[string]any `json:"products,omitempty" jsonschema:"description=两类产物完整快照，包含 resources 和 experiences"`
}

// SearchExplorationProductsInput 产物检索参数
type SearchExplorationProductsInput struct {
	Keywords []string `json:"keywords" jsonschema:"description=从当前需求提取 1 至 6 个具体短词"`
	Kind     string   `json:"kind,omitempty" jsonschema:"description=产物分类，默认 all,enum=all,enum=resource,enum=experience"`
	Offset   int      `json:"offset,omitempty"`
	Limit    int      `json:"limit,omitempty" jsonschema:"description=默认 5，最大 10"`
}

// RecordExperienceReuseInput 经验复用反馈参数
type RecordExperienceReuseInput struct {
	TaskID        string         `json:"task_id"`
	ExperienceRef string         `json:"experience_ref"`
	ReuseKey      string         `json:"reuse_key"`
	Report        map[string]any `json:"report"`
}

// ExplorationSchemaInput 候选图式参数
type ExplorationSchemaInput struct {
	Action   string `json:"action" jsonschema:"description=list 检索；save 保存,enum=list,enum=save"`
	Title    string `json:"title,omitempty"`
	Workflow string `json:"workflow,omitempty"`
	Evidence string `json:"evidence,omitempty"`
}

type explorationOutput struct {
	Success bool `json:"success"`
	Result  any  `json:"result,omitempty"`
}

func explorationOwner(ctx context.Context) (string, string, error) {
	tc, err := desktopPetOwnerContext(ctx, "只有主人内部聊天可以访问探索图式")
	if err != nil {
		return "", "", err
	}
	service := currentDesktopPetService()
	if service == nil || !service.Config().ExplorationEnabled {
		return "", "", fmt.Errorf("请先在实验性功能中开启桌宠自由探索")
	}
	return tc.SessionID, filepath.Join(memory.InstanceRoot(), "data"), nil
}

func explorationTaskTool(ctx context.Context, input *ExplorationTaskInput) (*explorationOutput, error) {
	sessionID, dataDir, err := explorationOwner(ctx)
	if err != nil {
		return nil, err
	}
	action := strings.TrimSpace(input.Action)
	switch action {
	case "list", "get", "progress", "complete", "fail":
	default:
		return nil, fmt.Errorf("未知探索任务操作")
	}
	result, err := desktoppet.ExplorationTasks(dataDir, sessionID, action, input.TaskID, "",
		input.Summary, input.Result, input.Products)
	if err != nil {
		return nil, err
	}
	if action == "list" {
		items, _ := result.([]map[string]any)
		trimmed := make([]map[string]any, 0, len(items))
		for _, item := range items {
			trimmed = append(trimmed, map[string]any{
				"id": item["id"], "goal": item["goal"], "status": item["status"], "updated_at": item["updated_at"],
			})
		}
		result = map[string]any{"items": trimmed}
	}
	return &explorationOutput{Success: true, Result: result}, nil
}

func searchExplorationProductsTool(ctx context.Context, input *SearchExplorationProductsInput) (*explorationOutput, error) {
	sessionID, dataDir, err := explorationOwner(ctx)
	if err != nil {
		return nil, err
	}
	kind := strings.TrimSpace(input.Kind)
	if kind == "" {
		kind = "all"
	}
	limit := input.Limit
	if limit == 0 {
		limit = 5
	}
	result, err := desktoppet.SearchExplorationProducts(dataDir, sessionID, input.Keywords, kind, input.Offset, limit)
	if err != nil {
		return nil, err
	}
	return &explorationOutput{Success: true, Result: result}, nil
}

func recordExperienceReuseTool(ctx context.Context, input *RecordExperienceReuseInput) (*explorationOutput, error) {
	sessionID, dataDir, err := explorationOwner(ctx)
	if err != nil {
		return nil, err
	}
	result, err := desktoppet.RecordExperienceReuse(dataDir, sessionID, input.TaskID, input.ExperienceRef,
		input.ReuseKey, input.Report)
	if err != nil {
		return nil, err
	}
	return &explorationOutput{Success: true, Result: result}, nil
}

func explorationSchemaTool(ctx context.Context, input *ExplorationSchemaInput) (*explorationOutput, error) {
	sessionID, dataDir, err := explorationOwner(ctx)
	if err != nil {
		return nil, err
	}
	var record map[string]any
	switch input.Action {
	case "list":
	case "save":
		record = map[string]any{}
		for key, value := range map[string]string{
			"title": input.Title, "workflow": input.Workflow, "evidence": input.Evidence,
		} {
			trimmed := strings.TrimSpace(value)
			limit := 100
			if key != "title" {
				limit = 8000
			}
			if length := len([]rune(trimmed)); length < 1 || length > limit {
				return nil, fmt.Errorf("%s 必须为 1 至 %d 字", key, limit)
			}
			record[key] = trimmed
		}
	default:
		return nil, fmt.Errorf("未知图式操作")
	}
	items, err := desktoppet.SchemaRecords(dataDir, sessionID, record)
	if err != nil {
		return nil, err
	}
	return &explorationOutput{Success: true, Result: map[string]any{"items": items}}, nil
}

// NewExplorationTaskTool 创建探索任务工具
func NewExplorationTaskTool() (tool.BaseTool, error) {
	return utils.InferTool("desktop_pet_exploration_task",
		"查询或上报探索任务。list 查任务 ID；get 读取资源和经验；progress 记录实际步骤；complete 提交精简 result 和结构化 products；fail 记录原因和已有产物。", explorationTaskTool)
}

// NewSearchExplorationProductsTool 创建产物检索工具
func NewSearchExplorationProductsTool() (tool.BaseTool, error) {
	return utils.InferTool("search_exploration_products",
		"检索历史探索资源与经验。用户需要资料、媒体、已有方法或解决类似问题时，先用具体关键词查找可复用产物。", searchExplorationProductsTool)
}

// NewRecordExperienceReuseTool 创建经验复用反馈工具
func NewRecordExperienceReuseTool() (tool.BaseTool, error) {
	return utils.InferTool("record_experience_reuse",
		"实际复用经验后追加结构化反馈，成功完善条件和步骤，失败记录原因。", recordExperienceReuseTool)
}

// NewExplorationSchemaTool 创建候选图式工具
func NewExplorationSchemaTool() (tool.BaseTool, error) {
	return utils.InferTool("desktop_pet_schema",
		"查询或保存主人会话的候选图式。相关任务先 list 检索已有方法；save 保存目标、适用条件、步骤、成功判据和实际实验结果。", explorationSchemaTool)
}

package platformio

import "errors"

var (
	errUnsupportedChatType        = errors.New("不支持的聊天类型")
	errEmptyPolicyPath            = errors.New("适配器策略文件路径为空")
	errEmptyAdapterIdentity       = errors.New("适配器身份不能为空")
	errInvalidDefaultAction       = errors.New("default_action 必须是 allow、block 或 inherit")
	errInvalidGlobalAction        = errors.New("action 必须是 allow 或 block")
	errInvalidOverrideAction      = errors.New("action 必须是 allow、block 或 inherit")
	errEmptyOverrideTargetID      = errors.New("target_id 不能为空")
	errEmptyOverridePlatform      = errors.New("platform 不能为空")
	errEmptyPolicyGroups          = errors.New("至少保留一个策略分组")
	errEmptyPolicyGroupField      = errors.New("分组 ID 和名称不能为空")
	errDuplicatePolicyGroup       = errors.New("分组 ID 和名称不能重复")
	errPolicyGroupMissing         = errors.New("当前策略分组不存在")
	errPolicyGroupMismatch        = errors.New("当前分组内容与生效规则不一致")
	errMissingPolicyGroupsForEdit = errors.New("切换分组时必须提供策略分组")
	errEmptyPolicyGroupContent    = errors.New("策略分组不能为空")
	errInvalidPolicyGroups        = errors.New("策略分组结构不合法")
)

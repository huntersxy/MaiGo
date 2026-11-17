package maibot

// 供宿主在收归公共目录前对旧模型配置执行同一套升级钩子。

import (
	"fmt"

	"github.com/pelletier/go-toml/v2"
)

// UpgradeModelDefinitions 对模型配置应用升级钩子；返回升级后的 models 与目标版本
func UpgradeModelDefinitions(document map[string]any) ([]any, string, error) {
	raw, err := toml.Marshal(document)
	if err != nil {
		return nil, "", err
	}
	var decoded struct {
		Inner InnerMeta `toml:"inner"`
		ModelConfigFile
	}
	if err := toml.Unmarshal(raw, &decoded); err != nil {
		return nil, "", err
	}
	current := decoded.Inner.Version
	if current == "" {
		return nil, "", fmt.Errorf("模型配置缺少 inner.version")
	}
	if compareVersion(current, ModelConfigTargetVersion) < 0 {
		upgraded, _, err := DefaultUpgradeRegistry.applyModel(current, ModelConfigTargetVersion, &decoded.ModelConfigFile)
		if err != nil {
			return nil, "", err
		}
		current = upgraded
	}
	encoded, err := toml.Marshal(decoded.ModelConfigFile)
	if err != nil {
		return nil, "", err
	}
	var upgraded map[string]any
	if err := toml.Unmarshal(encoded, &upgraded); err != nil {
		return nil, "", err
	}
	models, _ := upgraded["models"].([]any)
	return models, current, nil
}

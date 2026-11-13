package maibot

import (
	"strconv"
	"strings"
)

// upgradeLabelLegacyModelTypes 对齐 model_config 1.17.17：为缺少 model_type 的旧模型打标签
func upgradeLabelLegacyModelTypes(model *ModelConfigFile) []string {
	if model == nil || len(model.Models) == 0 {
		return nil
	}

	textModels := stringSetFromList(model.ModelTaskConfig.Embedding.ModelList)
	imageModels := stringSetFromList(model.ModelTaskConfig.ImageEmbedding.ModelList)

	var reasons []string
	for i := range model.Models {
		m := &model.Models[i]
		if modelTypeExplicitlySet(model, i) {
			continue
		}

		name := m.Name
		identifiers := []string{strings.ToLower(name), strings.ToLower(m.ModelIdentifier)}
		isEmbedding := textModels.has(name) || imageModels.has(name) || containsSubstring(identifiers, "embed")
		if isEmbedding {
			isImage := imageModels.has(name) || containsSubstring(identifiers, "vision") || containsSubstring(identifiers, "vl-embedding")
			if isImage {
				m.ModelType = "image_embedding"
			} else {
				m.ModelType = "embedding"
			}
		} else {
			m.ModelType = "llm"
		}
		reasons = append(reasons, formatModelIndexReason(i, "model_type"))
	}
	return reasons
}

func modelTypeExplicitlySet(model *ModelConfigFile, index int) bool {
	if model == nil {
		return false
	}
	if len(model.LegacyModelTypeKeySet) > index && model.LegacyModelTypeKeySet[index] {
		return true
	}
	// 无加载侧键位信息时：非空 model_type 视为显式（含非法值，交由后续校验）
	return strings.TrimSpace(model.Models[index].ModelType) != ""
}

type stringSet map[string]struct{}

func stringSetFromList(list []string) stringSet {
	s := make(stringSet, len(list))
	for _, raw := range list {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		s[name] = struct{}{}
	}
	return s
}

func (s stringSet) has(name string) bool {
	if s == nil {
		return false
	}
	_, ok := s[name]
	return ok
}

func containsSubstring(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func formatModelIndexReason(index int, field string) string {
	return "models[" + strconv.Itoa(index) + "]." + field
}

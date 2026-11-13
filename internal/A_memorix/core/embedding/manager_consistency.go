package embedding

// 模型一致性检查，对齐 manager.py 的 check_model_consistency

import (
	"context"
	"fmt"
)

// CheckModelConsistency 检查已存向量与当前模型输出是否一致
// 返回 (是否一致, 说明)，对齐 Python 返回 (bool, str)
func (m *EmbeddingManager) CheckModelConsistency(
	ctx context.Context,
	storedEmbeddings [][]float32,
	sampleTexts []string,
) (bool, string) {
	if len(storedEmbeddings) == 0 {
		// Python 在空数组上访问 shape[1] 会抛异常并被兜底捕获，这里给出等价失败说明
		return false, "一致性检查失败: stored_embeddings 为空"
	}
	actualDimension := len(storedEmbeddings[0])
	if actualDimension != m.config.Dimension {
		return false, fmt.Sprintf("维度不匹配: 期望 %d, 实际 %d", m.config.Dimension, actualDimension)
	}
	for index, embedding := range storedEmbeddings {
		if len(embedding) != actualDimension {
			return false, fmt.Sprintf(
				"一致性检查失败: 第 %d 条向量维度为 %d，期望 %d",
				index,
				len(embedding),
				actualDimension,
			)
		}
	}
	if len(sampleTexts) == 0 {
		return true, "维度匹配"
	}

	samples := sampleTexts
	if len(samples) > 5 {
		samples = samples[:5]
	}
	newEmbeddings, err := m.Encode(ctx, samples, 0, false, true)
	if err != nil {
		return false, fmt.Sprintf("一致性检查失败: %v", err)
	}

	count := len(storedEmbeddings)
	if count > 5 {
		count = 5
	}
	if len(newEmbeddings) < count {
		count = len(newEmbeddings)
	}
	if count == 0 {
		return false, "一致性检查失败: 新嵌入为空"
	}
	var similaritySum float64
	for index := 0; index < count; index++ {
		if len(newEmbeddings[index]) != actualDimension {
			return false, fmt.Sprintf(
				"一致性检查失败: 新向量维度为 %d，期望 %d",
				len(newEmbeddings[index]),
				actualDimension,
			)
		}
		// 对齐 np.dot(stored[:5], new.T).diagonal() 的逐条点积
		var dot float64
		for offset := 0; offset < actualDimension; offset++ {
			dot += float64(storedEmbeddings[index][offset]) * float64(newEmbeddings[index][offset])
		}
		similaritySum += dot
	}
	meanSimilarity := similaritySum / float64(count)
	if meanSimilarity < 0.95 {
		return false, fmt.Sprintf("模型可能已更改，平均相似度: %.3f", meanSimilarity)
	}
	return true, fmt.Sprintf("模型一致，平均相似度: %.3f", meanSimilarity)
}

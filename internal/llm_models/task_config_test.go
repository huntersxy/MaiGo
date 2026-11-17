package llm

import (
	"strings"
	"testing"

	"MaiGo/internal/config/maibot"
)

// TestResolveTaskConfigImageEmbeddingEmpty 验证图片嵌入任务未配置模型时输出 Python 同款错误
func TestResolveTaskConfigImageEmbeddingEmpty(t *testing.T) {
	_, err := ResolveTaskConfig(maibot.ModelConfigFile{}, TaskImageEmbedding)
	if err == nil || !strings.Contains(err.Error(), "图片嵌入任务未配置模型") {
		t.Fatalf("ResolveTaskConfig(image_embedding) error = %v", err)
	}
}

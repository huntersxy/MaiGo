package test

import (
	"context"
	"testing"
	"time"

	"MaiGo/internal/services/pythoncompat"
)

// TestPythonCompatSidecarRoundTrip 对真实侧车做一次往返。
//
// 需要先启动 scripts/python_compat_sidecar.py 并设置 MAIBOT_COMPAT_URL，
// 例如：
//
//	MAIBOT_SOURCE_ROOT=data/pycompat MAIBOT_COMPAT_SIDECAR_PORT=8799 \
//	  python scripts/python_compat_sidecar.py
//	MAIBOT_COMPAT_URL=http://127.0.0.1:8799 go test ./test/ -run SidecarRoundTrip
//
// 未配置时跳过，避免 CI 依赖外部进程。
func TestPythonCompatSidecarRoundTrip(t *testing.T) {
	client := pythoncompat.DefaultClient()
	if !client.Configured() {
		t.Skip("未设置 MAIBOT_COMPAT_URL，跳过侧车往返测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	typo, err := client.CreateTypoSentences(ctx, []string{"今天天气真不错，我们出去走走吧。"}, pythoncompat.TypoConfig{
		ErrorRate:       0.3,
		MinFreq:         5,
		ToneErrorRate:   0.2,
		WordReplaceRate: 0.3,
	}, ptrInt64(42))
	if err != nil {
		t.Fatalf("调用 /typo 失败: %v", err)
	}
	if len(typo) != 1 || typo[0].Text == "" {
		t.Fatalf("错别字结果异常: %+v", typo)
	}

	rows, cols := 12, 8
	vectors := make([]float32, rows*cols)
	for index := range vectors {
		vectors[index] = float32(index%7) - 3
	}
	result, err := client.RunKMeans(ctx, pythoncompat.KMeansRequest{
		Rows: rows, Cols: cols, Vectors: vectors, ClusterCount: 3,
	})
	if err != nil {
		t.Fatalf("调用 /kmeans 失败: %v", err)
	}
	if len(result.Labels) != rows {
		t.Fatalf("标签数量异常: %d", len(result.Labels))
	}
	if result.CentersRows != 3 || result.CentersCols != cols {
		t.Fatalf("中心尺寸异常: %dx%d", result.CentersRows, result.CentersCols)
	}
	if len(result.Centers) != result.CentersRows*result.CentersCols {
		t.Fatalf("中心数据长度异常: %d", len(result.Centers))
	}
	for _, label := range result.Labels {
		if label < 0 || label >= result.CentersRows {
			t.Fatalf("标签越界: %d", label)
		}
	}
}

func ptrInt64(value int64) *int64 { return &value }

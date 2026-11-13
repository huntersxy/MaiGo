package scripts

// 双池 ready 清单写入，对齐 _write_dual_ready_manifest

import (
	"path/filepath"
	"time"

	"github.com/bytedance/sonic"
)

// lpmmManifestJSON 对齐 json.dump(sort_keys=True, ensure_ascii=False, indent=2)
var lpmmManifestJSON = sonic.Config{SortMapKeys: true, EscapeHTML: false}.Froze()

// writeDualReadyManifest 写入 vectors/dual_ready.json
func (c *LPMMParquetConverter) writeDualReadyManifest() error {
	paragraphCount := c.paragraphVectorStore.NumVectors()
	graphCount := c.graphVectorStore.NumVectors()
	stats := map[string]any{
		"paragraphs": map[string]any{"done": c.vectorStats["paragraph"], "failed": 0},
		"entities":   map[string]any{"done": c.vectorStats["entity"], "failed": 0},
		"relations":  map[string]any{"done": c.vectorStats["relation"], "failed": 0},
	}
	migration := map[string]any{
		"paragraphs": map[string]any{"copied": c.vectorStats["paragraph"], "encoded": 0, "missing": 0},
		"entities":   map[string]any{"copied": c.vectorStats["entity"], "encoded": 0, "missing": 0},
		"relations":  map[string]any{"copied": c.vectorStats["relation"], "encoded": 0, "missing": 0},
	}
	payload := map[string]any{
		"status":                "ready",
		"version":               1,
		"mode":                  "dual",
		"dimension":             c.dimension,
		"created_at":            float64(time.Now().UnixNano()) / 1e9,
		"paragraph_vectors":     paragraphCount,
		"graph_vectors":         graphCount,
		"stats":                 stats,
		"migration":             migration,
		"embedding_fingerprint": c.embeddingFingerprint,
		"generation_reason":     "lpmm_conversion",
	}
	raw, err := lpmmManifestJSON.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return writeFileAtomic(filepath.Join(c.vectorDir, "dual_ready.json"), raw)
}

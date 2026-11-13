package expressionvector

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

func expressionVectorResolveVectorsPath(indexPath, rawVectorsPath string) string {
	raw := strings.TrimSpace(rawVectorsPath)
	if raw == "" {
		return expressionVectorDefaultVectorsPath(indexPath)
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(indexPath), raw))
}

func expressionVectorDefaultVectorsPath(indexPath string) string {
	extension := filepath.Ext(indexPath)
	if extension == "" {
		return indexPath + ".npz"
	}
	return strings.TrimSuffix(indexPath, extension) + ".npz"
}

func expressionVectorVersionedVectorsPath(indexPath string) (string, error) {
	extension := filepath.Ext(indexPath)
	stem := strings.TrimSuffix(filepath.Base(indexPath), extension)
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(indexPath), stem+".vectors-"+hex.EncodeToString(random)+".npz"), nil
}

func (s *PersistentExpressionVectorStore) removeObsoleteExpressionVectorFile(oldPath string) {
	indexDir := filepath.Dir(s.path)
	if filepath.Clean(filepath.Dir(oldPath)) != filepath.Clean(indexDir) {
		return
	}
	extension := filepath.Ext(s.path)
	stem := strings.TrimSuffix(filepath.Base(s.path), extension)
	name := filepath.Base(oldPath)
	if name != stem+".npz" && !strings.HasPrefix(name, stem+".vectors-") {
		return
	}
	if err := os.Remove(oldPath); err != nil && !os.IsNotExist(err) {
		zap.L().Warn("表达向量索引旧 npz 清理失败，旧文件可安全保留",
			zap.String("path", oldPath),
			zap.Error(err))
	}
}

func writeExpressionVectorJSONAtomic(path string, encoded []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".expression-vectors-*.json.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	committed := false
	defer func() {
		_ = temp.Close()
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := temp.Write(encoded); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}

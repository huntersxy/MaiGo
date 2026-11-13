package services

// 向量重建检查点，对齐 vector_rebuild_checkpoint.py 的 VectorRebuildCheckpoint

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"

	_ "github.com/glebarez/go-sqlite"
)

// EmbeddingConfigurationChangedError 构建期间嵌入配置变化，当前成果不得用于新模型
type EmbeddingConfigurationChangedError struct {
	Message string
}

func (e *EmbeddingConfigurationChangedError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "embedding 配置已变化"
}

// IsEmbeddingConfigurationChanged 判断错误是否为嵌入配置变化
func IsEmbeddingConfigurationChanged(err error) bool {
	var target *EmbeddingConfigurationChangedError
	return errors.As(err, &target)
}

// VectorRebuildCheckpointItem 检查点条目
type VectorRebuildCheckpointItem struct {
	ID   string
	Text string
}

// VectorRebuildCheckpoint 逐批提交成功向量，模型身份与文本内容共同决定是否可恢复
type VectorRebuildCheckpoint struct {
	path        string
	fingerprint string
	dimension   int
	db          *sql.DB
}

// NewVectorRebuildCheckpoint 打开或创建检查点数据库
func NewVectorRebuildCheckpoint(path string, fingerprint string, dimension int) (*VectorRebuildCheckpoint, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec("CREATE TABLE IF NOT EXISTS vectors (" +
		"model TEXT, pool TEXT, id TEXT, input TEXT, vector BLOB, " +
		"PRIMARY KEY (model, pool, id))")
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &VectorRebuildCheckpoint{path: path, fingerprint: fingerprint, dimension: dimension, db: db}, nil
}

// Path 返回检查点数据库路径
func (c *VectorRebuildCheckpoint) Path() string { return c.path }

// Dimension 返回检查点向量维度
func (c *VectorRebuildCheckpoint) Dimension() int { return c.dimension }

// InputHash 对齐 input_hash
func (c *VectorRebuildCheckpoint) InputHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// Read 读取与当前模型、文本完全匹配的向量
func (c *VectorRebuildCheckpoint) Read(pool string, items []VectorRebuildCheckpointItem) (map[string][]float32, error) {
	vectors := make(map[string][]float32, len(items))
	for _, item := range items {
		var raw []byte
		err := c.db.QueryRow(
			"SELECT vector FROM vectors WHERE model=? AND pool=? AND id=? AND input=?",
			c.fingerprint, pool, item.ID, c.InputHash(item.Text),
		).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		vector, decodeErr := decodeFloat32Vector(raw)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if len(vector) != c.dimension {
			return nil, fmt.Errorf("重建检查点向量维度异常: %s", item.ID)
		}
		vectors[item.ID] = vector
	}
	return vectors, nil
}

// Write 写入一批向量
func (c *VectorRebuildCheckpoint) Write(pool string, items []VectorRebuildCheckpointItem, vectors [][]float32) error {
	if len(items) != len(vectors) {
		return fmt.Errorf("重建检查点写入条目与向量数量不一致: %d != %d", len(items), len(vectors))
	}
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	statement, err := tx.Prepare("INSERT OR REPLACE INTO vectors VALUES (?, ?, ?, ?, ?)")
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer statement.Close()
	for index, item := range items {
		if _, err := statement.Exec(c.fingerprint, pool, item.ID, c.InputHash(item.Text), encodeFloat32Vector(vectors[index])); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// Discard 只删除本模型的成果，避免模型切换误删其他模型检查点
func (c *VectorRebuildCheckpoint) Discard() error {
	_, err := c.db.Exec("DELETE FROM vectors WHERE model=?", c.fingerprint)
	return err
}

// Close 关闭检查点数据库
func (c *VectorRebuildCheckpoint) Close() error { return c.db.Close() }

func encodeFloat32Vector(vector []float32) []byte {
	raw := make([]byte, len(vector)*4)
	for index, value := range vector {
		binary.LittleEndian.PutUint32(raw[index*4:], math.Float32bits(value))
	}
	return raw
}

func decodeFloat32Vector(raw []byte) ([]float32, error) {
	if len(raw)%4 != 0 {
		return nil, fmt.Errorf("检查点向量字节长度非法: %d", len(raw))
	}
	vector := make([]float32, len(raw)/4)
	for index := range vector {
		vector[index] = math.Float32frombits(binary.LittleEndian.Uint32(raw[index*4:]))
	}
	return vector, nil
}

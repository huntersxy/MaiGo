package memruntime

// 向量空间输入映射，对齐 sdk_memory_kernel.py 的 _vector_space_inputs 与 vector_space_service 的输入记录
// 输入映射按活动向量空间保存到 vectors/embedding_inputs.json，键为 item_type:item_id

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// VectorSpaceInputHash 对齐 input_hash，返回文本的 sha256 十六进制摘要
func VectorSpaceInputHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// VectorSpaceInputs 返回输入映射副本
func (k *Kernel) VectorSpaceInputs() map[string]string {
	k.stateMu.RLock()
	defer k.stateMu.RUnlock()
	out := make(map[string]string, len(k.vectorSpaceInputs))
	for key, value := range k.vectorSpaceInputs {
		out[key] = value
	}
	return out
}

// SetVectorSpaceInputs 替换输入映射
func (k *Kernel) SetVectorSpaceInputs(inputs map[string]string) {
	copied := make(map[string]string, len(inputs))
	for key, value := range inputs {
		copied[key] = value
	}
	k.stateMu.Lock()
	k.vectorSpaceInputs = copied
	k.stateMu.Unlock()
}

// RecordVectorSpaceInput 记录一次向量编码输入，对齐 record_input
func (k *Kernel) RecordVectorSpaceInput(itemType string, itemID string, text string) {
	k.stateMu.Lock()
	defer k.stateMu.Unlock()
	if k.vectorSpaceInputs == nil {
		k.vectorSpaceInputs = map[string]string{}
	}
	k.vectorSpaceInputs[itemType+":"+itemID] = VectorSpaceInputHash(text)
}

// VectorSpaceInputsPath 返回当前活动向量空间的输入映射文件
func (k *Kernel) VectorSpaceInputsPath() string {
	root := filepath.Join(k.dataDir, "vectors")
	if space := k.ActiveVectorSpaceID(); space != "" {
		root = filepath.Join(root, "spaces", space)
	}
	return filepath.Join(root, "embedding_inputs.json")
}

// SaveVectorSpaceInputs 持久化输入映射，对齐 _save_inputs 的原子写入
func (k *Kernel) SaveVectorSpaceInputs() error {
	path := k.VectorSpaceInputsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return fmt.Errorf("创建向量输入目录失败: %w", err)
	}
	k.stateMu.RLock()
	inputs := make(map[string]string, len(k.vectorSpaceInputs))
	for key, value := range k.vectorSpaceInputs {
		inputs[key] = value
	}
	k.stateMu.RUnlock()
	raw, err := json.Marshal(inputs)
	if err != nil {
		return fmt.Errorf("序列化向量输入映射失败: %w", err)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, raw, 0o666); err != nil {
		return fmt.Errorf("写入向量输入映射失败: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("替换向量输入映射失败: %w", err)
	}
	return nil
}

// LoadVectorSpaceInputs 从当前活动空间读取输入映射
func (k *Kernel) LoadVectorSpaceInputs() error {
	path := k.VectorSpaceInputsPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			k.SetVectorSpaceInputs(nil)
			return nil
		}
		return fmt.Errorf("读取向量输入映射失败: %w", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("解析向量输入映射失败: %w", err)
	}
	inputs := make(map[string]string, len(payload))
	for key, value := range payload {
		if text, ok := value.(string); ok {
			inputs[key] = text
		}
	}
	k.SetVectorSpaceInputs(inputs)
	return nil
}

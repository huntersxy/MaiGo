package messagecomponent

import (
	"bytes"
	"fmt"

	"github.com/vmihailenco/msgpack/v5"
)

// 对应 MaiBot/src/common/utils/utils_message.py 中的
// MessageUtils.from_MaiSeq_to_db_record_msg 与 from_db_record_msg_to_MaiSeq：
//
//	msgpack.packb(msg.to_dict())
//	msgpack.unpackb(raw_content)
//
// Python msgpack 的默认编码是 use_bin_type=True、raw=False，
// 即 str 编码为 msgpack str，bytes 编码为 bin，字符串键解码回 str。
// vmihailenco/msgpack 的默认行为与之一致，因此这里不做额外包装

// EncodeDBRecord 把消息序列编码为 mai_messages.raw_content 的字节串
func EncodeDBRecord(seq *MessageSequence) ([]byte, error) {
	if seq == nil {
		return nil, fmt.Errorf("消息序列不能为空")
	}
	var buf bytes.Buffer
	enc := msgpack.NewEncoder(&buf)
	// 逐组件手写，保证键顺序与 Python dict 插入顺序一致
	if err := encodeSequence(enc, seq); err != nil {
		return nil, fmt.Errorf("msgpack 编码消息序列失败: %w", err)
	}
	return buf.Bytes(), nil
}

// DecodeDBRecord 把 mai_messages.raw_content 解码为消息序列
func DecodeDBRecord(raw []byte) (*MessageSequence, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("消息原始内容为空")
	}
	var items []any
	if err := msgpack.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("msgpack 解码消息序列失败: %w", err)
	}
	return FromDict(items)
}

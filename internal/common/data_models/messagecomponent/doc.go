// Package messagecomponent 是 MaiBot 消息组件模型的 Go 移植。
//
// 权威来源：src/common/data_models/message_component_data_model.py（组件类型与
// to_dict/from_dict）、src/common/utils/utils_message.py（raw_content 的 msgpack 存取）。
//
// 组件形状（type/data 二元组，媒体类带 hash）与 Python 逐键一致，EncodeDBRecord
// 按 Python dict 插入顺序写出，因此同一消息在两侧得到逐字节相同的 raw_content。
package messagecomponent

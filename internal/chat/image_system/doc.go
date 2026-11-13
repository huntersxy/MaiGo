// Package imagesystem 对齐 MaiBot src/chat/image_system 与图片接收压缩逻辑
//
// 对应 Python：
//   - image_manager.py：data/images 缓存、sha256 去重、VLM 描述、DB 记录与失败语义
//   - image_cache_cleanup.py：按配置清理过期缓存、孤立文件与无文件识别结果
//   - message_receive/image_receive_compressor.py：入站过大图片压缩或丢弃
//   - common/data_models/image_data_model.py：MaiImage 的哈希与扩展名修正
//
// DB 通过 Store 端口隔离，VLM 通过 Describer 端口隔离，记忆同步通过 MemorySync
// 端口隔离；默认实现分别见 gorm_store.go、router_describer.go。
package imagesystem

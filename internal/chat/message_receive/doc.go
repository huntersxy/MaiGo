// Package messagereceive 移植 MaiBot src/chat/message_receive：
// 会话消息模型、ChatManager 会话索引、ChatBot 入站命令链与统一发送链。
// 数据库、平台、模型能力通过端口注入，包内不直接依赖 OneBot 或具体数据库驱动
package messagereceive

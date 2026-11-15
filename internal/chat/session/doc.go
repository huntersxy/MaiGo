// Package chatsession 对齐 MaiBot chat_manager 与 SessionUtils 的聊天流身份规则。
//
// MaiBot 的 session_id 是 MD5 十六进制串（src/common/utils/utils_session.py），由
// platform、account_id、scope 与群/私聊 ID 计算。MaiGo 内部编排使用
// conversation key（group:{target_id} / private:{target_id}），两者不等价。
//
// 写入约束：只有 get_or_create 与入站 register 链路可写入 chat_sessions；表达、
// 黑话、记忆、文件绑定等模块须经 ResolveSession 解析已存在的流，不得自行写入
// 计算的 MD5 或 conversation key。
package chatsession

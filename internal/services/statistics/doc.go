// Package stats 聚合在线时长、消息计数与可选远程遥测。
//
// 遥测仅在 MAIBOT_TELEMETRY_URL 非空时启用：心跳 60s 一次，统计上传间隔 11451s
// 并对齐 MaiBot 的 0/5/15/45s 重试；载荷 schema_version 2，游标存
// data/local_store.json 的 telemetry_stats_cursor。
//
// 统计表：online_time（分钟心跳区间）、statistics_message_hourly / tool_hourly /
// model_hourly（增量聚合，游标表 statistics_aggregation_cursors）、llm_usage（明细）、
// tool_records（工具调用明细，写入见 internal/maisaka/runtime/tool_records.go）。
// 时间序列按行换算目标时区：PostgreSQL 用 AT TIME ZONE，SQLite 按存储的本地墙钟格式化，
// 跨 DST 不会把整段当成固定偏移。WebUI 仪表盘缓存 version 6、20 分钟过期。
package stats

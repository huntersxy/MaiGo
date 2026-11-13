// Package stats 聚合在线时长、消息计数与可选远程遥测。
//
// 遥测仅在 MAIBOT_TELEMETRY_URL 非空时启用：心跳 60s 一次，统计上传间隔 11451s
// 并对齐 MaiBot 的 0/5/15/45s 重试；载荷 schema_version 2，游标存
// data/local_store.json 的 telemetry_stats_cursor。
//
// 统计表：online_time（分钟心跳区间）、statistics_message_hourly / tool_hourly /
// model_hourly（增量聚合，游标表 statistics_aggregation_cursors）、llm_usage（明细）。
// WebUI 仪表盘缓存 version 6、20 分钟过期。
//
// 与 MaiBot 的差异（tool_records 无持久表、DST 未处理）见 agents/MODULE_ALIGNMENT.md。
package stats

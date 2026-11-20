# AGENTS

本目录对齐 MaiBot `src/llm_models`：请求重试/切换模型、失败快照、用量统计与 Provider 客户端。

## 职责

- `retry.go` / `failover_model.go`：单模型重试、任务级 hard_timeout、model_list 切换。
- `request_snapshot.go`：失败请求快照与重放信息，schema v6 与 MaiBot 只读兼容。
- `usage_recording.go`：`llm_usage`、`model_task_usage` 与 `model_call_hourly` 写入。
- `model_client/`：Chat Completions、Responses、Gemini、插件 Provider 与嵌入/转写客户端。

## 诊断字段（Go 侧补充）

- `HTTPStatusError` 额外保存 `RequestID`：取上游响应头 `x-request-id`、`request-id`、`x-oneapi-request-id`、`cf-ray` 等，缺失时为空。
- 失败日志带 `session_id`、`request_id`（逻辑请求 UUID）、`status_code`、`provider_request_id`、`provider_message`、`snapshot_path` 与 `replay_command`；`llm.retry` / `llm.error` 事件带 `status_code`、`provider_request_id` 与 `request_id`。
- `model_call_hourly` 按实际 Provider 调用计次，失败计入 `failure_count`，无 usage 的成功调用也计次。
- `Error()` 的中文映射文案保持与 MaiBot `RespNotOkException` 一致，不得把状态码拼进用户可见文案。

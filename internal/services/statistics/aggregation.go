package stats

import (
	"context"
	"time"

	"gorm.io/gorm"
)

const (
	cursorSourceMessages   = "messages"
	cursorSourceTools      = "tool_records"
	cursorSourceModelUsage = "model_usage"
)

// RefreshStatisticsAggregates 增量刷新 MaiBot 兼容的统计汇总表
//
// 消息来自 mai_messages，工具调用来自 conversation_transcript 的 tool 条目，
// 模型调用明细来自 llm_usage；每张源表用 statistics_aggregation_cursors 记录 last_processed_id。
// SQL 对齐 Python statistics_aggregation_service（INSERT … WITH … SELECT … ON CONFLICT）。
func RefreshStatisticsAggregates(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if err := EnsureSchema(ctx, db); err != nil {
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := aggregateMessageHourly(tx); err != nil {
			return err
		}
		if err := aggregateToolHourly(tx); err != nil {
			return err
		}
		return aggregateModelHourly(tx)
	})
}

func aggregateMessageHourly(tx *gorm.DB) error {
	lastID, err := loadAggregationCursor(tx, cursorSourceMessages)
	if err != nil {
		return err
	}
	// chat_id/chat_name/chat_type 口径对齐 Python：优先 group_id/user_id/group_name/user_nickname。
	if err := tx.Exec(`INSERT INTO statistics_message_hourly(
		bucket_time, chat_id, chat_name, chat_type, message_count, latest_timestamp
	)
	WITH message_source AS (
		SELECT
			id,
			datetime(strftime('%Y-%m-%d %H:00:00', COALESCE(received_at, timestamp))) AS bucket_time,
			CASE
				WHEN group_id IS NOT NULL AND group_id != '' THEN 'g' || group_id
				ELSE 'u' || CAST(user_id AS TEXT)
			END AS chat_id,
			COALESCE(
				CASE WHEN group_id IS NOT NULL AND group_id != '' THEN NULLIF(group_name, '') ELSE NULLIF(user_nickname, '') END,
				CASE
					WHEN group_id IS NOT NULL AND group_id != '' THEN '群' || group_id
					ELSE '用户' || CAST(user_id AS TEXT)
				END
			) AS chat_name,
			CASE WHEN group_id IS NOT NULL AND group_id != '' THEN 'group' ELSE 'private' END AS chat_type,
			COALESCE(received_at, timestamp) AS timestamp
		FROM mai_messages
		WHERE id > ?
			AND timestamp IS NOT NULL
			AND (
				(group_id IS NOT NULL AND group_id != '')
				OR (user_id IS NOT NULL AND user_id != 0)
			)
	), message_aggregate AS (
		SELECT bucket_time, chat_id, COUNT(*) AS message_count, MAX(timestamp) AS latest_timestamp
		FROM message_source
		GROUP BY bucket_time, chat_id
	), latest_message AS (
		SELECT bucket_time, chat_id, chat_name, chat_type,
			ROW_NUMBER() OVER (PARTITION BY bucket_time, chat_id ORDER BY timestamp DESC, id DESC) AS row_num
		FROM message_source
	)
	SELECT a.bucket_time, a.chat_id, l.chat_name, l.chat_type, a.message_count, a.latest_timestamp
	FROM message_aggregate a
	JOIN latest_message l
		ON l.bucket_time = a.bucket_time AND l.chat_id = a.chat_id AND l.row_num = 1
	ON CONFLICT(bucket_time, chat_id) DO UPDATE SET
		chat_name = excluded.chat_name,
		chat_type = excluded.chat_type,
		message_count = statistics_message_hourly.message_count + excluded.message_count,
		latest_timestamp = MAX(statistics_message_hourly.latest_timestamp, excluded.latest_timestamp)`, lastID).Error; err != nil {
		return err
	}
	return setAggregationCursorToTableMax(tx, cursorSourceMessages, "mai_messages")
}

func aggregateToolHourly(tx *gorm.DB) error {
	lastID, err := loadAggregationCursor(tx, cursorSourceTools)
	if err != nil {
		return err
	}
	// Go 无持久 tool_records；从 conversation_transcript 的 tool 条目读，已留档。
	if err := tx.Exec(`INSERT INTO statistics_tool_hourly(bucket_time, tool_name, call_count)
	SELECT
		datetime(strftime('%Y-%m-%d %H:00:00', created_at)) AS bucket_time,
		COALESCE(NULLIF(tool_name, ''), 'unknown') AS tool_name,
		COUNT(*) AS call_count
	FROM conversation_transcript
	WHERE id > ? AND entry_kind = 'tool' AND created_at IS NOT NULL
	GROUP BY datetime(strftime('%Y-%m-%d %H:00:00', created_at)), COALESCE(NULLIF(tool_name, ''), 'unknown')
	ON CONFLICT(bucket_time, tool_name) DO UPDATE SET
		call_count = statistics_tool_hourly.call_count + excluded.call_count`, lastID).Error; err != nil {
		return err
	}
	return setAggregationCursorToTableMax(tx, cursorSourceTools, "conversation_transcript")
}

func aggregateModelHourly(tx *gorm.DB) error {
	lastID, err := loadAggregationCursor(tx, cursorSourceModelUsage)
	if err != nil {
		return err
	}
	if err := tx.Exec(`INSERT INTO statistics_model_hourly(
		bucket_time, request_type, module_name, provider_name, model_name,
		request_count, prompt_tokens, completion_tokens, total_tokens, cost, time_cost_sum, time_cost_sq_sum
	)
	SELECT
		datetime(strftime('%Y-%m-%d %H:00:00', timestamp)) AS bucket_time,
		COALESCE(request_type, 'unknown') AS request_type,
		CASE WHEN instr(COALESCE(request_type, 'unknown'), '.') > 0
			THEN substr(COALESCE(request_type, 'unknown'), 1, instr(COALESCE(request_type, 'unknown'), '.') - 1)
			ELSE COALESCE(request_type, 'unknown') END AS module_name,
		COALESCE(model_api_provider_name, 'unknown') AS provider_name,
		COALESCE(model_assign_name, model_name, 'unknown') AS model_name,
		COUNT(*) AS request_count,
		COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
		COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
		COALESCE(SUM(total_tokens), 0) AS total_tokens,
		COALESCE(SUM(cost), 0) AS cost,
		COALESCE(SUM(time_cost), 0) AS time_cost_sum,
		COALESCE(SUM(time_cost * time_cost), 0) AS time_cost_sq_sum
	FROM llm_usage
	WHERE id > ? AND timestamp IS NOT NULL
	GROUP BY
		datetime(strftime('%Y-%m-%d %H:00:00', timestamp)),
		COALESCE(request_type, 'unknown'),
		COALESCE(model_api_provider_name, 'unknown'),
		COALESCE(model_assign_name, model_name, 'unknown')
	ON CONFLICT(bucket_time, request_type, model_name, provider_name) DO UPDATE SET
		request_count = statistics_model_hourly.request_count + excluded.request_count,
		prompt_tokens = statistics_model_hourly.prompt_tokens + excluded.prompt_tokens,
		completion_tokens = statistics_model_hourly.completion_tokens + excluded.completion_tokens,
		total_tokens = statistics_model_hourly.total_tokens + excluded.total_tokens,
		cost = statistics_model_hourly.cost + excluded.cost,
		time_cost_sum = statistics_model_hourly.time_cost_sum + excluded.time_cost_sum,
		time_cost_sq_sum = statistics_model_hourly.time_cost_sq_sum + excluded.time_cost_sq_sum`, lastID).Error; err != nil {
		return err
	}
	return setAggregationCursorToTableMax(tx, cursorSourceModelUsage, "llm_usage")
}

func loadAggregationCursor(tx *gorm.DB, source string) (int64, error) {
	var lastID int64
	if err := tx.Raw(`SELECT COALESCE(last_processed_id, 0) FROM statistics_aggregation_cursors WHERE source_name = ?`, source).Scan(&lastID).Error; err != nil {
		return 0, err
	}
	return lastID, nil
}

func setAggregationCursorToTableMax(tx *gorm.DB, source, table string) error {
	maxID, err := sourceTableMaxID(tx, table)
	if err != nil {
		return err
	}
	return tx.Exec(`INSERT INTO statistics_aggregation_cursors(source_name, last_processed_id, updated_at)
		VALUES(?, ?, ?)
		ON CONFLICT(source_name) DO UPDATE SET
			last_processed_id = excluded.last_processed_id,
			updated_at = excluded.updated_at`, source, maxID, time.Now().UTC()).Error
}

func sourceTableMaxID(tx *gorm.DB, table string) (int64, error) {
	switch table {
	case "mai_messages", "conversation_transcript", "llm_usage":
	default:
		return 0, gorm.ErrInvalidData
	}
	var maxID int64
	if err := tx.Raw(`SELECT COALESCE(MAX(id), 0) FROM ` + table).Scan(&maxID).Error; err != nil {
		return 0, err
	}
	return maxID, nil
}

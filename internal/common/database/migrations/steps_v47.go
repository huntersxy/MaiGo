package migration

import "context"

// migrateV47 补齐 mai_messages 的消息元数据列，历史行使用列默认值
func migrateV47(ctx context.Context, q querier) error {
	exists, err := tableExists(ctx, q, "mai_messages")
	if err != nil || !exists {
		return err
	}
	columns := []struct {
		name       string
		definition string
	}{
		{"user_cardname", "TEXT"},
		{"group_id", "TEXT NOT NULL DEFAULT ''"},
		{"group_name", "TEXT NOT NULL DEFAULT ''"},
		{"is_at", "BOOLEAN NOT NULL DEFAULT false"},
		{"is_emoji", "BOOLEAN NOT NULL DEFAULT false"},
		{"is_picture", "BOOLEAN NOT NULL DEFAULT false"},
		{"is_command", "BOOLEAN NOT NULL DEFAULT false"},
		{"is_notify", "BOOLEAN NOT NULL DEFAULT false"},
		{"additional_config", jsonType(q)},
	}
	for _, column := range columns {
		if err := ensureColumn(ctx, q, "mai_messages", column.name, column.definition); err != nil {
			return err
		}
	}
	return ensureIndex(ctx, q, "ix_message_logs_is_command", "mai_messages", "is_command")
}

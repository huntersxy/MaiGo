package migration

import (
	"context"
)

// migrateV56 建立独立相通组表，对齐 Python v48→v49
func migrateV56(ctx context.Context, q querier) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS sharing_groups (
			id VARCHAR NOT NULL PRIMARY KEY,
			name VARCHAR NOT NULL,
			color VARCHAR NOT NULL,
			note VARCHAR NOT NULL,
			enabled BOOLEAN NOT NULL,
			revision INTEGER NOT NULL,
			created_at FLOAT NOT NULL,
			updated_at FLOAT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sharing_group_members (
			group_id VARCHAR NOT NULL,
			session_id VARCHAR NOT NULL,
			created_at FLOAT NOT NULL,
			PRIMARY KEY (group_id, session_id),
			FOREIGN KEY(group_id) REFERENCES sharing_groups (id)
		)`,
		`CREATE INDEX IF NOT EXISTS ix_sharing_group_members_session_id ON sharing_group_members (session_id)`,
		`CREATE TABLE IF NOT EXISTS sharing_group_capabilities (
			group_id VARCHAR NOT NULL,
			capability VARCHAR NOT NULL,
			enabled BOOLEAN NOT NULL,
			PRIMARY KEY (group_id, capability),
			FOREIGN KEY(group_id) REFERENCES sharing_groups (id)
		)`,
		`CREATE TABLE IF NOT EXISTS sharing_group_rules (
			id VARCHAR NOT NULL PRIMARY KEY,
			group_id VARCHAR NOT NULL,
			platform VARCHAR NOT NULL,
			item_id VARCHAR NOT NULL,
			rule_type VARCHAR NOT NULL,
			FOREIGN KEY(group_id) REFERENCES sharing_groups (id)
		)`,
		`CREATE INDEX IF NOT EXISTS ix_sharing_group_rules_group_id ON sharing_group_rules (group_id)`,
		`CREATE TABLE IF NOT EXISTS sharing_group_migrations (
			source VARCHAR NOT NULL PRIMARY KEY,
			group_id VARCHAR,
			payload VARCHAR NOT NULL,
			migrated_at FLOAT NOT NULL
		)`,
	}
	for _, statement := range statements {
		if err := q.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

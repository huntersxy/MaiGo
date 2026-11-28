package migration

import (
	"context"
)

func migrateV59(ctx context.Context, q querier) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS binary_data (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			data_hash VARCHAR(255) NOT NULL,
			full_path VARCHAR(1024) NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS ix_binary_data_data_hash ON binary_data (data_hash)`,
	}
	if q.Dialect() == DialectPostgres {
		statements = []string{
			`CREATE TABLE IF NOT EXISTS binary_data (
				id BIGSERIAL PRIMARY KEY,
				data_hash VARCHAR(255) NOT NULL,
				full_path VARCHAR(1024) NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS ix_binary_data_data_hash ON binary_data (data_hash)`,
		}
	}
	for _, statement := range statements {
		if err := q.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
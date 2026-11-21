package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

func migrateV58(ctx context.Context, q querier) error {
	individualIDType := "VARCHAR(32)"
	createdAtType := "DATETIME"
	createdAtDefault := "CURRENT_TIMESTAMP"
	mergeEventID := "INTEGER PRIMARY KEY AUTOINCREMENT"
	if q.Dialect() == DialectPostgres {
		individualIDType = "VARCHAR(32)"
		createdAtType = "TIMESTAMPTZ"
		createdAtDefault = "now()"
		mergeEventID = "BIGSERIAL PRIMARY KEY"
	}
	statements := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS individuals (
			individual_id %s NOT NULL,
			display_name VARCHAR(255) NOT NULL,
			is_known BOOLEAN NOT NULL DEFAULT FALSE,
			profile_sources_json TEXT NOT NULL DEFAULT '[]',
			created_at %s NOT NULL DEFAULT %s,
			PRIMARY KEY (individual_id)
		)`, individualIDType, createdAtType, createdAtDefault),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS individual_merge_events (
			id %s,
		target_individual_id VARCHAR(32) NOT NULL,
		members_json TEXT NOT NULL,
		profiles_json TEXT NOT NULL DEFAULT '{}',
		created_at %s NOT NULL DEFAULT %s
		)`, mergeEventID, createdAtType, createdAtDefault),
	}
	for _, statement := range statements {
		if err := q.Exec(ctx, statement); err != nil {
			return err
		}
	}
	if err := ensureColumn(ctx, q, "person_info", "individual_id", "VARCHAR(32) REFERENCES individuals(individual_id)"); err != nil {
		return err
	}
	rows, err := q.Query(ctx, `SELECT id, person_id, person_name, user_nickname, platform, user_id, is_known
		FROM person_info WHERE individual_id IS NULL`)
	if err != nil {
		return err
	}
	type personRow struct {
		id           int64
		personID     sql.NullString
		personName   sql.NullString
		userNickname sql.NullString
		platform     sql.NullString
		userID       sql.NullString
		isKnown      bool
	}
	people := make([]personRow, 0)
	for rows.Next() {
		var person personRow
		if err := rows.Scan(&person.id, &person.personID, &person.personName, &person.userNickname, &person.platform, &person.userID, &person.isKnown); err != nil {
			rows.Close()
			return err
		}
		people = append(people, person)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, person := range people {
		individualID := strings.ReplaceAll(uuid.NewString(), "-", "")
		displayName := nullableText(person.personName)
		if displayName == "" {
			displayName = nullableText(person.userNickname)
		}
		if displayName == "" {
			displayName = nullableText(person.userID)
		}
		profileSource, err := json.Marshal([]map[string]any{{
			"individual_id": individualID,
			"person_id":     nullableText(person.personID),
			"platform":      nullableText(person.platform),
			"user_id":       nullableText(person.userID),
			"display_name":  displayName,
			"is_known":      person.isKnown,
		}})
		if err != nil {
			return err
		}
		if err := q.Exec(ctx, `INSERT INTO individuals (individual_id, display_name, is_known, profile_sources_json)
			VALUES (?, ?, ?, ?)`, individualID, displayName, person.isKnown, string(profileSource)); err != nil {
			return err
		}
		if err := q.Exec(ctx, `UPDATE person_info SET individual_id=? WHERE id=?`, individualID, person.id); err != nil {
			return err
		}
	}
	if err := ensureIndex(ctx, q, "ix_person_info_individual_id", "person_info", "individual_id"); err != nil {
		return err
	}
	if err := ensureIndex(ctx, q, "ix_individual_merge_events_target_individual_id", "individual_merge_events", "target_individual_id"); err != nil {
		return err
	}
	if q.Dialect() == DialectSQLite {
		for _, operation := range []string{"INSERT", "UPDATE"} {
			trigger := "person_individual_required_" + strings.ToLower(operation)
			statement := fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS %s BEFORE %s ON person_info
				WHEN NEW.individual_id IS NULL BEGIN
				SELECT RAISE(ABORT, 'person_info.individual_id is required');
				END`, trigger, operation)
			if err := q.Exec(ctx, statement); err != nil {
				return err
			}
		}
	} else if err := q.Exec(ctx, "ALTER TABLE person_info ALTER COLUMN individual_id SET NOT NULL"); err != nil {
		return err
	}
	for _, column := range []string{"name_reason", "memory_points"} {
		exists, err := columnExists(ctx, q, "person_info", column)
		if err != nil {
			return err
		}
		if exists {
			if err := q.Exec(ctx, "ALTER TABLE person_info DROP COLUMN "+column); err != nil {
				return err
			}
		}
	}
	return nil
}

func nullableText(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

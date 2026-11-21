package memorixcore

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strconv"
	"strings"

	"MaiGo/internal/A_memorix/core/storage"
)

type PersonMigrationPlanner interface {
	HasTable(string) bool
	QualifiedTable(string) string
	Rows(table, where string, args ...any) ([]map[string]any, error)
	Transform(table, where string, args []any, transform func(map[string]any) (map[string]any, error), merge func(string, map[string]any, map[string]any) (map[string]any, error), keepSource bool) error
	Combine(key string, source, target map[string]any) map[string]any
}

type personMemoryMigration struct {
	platform, sourceUser, targetUser, source, target string
	claims                                           map[string]map[string]any
	claimIDs                                         map[string]string
}

func BuildPersonMigration(plan PersonMigrationPlanner, platform, sourceUser, targetUser, source, target string) error {
	migration := personMemoryMigration{platform: platform, sourceUser: sourceUser, targetUser: targetUser, source: source, target: target}
	people := []any{source, target}
	factsWhere := `scope_type='person' AND scope_id IN (?,?) AND status IN ('active','conflicted')`
	if err := migration.planClaims(plan, factsWhere, people); err != nil {
		return err
	}
	if err := plan.Transform("fact_claims", factsWhere, people, func(row map[string]any) (map[string]any, error) {
		return maps.Clone(migration.claims[rowText(row, "claim_id")]), nil
	}, nil, true); err != nil {
		return err
	}
	if plan.HasTable("fact_evidence") && plan.HasTable("fact_claims") {
		where := `claim_id IN (SELECT claim_id FROM ` + plan.QualifiedTable("fact_claims") + ` WHERE ` + factsWhere + `)`
		if err := plan.Transform("fact_evidence", where, people, migration.rewriteRow, nil, true); err != nil {
			return err
		}
	}
	for _, table := range []string{"person_identities", "person_identity_aliases", "person_profile_overrides", "person_profile_alias_overrides", "person_profile_refresh_queue"} {
		var merge func(string, map[string]any, map[string]any) (map[string]any, error)
		if table == "person_profile_alias_overrides" {
			merge = func(key string, source, target map[string]any) (map[string]any, error) {
				aliases, err := mergePersonAliasOverrides(rowText(source, "aliases_json"), rowText(target, "aliases_json"))
				if err != nil {
					return nil, err
				}
				source["aliases_json"], target["aliases_json"] = aliases, aliases
				return plan.Combine(key, source, target), nil
			}
		}
		if err := plan.Transform(table, "person_id IN (?,?)", people, func(row map[string]any) (map[string]any, error) {
			if table == "person_profile_refresh_queue" {
				row["status"], row["reason"] = "pending", "person_identity_migration"
				row["retry_count"], row["last_error"] = int64(0), nil
			}
			return migration.rewriteRow(row)
		}, merge, false); err != nil {
			return err
		}
	}
	if err := migration.moveSnapshots(plan, people); err != nil {
		return err
	}
	if plan.HasTable("paragraphs") {
		paragraphWhere := `source IN (?,?) AND (expires_at IS NULL OR expires_at > CAST(strftime('%s','now') AS REAL))`
		paragraphArgs := []any{"person_fact:" + source, "person_fact:" + target}
		if err := plan.Transform("paragraphs", paragraphWhere, paragraphArgs, migration.rewriteRow, nil, false); err != nil {
			return err
		}
		where := `paragraph_hash IN (SELECT hash FROM ` + plan.QualifiedTable("paragraphs") + ` WHERE ` + paragraphWhere + `)`
		if err := plan.Transform("external_memory_refs", where, paragraphArgs, migration.rewriteRow, nil, false); err != nil {
			return err
		}
	}
	return nil
}

func (migration *personMemoryMigration) planClaims(plan PersonMigrationPlanner, where string, people []any) error {
	claims, err := plan.Rows("fact_claims", where, people...)
	if err != nil {
		return err
	}
	migration.claims, migration.claimIDs = map[string]map[string]any{}, map[string]string{}
	groups := map[string]map[string]bool{}
	changedGroups := map[string]bool{}
	for _, claim := range claims {
		oldID := rowText(claim, "claim_id")
		if claim["scope_id"] == migration.source {
			factKey, value := rowText(claim, "fact_key"), rowText(claim, "value_normalized")
			claimID, err := storage.ClaimIdentity("person", migration.target, factKey, value, rowText(claim, "polarity"))
			if err != nil {
				return err
			}
			group, err := storage.ConflictGroup("person", migration.target, factKey, value, rowText(claim, "cardinality"))
			if err != nil {
				return err
			}
			claim["claim_id"], claim["scope_id"], claim["conflict_group"] = claimID, migration.target, group
			migration.claimIDs[oldID] = claimID
			changedGroups[group] = true
		}
		migration.claims[oldID] = claim
		group := rowText(claim, "conflict_group")
		if groups[group] == nil {
			groups[group] = map[string]bool{}
		}
		groups[group][rowText(claim, "claim_id")] = true
	}
	for _, claim := range migration.claims {
		group := rowText(claim, "conflict_group")
		if changedGroups[group] && len(groups[group]) > 1 {
			claim["status"] = "conflicted"
		}
	}
	return nil
}

func (migration *personMemoryMigration) moveSnapshots(plan PersonMigrationPlanner, people []any) error {
	const table = "person_profile_snapshots"
	if !plan.HasTable(table) {
		return nil
	}
	where := `person_id IN (?,?) AND profile_version = (SELECT MAX(latest.profile_version) FROM ` + plan.QualifiedTable(table) +
		` AS latest WHERE latest.person_id=person_profile_snapshots.person_id)`
	snapshots, err := plan.Rows(table, where, people...)
	if err != nil {
		return err
	}
	var version int64
	for _, snapshot := range snapshots {
		if snapshot["person_id"] != migration.target {
			continue
		}
		value, err := strconv.ParseInt(fmt.Sprint(snapshot["profile_version"]), 10, 64)
		if err != nil || value < 0 {
			return errors.New("画像版本无效")
		}
		version = max(version, value)
	}
	return plan.Transform(table, where, people, func(row map[string]any) (map[string]any, error) {
		if row["person_id"] == migration.source {
			row["profile_version"] = version + 1
		}
		row["expires_at"] = int64(0)
		return migration.rewriteRow(row)
	}, nil, false)
}

func (migration *personMemoryMigration) rewriteRow(row map[string]any) (map[string]any, error) {
	for key, value := range row {
		if key == "user_id" && row["platform"] != nil && row["platform"] != migration.platform {
			continue
		}
		if raw, ok := value.(string); ok && raw != "" && (strings.HasSuffix(key, "_json") || key == "metadata") {
			var data any
			decoder := json.NewDecoder(strings.NewReader(raw))
			decoder.UseNumber()
			if err := decoder.Decode(&data); err != nil || !json.Valid([]byte(raw)) {
				return nil, fmt.Errorf("%s 不是有效的 JSON", key)
			}
			changed := migration.rewrite(data, strings.TrimSuffix(key, "_json"))
			if !reflect.DeepEqual(data, changed) {
				encoded, err := json.Marshal(changed)
				if err != nil {
					return nil, err
				}
				row[key] = string(encoded)
			}
		} else {
			row[key] = migration.rewrite(value, key)
		}
	}
	return row, nil
}

func (migration *personMemoryMigration) rewrite(value any, key string) any {
	switch item := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(item))
		for field, child := range item {
			result[field] = migration.rewrite(child, field)
		}
		if item["scope_type"] == "person" && item["scope_id"] == migration.source {
			result["scope_id"] = migration.target
		}
		return result
	case []any:
		allText := true
		result := make([]any, len(item))
		for index, child := range item {
			result[index] = migration.rewrite(child, key)
			if _, ok := result[index].(string); !ok {
				allText = false
			}
		}
		if allText {
			seen := map[string]bool{}
			unique := result[:0]
			for _, child := range result {
				text := child.(string)
				if !seen[text] {
					seen[text] = true
					unique = append(unique, child)
				}
			}
			return unique
		}
		return result
	case string:
		switch key {
		case "person_id", "person_ids", "target_person_id":
			if item == migration.source {
				return migration.target
			}
		case "user_id", "user_ids", "target_user_id", "sender_id":
			if item == migration.sourceUser {
				return migration.targetUser
			}
		case "claim_id", "claim_ids", "old_claim_id", "new_claim_id", "fact_claim_ids":
			if edited := migration.claimIDs[item]; edited != "" {
				return edited
			}
		case "source", "external_id", "external_ref":
			prefix := "person_fact:" + migration.source
			if item == prefix || strings.HasPrefix(item, prefix+":") {
				return "person_fact:" + migration.target + item[len(prefix):]
			}
		}
	}
	return value
}

func rowText(row map[string]any, key string) string {
	text, _ := row[key].(string)
	return text
}

func mergePersonAliasOverrides(source, target string) (string, error) {
	union := []string{}
	seen := map[string]bool{}
	for _, raw := range []string{target, source} {
		var values []string
		if err := json.Unmarshal([]byte(raw), &values); err != nil || values == nil {
			return "", errors.New("画像别名覆盖必须为 JSON 字符串数组")
		}
		for _, value := range values {
			if !seen[value] {
				seen[value] = true
				union = append(union, value)
			}
		}
	}
	encoded, err := json.Marshal(union)
	return string(encoded), err
}

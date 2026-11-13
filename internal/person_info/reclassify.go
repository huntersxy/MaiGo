package personinfo

// 历史人物事实重分类，对齐 MaiBot src/services/person_fact_reverification.py
// 只读 metadata.db 找出 user_supported 证据的 summary_derived 事实，再升级为 direct_user

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/glebarez/go-sqlite"
)

// ReclassifyHistoricalResult 一批历史事实重分类结果
type ReclassifyHistoricalResult struct {
	Processed  int    `json:"processed"`
	Promoted   int    `json:"promoted"`
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}

type historicalFactRow struct {
	ClaimID   string
	PersonID  string
	ValueText string
	Metadata  sql.NullString
}

// ReclassifyHistoricalPersonFacts 按 claim_id 游标扫描并升级历史人物事实
func (r *Registry) ReclassifyHistoricalPersonFacts(ctx context.Context, cursor string, limit int) (ReclassifyHistoricalResult, error) {
	dbPath, err := r.metadataDBPath()
	if err != nil {
		return ReclassifyHistoricalResult{}, err
	}
	if _, err := os.Stat(dbPath); err != nil {
		return ReclassifyHistoricalResult{}, fmt.Errorf("personinfo: metadata.db 不可读: %w", err)
	}
	dsn := "file:" + filepath.ToSlash(dbPath) + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return ReclassifyHistoricalResult{}, fmt.Errorf("personinfo: 打开 metadata.db 失败: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return ReclassifyHistoricalResult{}, fmt.Errorf("personinfo: 连接 metadata.db 失败: %w", err)
	}
	return r.reclassifyHistoricalBatch(ctx, db, cursor, limit)
}

func (r *Registry) reclassifyHistoricalBatch(ctx context.Context, db *sql.DB, cursor string, limit int) (ReclassifyHistoricalResult, error) {
	// 对齐 Python max(1, limit)：只保证下限，不额外夹紧上限
	if limit < 1 {
		limit = 1
	}
	query := `
		SELECT c.claim_id, c.scope_id, c.value_text, p.metadata
		FROM fact_claims c
		JOIN fact_evidence e
		  ON e.claim_id = c.claim_id
		 AND e.evidence_type = 'paragraph'
		 AND e.stance = 'support'
		JOIN paragraphs p ON p.hash = e.evidence_id
		WHERE c.scope_type = 'person'
		  AND c.status = 'active'
		  AND c.authority = 'summary_derived'
		  AND c.stability = 'uncertain'
		  AND c.profile_section = 'uncertain_notes'
		  AND c.claim_id > ?
		  AND (p.is_deleted IS NULL OR p.is_deleted = 0)
		  AND e.evidence_id = (
			SELECT MIN(e2.evidence_id)
			FROM fact_evidence e2
			JOIN paragraphs p2 ON p2.hash = e2.evidence_id
			WHERE e2.claim_id = c.claim_id
			  AND e2.evidence_type = 'paragraph'
			  AND e2.stance = 'support'
			  AND (p2.is_deleted IS NULL OR p2.is_deleted = 0)
			  AND json_valid(p2.metadata)
			  AND json_extract(p2.metadata, '$.evidence_source') = 'user_supported'
		  )
		ORDER BY c.claim_id ASC
		LIMIT ?`
	rows, err := db.QueryContext(ctx, query, strings.TrimSpace(cursor), limit+1)
	if err != nil {
		return ReclassifyHistoricalResult{}, fmt.Errorf("personinfo: 查询历史人物事实失败: %w", err)
	}
	defer rows.Close()

	var items []historicalFactRow
	for rows.Next() {
		var item historicalFactRow
		if err := rows.Scan(&item.ClaimID, &item.PersonID, &item.ValueText, &item.Metadata); err != nil {
			return ReclassifyHistoricalResult{}, fmt.Errorf("personinfo: 扫描历史人物事实失败: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return ReclassifyHistoricalResult{}, err
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	result := ReclassifyHistoricalResult{NextCursor: strings.TrimSpace(cursor), HasMore: hasMore}
	// 对齐 Python：processed 是本批取出条数，与后续元数据校验结果无关
	result.Processed = len(items)
	for _, item := range items {
		result.NextCursor = item.ClaimID
		if !evidenceSourceIsUserSupported(item.Metadata.String) {
			continue
		}
		promoted, err := r.promoteHistoricalFact(ctx, item.ClaimID)
		if err != nil {
			return result, err
		}
		if promoted {
			result.Promoted++
		}
	}
	return result, nil
}

func evidenceSourceIsUserSupported(metadata string) bool {
	if strings.TrimSpace(metadata) == "" {
		return false
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(metadata), &parsed); err != nil {
		return false
	}
	return strings.EqualFold(stringFromAny(parsed["evidence_source"]), EvidenceSourceUserSupported)
}

func (r *Registry) promoteHistoricalFact(ctx context.Context, claimID string) (bool, error) {
	payload, err := r.RawFactAdmin(ctx, "get", map[string]any{"claim_id": claimID})
	if err != nil {
		return false, err
	}
	if !boolFromAny(payload["success"]) {
		return false, nil
	}
	claim := mapFromAnyLoose(payload["claim"])
	if claim == nil {
		return false, nil
	}
	if stringFromAny(claim["authority"]) != FactAuthoritySummaryDerived {
		return false, nil
	}
	updated, err := r.RawFactAdmin(ctx, "update", map[string]any{
		"claim_id":        claimID,
		"authority":       FactAuthorityDirectUser,
		"stability":       FactStabilityStable,
		"profile_section": ProfileSectionStableFacts,
		"confidence":      1.0,
		"reason":          "historical_person_fact_reclassified",
	})
	if err != nil {
		return false, err
	}
	if !boolFromAny(updated["success"]) {
		return false, fmt.Errorf("personinfo: 重分类 %s 失败: %s", claimID, stringFromAny(updated["error"]))
	}
	return true, nil
}

func (r *Registry) runtimeDataDir() (string, error) {
	host := r.memorixHostRaw()
	if host == nil {
		return "", ErrMemorixUnavailable
	}
	dirProvider, ok := host.(interface{ GetRuntimeDataDir() string })
	if !ok {
		return "", fmt.Errorf("personinfo: Memorix 宿主未暴露 runtime_data_dir")
	}
	dir := strings.TrimSpace(dirProvider.GetRuntimeDataDir())
	if dir == "" {
		return "", fmt.Errorf("personinfo: Memorix runtime_data_dir 为空")
	}
	return dir, nil
}

func (r *Registry) metadataDBPath() (string, error) {
	dir, err := r.runtimeDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "metadata", "metadata.db"), nil
}

func (r *Registry) memorixHostRaw() any {
	return memorixHostValue()
}

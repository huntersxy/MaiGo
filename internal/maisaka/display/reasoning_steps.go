package display

// 推理轮次关联：上下文传递 trace id，磁盘索引由预览写入维护，对齐 maisaka/display/reasoning_steps.py

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/glebarez/go-sqlite"
)

const reasoningStepsIndexName = ".reasoning-steps.sqlite3"

// ReasoningStep 索引中的一条推理记录
type ReasoningStep struct {
	Stage   string
	Session string
	Stem    string
}

// RegisterReasoningStep 把预览文件登记到本轮 trace 下
func RegisterReasoningStep(root, path, traceID string) error {
	if strings.TrimSpace(traceID) == "" {
		return nil
	}
	stage, session, stem, ok := reasoningStepIdentity(root, path)
	if !ok {
		return nil
	}
	db, err := sql.Open("sqlite", filepath.Join(root, reasoningStepsIndexName))
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(
		"CREATE TABLE IF NOT EXISTS steps (" +
			"stage TEXT, session TEXT, stem TEXT, trace_id TEXT NOT NULL, PRIMARY KEY(stage, session, stem))",
	); err != nil {
		return err
	}
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS steps_trace ON steps(trace_id, session)"); err != nil {
		return err
	}
	_, err = db.Exec(
		"INSERT OR REPLACE INTO steps VALUES (?, ?, ?, ?)",
		stage, session, stem, traceID,
	)
	return err
}

// RemoveReasoningStep 更新记录前移除旧索引
func RemoveReasoningStep(root, path string) {
	index := filepath.Join(root, reasoningStepsIndexName)
	if _, err := os.Stat(index); err != nil {
		return
	}
	if strings.ToLower(filepath.Ext(path)) != ".json" {
		return
	}
	stage, session, stem, ok := reasoningStepIdentity(root, path)
	if !ok {
		return
	}
	db, err := sql.Open("sqlite", index)
	if err != nil {
		return
	}
	defer db.Close()
	_, _ = db.Exec("DELETE FROM steps WHERE stage=? AND session=? AND stem=?", stage, session, stem)
}

// FindReasoningSteps 按 trace 与会话读取同轮次记录，按 stem、stage 排序
func FindReasoningSteps(root, traceID, session string) []ReasoningStep {
	if strings.TrimSpace(traceID) == "" {
		return nil
	}
	index := filepath.Join(root, reasoningStepsIndexName)
	if _, err := os.Stat(index); err != nil {
		return nil
	}
	db, err := sql.Open("sqlite", index)
	if err != nil {
		return nil
	}
	defer db.Close()
	rows, err := db.Query(
		"SELECT stage, session, stem FROM steps WHERE trace_id=? AND session=? ORDER BY stem, stage",
		traceID, session,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	steps := make([]ReasoningStep, 0, 16)
	for rows.Next() {
		var step ReasoningStep
		if err := rows.Scan(&step.Stage, &step.Session, &step.Stem); err != nil {
			continue
		}
		steps = append(steps, step)
	}
	return steps
}

// reasoningStepIdentity 从 <root>/<stage>/<session>/<stem>.<ext> 解析索引键
func reasoningStepIdentity(root, path string) (string, string, string, bool) {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return "", "", "", false
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) != 3 {
		return "", "", "", false
	}
	stem := strings.TrimSuffix(parts[2], filepath.Ext(parts[2]))
	if stem == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], stem, true
}

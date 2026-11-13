package learners

import (
	"go.uber.org/zap"
)

type skippedJargonEntry struct {
	Content  string
	SourceID string
	Reason   string
}

type jargonProcessResult struct {
	Parsed   int
	Accepted int
	Saved    int
	Updated  int
	Contents []string
	Skipped  []skippedJargonEntry
}

// logJargonUpdateProcess 对齐 Python _log_jargon_update_process 的过程摘要
func (l *JargonLearner) logJargonUpdateProcess(status string, miner *JargonMiner, sourceItems []SourceItem, parsed, accepted int, skipped []skippedJargonEntry, saved, updated int) {
	learnerSessionID := ""
	if l != nil {
		learnerSessionID = l.SessionID
	}
	learningSessionID, sessionName := "", ""
	if miner != nil {
		learningSessionID = miner.SessionID
		sessionName = miner.SessionName
	}
	zap.L().Info("黑话学习更新过程",
		zap.String("learner_session_id", learnerSessionID),
		zap.String("status", status),
		zap.String("learning_session_id", learningSessionID),
		zap.String("session_name", sessionName),
		zap.Int("source_items", len(sourceItems)),
		zap.Int("parsed", parsed),
		zap.Int("accepted", accepted),
		zap.Int("skipped", len(skipped)),
		zap.Int("saved", saved),
		zap.Int("updated", updated))
}

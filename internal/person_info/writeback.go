package personinfo

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strings"

	"MaiGo/internal/A_memorix"

	"go.uber.org/zap"
)

// AnswerWritebackInput 对齐 MaiBot store_person_memory_from_answer 参数
type AnswerWritebackInput struct {
	PersonName         string
	MemoryContent      string
	ChatID             string
	PersonID           string
	Platform           string
	UserID             string
	GroupID            string
	EvidenceSource     string
	EvidenceMessageIDs []string
	FactClaim          map[string]any
}

// StorePersonFactFromAnswer 将人物事实写入 Memorix
func StorePersonFactFromAnswer(ctx context.Context, in AnswerWritebackInput) error {
	return DefaultRegistry().StorePersonFactFromAnswer(ctx, in)
}

// StorePersonFactFromAnswer 对齐 store_person_memory_from_answer
func (r *Registry) StorePersonFactFromAnswer(ctx context.Context, in AnswerWritebackInput) error {
	content := strings.TrimSpace(in.MemoryContent)
	if content == "" {
		zap.L().Debug("人物事实写回跳过：memory_content 为空")
		return nil
	}
	chatID := strings.TrimSpace(in.ChatID)
	if chatID == "" {
		zap.L().Warn("人物事实写回失败：chat_id 为空")
		return nil
	}

	personID := strings.TrimSpace(in.PersonID)
	if personID == "" {
		personID = r.ResolvePersonID(ctx, in.Platform, in.UserID, in.PersonName, false)
	}
	if personID == "" {
		zap.L().Warn("人物事实写回失败：无法确定 person_id",
			zap.String("person_name", in.PersonName),
			zap.String("chat_id", chatID))
		return nil
	}
	if !r.IsKnown(ctx, personID) {
		zap.L().Warn("人物事实写回跳过：用户尚未认识",
			zap.String("person_name", in.PersonName),
			zap.String("person_id", personID))
		return nil
	}

	prof, err := r.GetProfile(ctx, personID)
	if err != nil {
		return err
	}
	participant := strings.TrimSpace(prof.PersonName)
	if participant == "" {
		participant = strings.TrimSpace(prof.Nickname)
	}
	if participant == "" {
		participant = strings.TrimSpace(in.PersonName)
	}

	host := memorix.DefaultRegistry().Host()
	if host == nil {
		return nil
	}

	fp := md5.Sum([]byte(personID + "|" + chatID + "|" + content))
	externalID := fmt.Sprintf("person_fact:%s:%s", personID, hex.EncodeToString(fp[:]))
	evidenceSource := strings.TrimSpace(in.EvidenceSource)
	if evidenceSource == "" {
		evidenceSource = "user_supported"
	}
	meta := map[string]any{
		"platform":             strings.TrimSpace(in.Platform),
		"person_id":            personID,
		"person_name":          participant,
		"writeback_source":     "memory_flow_service",
		"evidence_source":      evidenceSource,
		"evidence_message_ids": append([]string(nil), in.EvidenceMessageIDs...),
	}
	if in.FactClaim != nil {
		meta["fact_claim"] = cloneMapAny(in.FactClaim)
	}

	req := memorix.IngestMessageRequest{
		ExternalID:    externalID,
		SourceType:    "person_fact",
		Text:          content,
		ChatID:        chatID,
		PersonIDs:     []string{personID},
		Participants:  strSliceNonEmpty(participant),
		Tags:          []string{"person_fact"},
		Metadata:      meta,
		RespectFilter: true,
		UserID:        strings.TrimSpace(in.UserID),
		GroupID:       strings.TrimSpace(in.GroupID),
	}
	result, err := host.IngestMessage(ctx, req)
	if err != nil {
		zap.L().Warn("人物事实写回 Memorix 失败",
			zap.String("person_id", personID),
			zap.Error(err))
		return err
	}
	if !result.Success && !result.Disabled {
		zap.L().Warn("人物事实写回未成功",
			zap.String("person_id", personID),
			zap.String("detail", result.Detail))
	}
	return nil
}

func strSliceNonEmpty(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return []string{s}
}

func cloneMapAny(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

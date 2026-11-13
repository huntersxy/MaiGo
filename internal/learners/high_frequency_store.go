package learners

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
)

// MergeHighFrequencyTerms 对齐 _merge_high_frequency_terms：
// 同一聊天内按规范化词条合并计数，重排后裁剪到 maxTerms
func (s *GORMStore) MergeHighFrequencyTerms(ctx context.Context, chatID string, terms []HighFrequencyTermInput, generatedAt time.Time, maxTerms int) (int, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" || len(terms) == 0 {
		return 0, nil
	}
	if generatedAt.IsZero() {
		generatedAt = time.Now()
	}
	if maxTerms <= 0 {
		maxTerms = defaultHighFrequencyMaxTerms
	}
	merged := 0
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []HighFrequencyTerm
		if err := tx.Where("chat_id = ?", chatID).Find(&rows).Error; err != nil {
			return err
		}
		records := make([]*HighFrequencyTerm, 0, len(rows)+len(terms))
		for index := range rows {
			row := rows[index]
			records = append(records, &row)
		}
		byTerm := make(map[string]int, len(records))
		for index, record := range records {
			byTerm[NormalizeMatchText(record.Term)] = index
		}
		for _, input := range terms {
			key := NormalizeMatchText(input.Term)
			if key == "" {
				continue
			}
			index, ok := byTerm[key]
			if !ok {
				record := &HighFrequencyTerm{
					ChatID:    chatID,
					Term:      input.Term,
					CreatedAt: generatedAt,
					UpdatedAt: generatedAt,
				}
				records = append(records, record)
				index = len(records) - 1
				byTerm[key] = index
			}
			record := records[index]
			record.Term = input.Term
			record.OccurrenceCount += input.Count
			record.MessageCount += input.MessageCount
			record.UpdatedAt = generatedAt
			merged++
		}
		kept, removed := rerankHighFrequencyRecords(records, maxTerms)
		totalOccurrence := 0
		totalMessage := 0
		for _, record := range kept {
			totalOccurrence += record.OccurrenceCount
			totalMessage += record.MessageCount
		}
		for index, record := range kept {
			record.Rank = index + 1
			record.Frequency = ratio(record.OccurrenceCount, totalOccurrence)
			record.MessageFrequency = ratio(record.MessageCount, totalMessage)
			if record.ID == 0 {
				if err := tx.Create(record).Error; err != nil {
					return err
				}
				continue
			}
			if err := tx.Save(record).Error; err != nil {
				return err
			}
		}
		removedIDs := make([]uint, 0, len(removed))
		for _, record := range removed {
			if record.ID != 0 {
				removedIDs = append(removedIDs, record.ID)
			}
		}
		if len(removedIDs) > 0 {
			if err := tx.Delete(&HighFrequencyTerm{}, removedIDs).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return merged, nil
}

// rerankHighFrequencyRecords 对齐 _rerank_high_frequency_records 的排序与裁剪
func rerankHighFrequencyRecords(records []*HighFrequencyTerm, maxTerms int) ([]*HighFrequencyTerm, []*HighFrequencyTerm) {
	sorted := append([]*HighFrequencyTerm(nil), records...)
	sort.SliceStable(sorted, func(i, j int) bool {
		left, right := sorted[i], sorted[j]
		if left.OccurrenceCount != right.OccurrenceCount {
			return left.OccurrenceCount > right.OccurrenceCount
		}
		if left.MessageCount != right.MessageCount {
			return left.MessageCount > right.MessageCount
		}
		leftLen := utf8.RuneCountInString(left.Term)
		rightLen := utf8.RuneCountInString(right.Term)
		if leftLen != rightLen {
			return leftLen > rightLen
		}
		return left.Term < right.Term
	})
	if maxTerms < 1 {
		maxTerms = 1
	}
	if len(sorted) <= maxTerms {
		return sorted, nil
	}
	return sorted[:maxTerms], sorted[maxTerms:]
}

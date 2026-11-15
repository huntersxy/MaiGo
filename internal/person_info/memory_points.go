package personinfo

// person_info 记忆点与群名片兼容操作，对齐 MaiBot Person.del_memory/add_group_nick_name

import (
	"context"
	"fmt"
	"strings"
)

// LevenshteinDistance 按 Unicode 字符计算编辑距离
func LevenshteinDistance(left, right string) int {
	a := []rune(left)
	b := []rune(right)
	if len(a) < len(b) {
		a, b = b, a
	}
	if len(b) == 0 {
		return len(a)
	}
	previous := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 0; i < len(a); i++ {
		current := make([]int, len(b)+1)
		current[0] = i + 1
		for j := 0; j < len(b); j++ {
			insertions := previous[j+1] + 1
			deletions := current[j] + 1
			substitutions := previous[j]
			if a[i] != b[j] {
				substitutions++
			}
			current[j+1] = min(insertions, min(deletions, substitutions))
		}
		previous = current
	}
	return previous[len(b)]
}

// CalculateStringSimilarity 对齐 calculate_string_similarity
func CalculateStringSimilarity(left, right string) float64 {
	if left == right {
		return 1.0
	}
	if left == "" || right == "" {
		return 0.0
	}
	distance := LevenshteinDistance(left, right)
	leftLen := len([]rune(left))
	rightLen := len([]rune(right))
	maximum := leftLen
	if rightLen > maximum {
		maximum = rightLen
	}
	if maximum == 0 {
		return 1.0
	}
	return 1.0 - float64(distance)/float64(maximum)
}

// DeleteMemoryPoint 按分类与相似度删除记忆点，返回删除数量
func (r *Registry) DeleteMemoryPoint(ctx context.Context, personID, category, content string, threshold float64) (int, error) {
	personID = strings.TrimSpace(personID)
	category = strings.TrimSpace(category)
	content = strings.TrimSpace(content)
	if personID == "" {
		return 0, fmt.Errorf("personinfo: 删除记忆点需要 person_id")
	}
	if threshold <= 0 {
		threshold = 0.95
	}
	rec, err := r.loadPersonByID(ctx, personID)
	if err != nil {
		return 0, err
	}
	points := decodeMemoryPoints(rec.MemoryPoints)
	if len(points) == 0 {
		return 0, nil
	}
	kept := make([]string, 0, len(points))
	deleted := 0
	for _, point := range points {
		parts := strings.SplitN(point, ":", 3)
		if len(parts) < 3 {
			kept = append(kept, point)
			continue
		}
		if strings.TrimSpace(parts[0]) != category {
			kept = append(kept, point)
			continue
		}
		if CalculateStringSimilarity(content, strings.TrimSpace(parts[1])) >= threshold {
			deleted++
			continue
		}
		kept = append(kept, point)
	}
	if deleted == 0 {
		return 0, nil
	}
	memText, err := encodeMemoryPoints(kept)
	if err != nil {
		return 0, err
	}
	rec.MemoryPoints = jsonTextPtr(memText)
	if err := r.syncLoadedPerson(ctx, rec); err != nil {
		return 0, err
	}
	return deleted, nil
}

// AddGroupNickname 添加或更新群名片
func (r *Registry) AddGroupNickname(ctx context.Context, personID, groupID, groupNickname string) error {
	groupID = strings.TrimSpace(groupID)
	groupNickname = strings.TrimSpace(groupNickname)
	if groupID == "" || groupNickname == "" {
		return nil
	}
	rec, err := r.loadPersonByID(ctx, personID)
	if err != nil {
		return err
	}
	cards := upsertGroupCard(decodeGroupCards(rec.GroupCardname), groupID, groupNickname)
	cardText, err := encodeGroupCards(cards)
	if err != nil {
		return err
	}
	rec.GroupCardname = jsonTextPtr(cardText)
	return r.syncLoadedPerson(ctx, rec)
}

// GetPersonIDByPersonName 返回唯一精确匹配的 person_id
func (r *Registry) GetPersonIDByPersonName(ctx context.Context, personName string) string {
	name := strings.TrimSpace(personName)
	if name == "" {
		return ""
	}
	db := r.db(ctx)
	if db == nil || !db.Migrator().HasTable((PersonInfo{}).TableName()) {
		return ""
	}
	var ids []string
	if err := db.Model(&PersonInfo{}).Where("person_name = ?", name).Limit(2).Pluck("person_id", &ids).Error; err != nil {
		return ""
	}
	if len(ids) != 1 {
		return ""
	}
	return ids[0]
}

package personinfo

// 人物画像 schema：固定六分桶、证据链与画像文档编解码
// 对齐 MaiBot src/A_memorix/core/utils/profile_text.py 与 person_profile_service.py 的段落协议

import (
	"fmt"
	"strings"
)

const (
	// ProfileSectionIdentitySettings 身份设定
	ProfileSectionIdentitySettings = "identity_settings"
	// ProfileSectionRelationshipSettings 关系设定
	ProfileSectionRelationshipSettings = "relationship_settings"
	// ProfileSectionStableFacts 稳定了解
	ProfileSectionStableFacts = "stable_facts"
	// ProfileSectionInteractionPreferences 相处偏好
	ProfileSectionInteractionPreferences = "interaction_preferences"
	// ProfileSectionRecentInteractions 近期互动
	ProfileSectionRecentInteractions = "recent_interactions"
	// ProfileSectionUncertainNotes 不确定信息
	ProfileSectionUncertainNotes = "uncertain_notes"
)

const (
	// FactAuthorityDirectUser 用户直接陈述
	FactAuthorityDirectUser = "direct_user"
	// FactAuthoritySummaryDerived 摘要自动提取
	FactAuthoritySummaryDerived = "summary_derived"
	// FactAuthorityManual 手工维护
	FactAuthorityManual = "manual"
	// FactAuthorityImported 导入
	FactAuthorityImported = "imported"

	// FactStabilityStable 稳定事实
	FactStabilityStable = "stable"
	// FactStabilityTemporal 临时事实
	FactStabilityTemporal = "temporal"
	// FactStabilityUncertain 未确认事实
	FactStabilityUncertain = "uncertain"

	// EvidenceSourceUserSupported 目标用户原始发言支持
	EvidenceSourceUserSupported = "user_supported"
)

const (
	profileTextPrefix           = "# 人物画像"
	profileEmptyItem            = "- 暂无"
	profileMaintenanceNote      = "- 自动画像仅供内部参考；若与当前对话冲突，以当前对话为准。"
	profileSectionTitleIdentity = "身份设定"
	profileSectionTitleRelation = "关系设定"
	profileSectionTitleStable   = "稳定了解"
	profileSectionTitlePrefer   = "相处偏好"
	profileSectionTitleRecent   = "近期互动"
	profileSectionTitleUncert   = "不确定信息"
	profileSectionTitleMaint    = "维护备注"
)

// ProfileSectionOrder 固定段落顺序
var ProfileSectionOrder = []string{
	ProfileSectionIdentitySettings,
	ProfileSectionRelationshipSettings,
	ProfileSectionStableFacts,
	ProfileSectionInteractionPreferences,
	ProfileSectionRecentInteractions,
	ProfileSectionUncertainNotes,
}

var profileSectionTitles = map[string]string{
	ProfileSectionIdentitySettings:       profileSectionTitleIdentity,
	ProfileSectionRelationshipSettings:   profileSectionTitleRelation,
	ProfileSectionStableFacts:            profileSectionTitleStable,
	ProfileSectionInteractionPreferences: profileSectionTitlePrefer,
	ProfileSectionRecentInteractions:     profileSectionTitleRecent,
	ProfileSectionUncertainNotes:         profileSectionTitleUncert,
}

var profileTitleSections = map[string]string{
	profileSectionTitleIdentity: ProfileSectionIdentitySettings,
	profileSectionTitleRelation: ProfileSectionRelationshipSettings,
	profileSectionTitleStable:   ProfileSectionStableFacts,
	profileSectionTitlePrefer:   ProfileSectionInteractionPreferences,
	profileSectionTitleRecent:   ProfileSectionRecentInteractions,
	profileSectionTitleUncert:   ProfileSectionUncertainNotes,
	profileSectionTitleMaint:    "maintenance_notes",
}

// ProfileDocument 可编辑的结构化人物画像
type ProfileDocument struct {
	PersonID    string              `json:"person_id"`
	PrimaryName string              `json:"primary_name,omitempty"`
	Aliases     []string            `json:"aliases,omitempty"`
	Sections    map[string][]string `json:"sections,omitempty"`
}

// ProfileEvidence 画像证据链条目
type ProfileEvidence struct {
	EvidenceKey    string         `json:"evidence_key,omitempty"`
	EvidenceType   string         `json:"evidence_type,omitempty"`
	Hash           string         `json:"hash,omitempty"`
	Content        string         `json:"content,omitempty"`
	Source         string         `json:"source,omitempty"`
	SourceType     string         `json:"source_type,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	Score          any            `json:"score,omitempty"`
	Confidence     any            `json:"confidence,omitempty"`
	CorrectionMode string         `json:"correction_mode,omitempty"`
	Deletable      bool           `json:"deletable"`
	NotDeletable   string         `json:"not_deletable_reason,omitempty"`
	Raw            map[string]any `json:"raw,omitempty"`
}

// ProfileUncertainCandidate 未确认事实候选
type ProfileUncertainCandidate struct {
	ClaimID string `json:"claim_id,omitempty"`
	Text    string `json:"text,omitempty"`
}

// ProfileSnapshot 画像查询结果
type ProfileSnapshot struct {
	Success             bool                        `json:"success"`
	PersonID            string                      `json:"person_id,omitempty"`
	PersonName          string                      `json:"person_name,omitempty"`
	ProfileText         string                      `json:"profile_text,omitempty"`
	Summary             string                      `json:"summary,omitempty"`
	Traits              []string                    `json:"traits,omitempty"`
	Aliases             []string                    `json:"aliases,omitempty"`
	Sections            map[string][]string         `json:"sections,omitempty"`
	ProfileSource       string                      `json:"profile_source,omitempty"`
	AutoProfileText     string                      `json:"auto_profile_text,omitempty"`
	ManualOverrideText  string                      `json:"manual_override_text,omitempty"`
	HasManualOverride   bool                        `json:"has_manual_override"`
	FromCache           bool                        `json:"from_cache"`
	EvidenceUnchanged   bool                        `json:"evidence_unchanged,omitempty"`
	ProfileVersion      int64                       `json:"profile_version,omitempty"`
	UpdatedAt           any                         `json:"updated_at,omitempty"`
	ExpiresAt           any                         `json:"expires_at,omitempty"`
	Evidence            []ProfileEvidence           `json:"evidence,omitempty"`
	UncertainCandidates []ProfileUncertainCandidate `json:"uncertain_candidates,omitempty"`
	UncertainFactCount  int                         `json:"uncertain_fact_count,omitempty"`
	Error               string                      `json:"error,omitempty"`
	Raw                 map[string]any              `json:"raw,omitempty"`
}

// ProfileSectionTitle 返回分桶中文标题
func ProfileSectionTitle(section string) string {
	return profileSectionTitles[strings.TrimSpace(section)]
}

// NormalizeProfileSection 规范化分桶名，支持键名与中文标题
func NormalizeProfileSection(value string) (string, error) {
	token := strings.TrimSpace(value)
	if _, ok := profileSectionTitles[token]; ok {
		return token, nil
	}
	if key, ok := profileTitleSections[token]; ok && key != "maintenance_notes" {
		return key, nil
	}
	return "", fmt.Errorf("不支持的人物画像段落: %s", token)
}

// BuildProfileDocument 按固定格式构建画像文本
func BuildProfileDocument(doc ProfileDocument) string {
	aliasItems := make([]string, 0, len(doc.Aliases))
	for _, alias := range doc.Aliases {
		if text := strings.TrimSpace(alias); text != "" {
			aliasItems = append(aliasItems, text)
		}
	}
	limits := map[string]int{
		ProfileSectionIdentitySettings:       4,
		ProfileSectionRelationshipSettings:   4,
		ProfileSectionStableFacts:            6,
		ProfileSectionInteractionPreferences: 5,
		ProfileSectionRecentInteractions:     3,
		ProfileSectionUncertainNotes:         3,
	}
	lines := []string{
		profileTextPrefix,
		"人物ID: " + strings.TrimSpace(doc.PersonID),
		"主称呼: " + strings.TrimSpace(doc.PrimaryName),
		"别名: " + strings.Join(aliasItems, "、"),
	}
	for _, section := range ProfileSectionOrder {
		title := profileSectionTitles[section]
		lines = append(lines, "", "## "+title)
		bullets := dedupeProfileBullets(doc.Sections[section], limits[section])
		if len(bullets) == 0 {
			lines = append(lines, profileEmptyItem)
			continue
		}
		lines = append(lines, bullets...)
	}
	lines = append(lines, "", "## "+profileSectionTitleMaint, profileMaintenanceNote)
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// ParseProfileDocument 从画像文本解析固定段落
func ParseProfileDocument(text string) ProfileDocument {
	doc := ProfileDocument{Sections: map[string][]string{}}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	current := ""
	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t\r")
		stripped := strings.TrimSpace(line)
		if strings.HasPrefix(stripped, "## ") {
			title := strings.TrimSpace(strings.TrimPrefix(stripped, "## "))
			current = profileTitleSections[title]
			if current != "" {
				if _, ok := doc.Sections[current]; !ok {
					doc.Sections[current] = []string{}
				}
			}
			continue
		}
		if current == "" {
			if strings.HasPrefix(stripped, "人物ID:") {
				doc.PersonID = strings.TrimSpace(strings.TrimPrefix(stripped, "人物ID:"))
			} else if strings.HasPrefix(stripped, "主称呼:") {
				doc.PrimaryName = strings.TrimSpace(strings.TrimPrefix(stripped, "主称呼:"))
			} else if strings.HasPrefix(stripped, "别名:") {
				aliasText := strings.TrimSpace(strings.TrimPrefix(stripped, "别名:"))
				for _, item := range strings.Split(aliasText, "、") {
					if item = strings.TrimSpace(item); item != "" {
						doc.Aliases = append(doc.Aliases, item)
					}
				}
			}
			continue
		}
		doc.Sections[current] = append(doc.Sections[current], stripped)
	}
	return doc
}

// IsStructuredProfileText 判断文本是否符合结构化画像协议
func IsStructuredProfileText(text string) bool {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, profileTextPrefix) {
		return false
	}
	doc := ParseProfileDocument(trimmed)
	for _, section := range ProfileSectionOrder {
		if _, ok := doc.Sections[section]; !ok {
			return false
		}
	}
	return true
}

// ParseProfileSections 返回固定段落及维护备注
func ParseProfileSections(text string) map[string][]string {
	doc := ParseProfileDocument(text)
	return doc.Sections
}

// BuildProfileInjectionText 构造紧凑注入文本
func BuildProfileInjectionText(text string, recentLimit int, uncertainCandidates []string) string {
	if !IsStructuredProfileText(text) {
		return strings.TrimSpace(text)
	}
	if recentLimit < 0 {
		recentLimit = 0
	}
	doc := ParseProfileDocument(text)
	var lines []string
	meaningful := false
	for _, section := range []string{
		ProfileSectionIdentitySettings,
		ProfileSectionRelationshipSettings,
		ProfileSectionStableFacts,
		ProfileSectionInteractionPreferences,
		ProfileSectionRecentInteractions,
	} {
		items := append([]string(nil), doc.Sections[section]...)
		if section == ProfileSectionRecentInteractions && len(items) > recentLimit {
			items = items[:recentLimit]
		}
		if sectionIsEmpty(items) {
			continue
		}
		meaningful = true
		lines = append(lines, "## "+profileSectionTitles[section])
		lines = append(lines, items...)
		lines = append(lines, "")
	}
	uncertain := dedupeProfileBullets(uncertainCandidates, 2)
	if len(uncertain) > 0 {
		lines = append(lines, "## 不确定信息（未确认，不可当作确定事实）")
		lines = append(lines, uncertain...)
		lines = append(lines, "")
	} else if !meaningful {
		fallback := doc.Sections[ProfileSectionUncertainNotes]
		if len(fallback) > 1 {
			fallback = fallback[:1]
		}
		if !sectionIsEmpty(fallback) {
			lines = append(lines, "## 不确定信息（未确认）")
			lines = append(lines, fallback...)
			lines = append(lines, "")
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func dedupeProfileBullets(values []string, limit int) []string {
	if limit <= 0 {
		limit = 1
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, limit)
	for _, value := range values {
		text := strings.TrimSpace(value)
		for strings.HasPrefix(text, "-") {
			text = strings.TrimSpace(strings.TrimPrefix(text, "-"))
		}
		if text == "" || "- "+text == profileEmptyItem {
			continue
		}
		bullet := "- " + text
		key := strings.ToLower(bullet)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, bullet)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func sectionIsEmpty(lines []string) bool {
	for _, line := range lines {
		item := strings.TrimSpace(line)
		if item != "" && item != profileEmptyItem {
			return false
		}
	}
	return true
}

// ProfileTraits 从画像文本提取要点，供工具展示
func ProfileTraits(text string, limit int) []string {
	if limit <= 0 {
		limit = 8
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		item := strings.TrimSpace(strings.Trim(line, "-"))
		if item == "" || strings.HasPrefix(item, "#") {
			continue
		}
		out = append(out, item)
		if len(out) >= limit {
			break
		}
	}
	return out
}

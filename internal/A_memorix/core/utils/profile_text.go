package utils

// A_memorix 人物画像结构化文本助手，对齐 Python core/utils/profile_text.py

import (
	"fmt"
	"strings"
)

// ProfileSectionTitles 固定画像段落标题
var ProfileSectionTitles = []string{"身份设定", "关系设定", "稳定了解", "相处偏好", "近期互动", "不确定信息", "维护备注"}

// ProfileInjectionSectionTitles 参与注入的画像段落标题
var ProfileInjectionSectionTitles = []string{"身份设定", "关系设定", "稳定了解", "相处偏好", "近期互动"}

const (
	// ProfileRequiredPrefix 结构化画像必需前缀
	ProfileRequiredPrefix = "# 人物画像"
	// ProfileEmptyItem 空段落占位项
	ProfileEmptyItem = "- 暂无"
	// ProfileMaintenanceNote 维护备注固定文案
	ProfileMaintenanceNote = "- 自动画像仅供内部参考；若与当前对话冲突，以当前对话为准。"
)

// CleanProfileBullet 对齐 clean_profile_bullet，规范化单条画像要点
func CleanProfileBullet(value string) string {
	text := pyStrip(value)
	if text == "" {
		return ""
	}
	for strings.HasPrefix(text, "-") {
		text = pyStrip(text[1:])
	}
	if text == "" {
		return ""
	}
	return "- " + text
}

// DedupeProfileBullets 对齐 dedupe_profile_bullets，去重并裁剪画像要点
func DedupeProfileBullets(values []string, limit int) []string {
	bullets := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		bullet := CleanProfileBullet(value)
		if bullet == "" || bullet == ProfileEmptyItem {
			continue
		}
		key := pyLower(bullet)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		bullets = append(bullets, bullet)
		if limit < 1 {
			limit = 1
		}
		if len(bullets) >= limit {
			break
		}
	}
	return bullets
}

// SectionIsEmpty 对齐 section_is_empty，判断段落是否没有有效内容
func SectionIsEmpty(lines []string) bool {
	if len(lines) == 0 {
		return true
	}
	for _, line := range lines {
		item := pyStrip(line)
		if item != "" && item != ProfileEmptyItem {
			return false
		}
	}
	return true
}

// StructuredProfileInput 构建结构化画像的输入
type StructuredProfileInput struct {
	PersonID               string
	PrimaryName            string
	Aliases                []string
	IdentitySettings       []string
	RelationshipSettings   []string
	StableFacts            []string
	InteractionPreferences []string
	RecentInteractions     []string
	UncertainNotes         []string
}

// BuildStructuredProfileText 对齐 build_structured_profile_text，构建固定格式画像
func BuildStructuredProfileText(input StructuredProfileInput) string {
	aliasItems := make([]string, 0, len(input.Aliases))
	for _, item := range input.Aliases {
		if trimmed := pyStrip(item); trimmed != "" {
			aliasItems = append(aliasItems, trimmed)
		}
	}
	aliasText := strings.Join(aliasItems, "、")

	sections := map[string][]string{
		"身份设定":  DedupeProfileBullets(input.IdentitySettings, 4),
		"关系设定":  DedupeProfileBullets(input.RelationshipSettings, 4),
		"稳定了解":  DedupeProfileBullets(input.StableFacts, 6),
		"相处偏好":  DedupeProfileBullets(input.InteractionPreferences, 5),
		"近期互动":  DedupeProfileBullets(input.RecentInteractions, 3),
		"不确定信息": DedupeProfileBullets(input.UncertainNotes, 3),
		"维护备注":  {ProfileMaintenanceNote},
	}

	lines := []string{
		ProfileRequiredPrefix,
		"人物ID: " + pyStrip(input.PersonID),
		"主称呼: " + pyStrip(input.PrimaryName),
		"别名: " + aliasText,
	}
	for _, title := range ProfileSectionTitles {
		lines = append(lines, "", "## "+title)
		sectionLines := sections[title]
		if len(sectionLines) == 0 {
			lines = append(lines, ProfileEmptyItem)
			continue
		}
		lines = append(lines, sectionLines...)
	}
	return pyStrip(strings.Join(lines, "\n"))
}

// ParseProfileSections 对齐 parse_profile_sections，解析固定段落
func ParseProfileSections(profileText string) map[string][]string {
	sections := make(map[string][]string)
	currentTitle := ""
	for _, rawLine := range splitLinesPython(profileText) {
		stripped := pyStrip(rawLine)
		if strings.HasPrefix(stripped, "## ") {
			title := pyStrip(stripped[3:])
			currentTitle = ""
			if profileSectionTitleKnown(title) {
				currentTitle = title
				if _, ok := sections[currentTitle]; !ok {
					sections[currentTitle] = []string{}
				}
			}
			continue
		}
		if currentTitle != "" {
			sections[currentTitle] = append(sections[currentTitle], stripped)
		}
	}
	return sections
}

// IsStructuredProfileText 对齐 is_structured_profile_text
func IsStructuredProfileText(profileText string) bool {
	text := pyStrip(profileText)
	if !strings.HasPrefix(text, ProfileRequiredPrefix) {
		return false
	}
	sections := ParseProfileSections(text)
	for _, title := range ProfileSectionTitles {
		if _, ok := sections[title]; !ok {
			return false
		}
	}
	return true
}

// ReplaceProfileSection 对齐 replace_profile_section，替换单个段落
func ReplaceProfileSection(profileText string, title string, newLines []string) (string, error) {
	cleanTitle := pyStrip(title)
	if !profileSectionTitleKnown(cleanTitle) {
		return "", fmt.Errorf("不支持的人物画像段落: %s", cleanTitle)
	}

	lines := splitLinesPython(profileText)
	out := make([]string, 0, len(lines)+len(newLines)+2)
	replacement := DedupeProfileBullets(newLines, 20)
	if len(replacement) == 0 {
		replacement = []string{ProfileEmptyItem}
	}

	index := 0
	replaced := false
	for index < len(lines) {
		line := lines[index]
		if pyStrip(line) == "## "+cleanTitle {
			out = append(out, line)
			out = append(out, replacement...)
			replaced = true
			index++
			for index < len(lines) && !strings.HasPrefix(pyStrip(lines[index]), "## ") {
				index++
			}
			continue
		}
		out = append(out, line)
		index++
	}

	if !replaced {
		if len(out) > 0 && pyStrip(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, "## "+cleanTitle)
		out = append(out, replacement...)
	}
	return pyStrip(strings.Join(out, "\n")), nil
}

// BuildProfileInjectionText 对齐 build_profile_injection_text，构建紧凑注入文本
func BuildProfileInjectionText(
	profileText string,
	recentLimit int,
	uncertainFallbackLimit int,
	uncertainCandidates []string,
	includeUncertainFallback bool,
) string {
	text := pyStrip(profileText)
	if !IsStructuredProfileText(text) {
		return text
	}

	if recentLimit < 0 {
		recentLimit = 0
	}
	if uncertainFallbackLimit < 0 {
		uncertainFallbackLimit = 0
	}

	sections := ParseProfileSections(text)
	selected := make([]string, 0, len(ProfileInjectionSectionTitles)*4)
	meaningfulFound := false
	for _, title := range ProfileInjectionSectionTitles {
		lines := sections[title]
		if title == "近期互动" && len(lines) > recentLimit {
			lines = lines[:recentLimit]
		}
		if SectionIsEmpty(lines) {
			continue
		}
		meaningfulFound = true
		selected = append(selected, "## "+title)
		selected = append(selected, lines...)
		selected = append(selected, "")
	}

	selectedUncertain := DedupeProfileBullets(uncertainCandidates, 2)
	if !SectionIsEmpty(selectedUncertain) {
		selected = append(selected, "## 不确定信息（未确认，不可当作确定事实）")
		selected = append(selected, selectedUncertain...)
		selected = append(selected, "")
	} else if !meaningfulFound && includeUncertainFallback {
		uncertain := sections["不确定信息"]
		if len(uncertain) > uncertainFallbackLimit {
			uncertain = uncertain[:uncertainFallbackLimit]
		}
		if !SectionIsEmpty(uncertain) {
			selected = append(selected, "## 不确定信息（未确认）")
			selected = append(selected, uncertain...)
			selected = append(selected, "")
		}
	}

	return pyStrip(strings.Join(selected, "\n"))
}

// profileSectionTitleKnown 判断标题是否属于固定段落
func profileSectionTitleKnown(title string) bool {
	for _, candidate := range ProfileSectionTitles {
		if candidate == title {
			return true
		}
	}
	return false
}

// splitLinesPython 对齐 str.splitlines() 支持的换行集合
func splitLinesPython(text string) []string {
	if text == "" {
		return nil
	}
	lines := make([]string, 0, strings.Count(text, "\n")+1)
	var builder strings.Builder
	runes := []rune(text)
	for index := 0; index < len(runes); index++ {
		char := runes[index]
		if char == '\r' {
			if index+1 < len(runes) && runes[index+1] == '\n' {
				index++
			}
			lines = append(lines, builder.String())
			builder.Reset()
			continue
		}
		if isPythonLineBreak(char) {
			lines = append(lines, builder.String())
			builder.Reset()
			continue
		}
		builder.WriteRune(char)
	}
	if builder.Len() > 0 {
		lines = append(lines, builder.String())
	}
	return lines
}

// isPythonLineBreak 判断 Python splitlines 识别的非 CR 换行符
func isPythonLineBreak(char rune) bool {
	switch char {
	case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
		return true
	default:
		return false
	}
}

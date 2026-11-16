package search

import (
	"regexp"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// casefold 对齐 Python str.casefold()：全量折叠而非简单小写
func casefold(value string) string {
	return cases.Fold().String(value)
}

var _ = language.Und

// GroundingError 表示回答包含本次检索证据中找不到依据的技术项
type GroundingError struct{ Message string }

func (e *GroundingError) Error() string { return e.Message }

var (
	backtickTechnicalPattern  = regexp.MustCompile(`[_./\[\]=]|\d|^--`)
	snakeCasePattern          = regexp.MustCompile(`(?i)\b([a-z][a-z0-9]*(?:_[a-z0-9]+)+)\b`)
	tomlPathPattern           = regexp.MustCompile(`(?i)\b([\w.-]+\.toml)\b`)
	escapedQuotePattern       = regexp.MustCompile(`\\(["'])`)
	assignmentPattern         = regexp.MustCompile(`^(.+?)\s*=\s*(.+)$`)
	httpClaimPattern          = regexp.MustCompile(`(?i)^(get|post|put|patch|delete)\s+(\S+)$`)
	sectionFieldPattern       = regexp.MustCompile(`^\[([\w.-]+)\]\s*([\w.-]+)$`)
	packageRequirementPattern = regexp.MustCompile(`^([a-z0-9_.-]+)\s*((?:===|==|~=|!=|<=|>=|<|>)\s*\d[\w.*+,!<>=~\s-]*)$`)
	packageEvidencePattern    = regexp.MustCompile(`"name"\s*:\s*"([a-z0-9_.-]+)"\s*,\s*"version_spec"\s*:\s*"([^"]+)"`)
)

// ValidateOutputEvidence 与 Python ai_search_grounding.validate_model_output_evidence 对齐
func ValidateOutputEvidence(output ModelOutput, evidence string) error {
	normalizedEvidence := casefold(evidence)
	unsupported := make([]string, 0, 4)
	seen := map[string]struct{}{}
	for _, content := range append([]string{output.Answer}, output.Suggestions...) {
		for _, claim := range extractVerifiableClaims(content) {
			normalizedClaim := casefold(claim)
			if _, exists := seen[normalizedClaim]; exists {
				continue
			}
			if isClaimSupportedByEvidence(normalizedClaim, normalizedEvidence) {
				continue
			}
			seen[normalizedClaim] = struct{}{}
			unsupported = append(unsupported, claim)
		}
	}
	if len(unsupported) == 0 {
		return nil
	}
	limit := len(unsupported)
	if limit > 6 {
		limit = 6
	}
	claims := make([]string, 0, limit)
	for _, claim := range unsupported[:limit] {
		claims = append(claims, "`"+claim+"`")
	}
	return &GroundingError{Message: "回答包含未在本次 AI 获取到的检索或读取证据中找到依据的技术项：" +
		strings.Join(claims, "、") + "；这不代表它们在整个项目中一定不存在"}
}

func extractVerifiableClaims(text string) []string {
	claims := make([]string, 0, 4)
	seen := map[string]struct{}{}
	addClaim := func(raw string) {
		claim := normalizeVerifiableClaim(raw)
		if claim == "" {
			return
		}
		if _, exists := seen[claim]; exists {
			return
		}
		seen[claim] = struct{}{}
		claims = append(claims, claim)
	}
	for _, match := range findBacktickClaims(text) {
		if backtickTechnicalPattern.MatchString(match) {
			addClaim(match)
		}
	}
	for _, match := range snakeCasePattern.FindAllStringSubmatch(text, -1) {
		addClaim(match[1])
	}
	for _, match := range tomlPathPattern.FindAllStringSubmatch(text, -1) {
		addClaim(match[1])
	}
	return claims
}

// findBacktickClaims 复刻 Python `(?<!`)`([^`\n]+)`(?!`)` 的前后瞻语义
//
// Go 的 RE2 不支持 lookaround：这里扫描单反引号区间，并跳过成对反引号围栏内部的内容。
func findBacktickClaims(text string) []string {
	claims := make([]string, 0, 4)
	for index := 0; index < len(text); index++ {
		if text[index] != '`' {
			continue
		}
		// 连续两个反引号属于围栏，不是行内代码
		if index+1 < len(text) && text[index+1] == '`' {
			index++
			continue
		}
		if index > 0 && text[index-1] == '`' {
			continue
		}
		end := index + 1
		for end < len(text) && text[end] != '`' && text[end] != '\n' {
			end++
		}
		if end >= len(text) || text[end] != '`' {
			continue
		}
		if end+1 < len(text) && text[end+1] == '`' {
			index = end
			continue
		}
		claims = append(claims, text[index+1:end])
		index = end
	}
	return claims
}

func normalizeVerifiableClaim(claim string) string {
	normalized := escapedQuotePattern.ReplaceAllString(strings.TrimSpace(claim), "$1")
	for len(normalized) >= 2 && normalized[0] == normalized[len(normalized)-1] &&
		(normalized[0] == '"' || normalized[0] == '\'') {
		normalized = strings.TrimSpace(normalized[1 : len(normalized)-1])
	}
	return normalized
}

func compactWhitespace(value string) string {
	return strings.Join(strings.Fields(value), "")
}

func isClaimSupportedByEvidence(claim, evidence string) bool {
	if strings.Contains(evidence, claim) {
		return true
	}
	if strings.HasSuffix(claim, "*") && strings.Count(claim, "*") == 1 {
		return strings.Contains(evidence, strings.TrimSuffix(claim, "*"))
	}
	if match := packageRequirementPattern.FindStringSubmatch(claim); match != nil {
		name := match[1]
		specifier := compactWhitespace(match[2])
		for _, evidenceMatch := range packageEvidencePattern.FindAllStringSubmatch(evidence, -1) {
			if evidenceMatch[1] == name && compactWhitespace(evidenceMatch[2]) == specifier {
				return true
			}
		}
	}
	if match := assignmentPattern.FindStringSubmatch(claim); match != nil {
		fieldName := strings.TrimSpace(match[1])
		fieldValue := strings.TrimSpace(match[2])
		return isConfigFieldSupported(fieldName, evidence) && strings.Contains(evidence, fieldValue)
	}
	if match := httpClaimPattern.FindStringSubmatch(claim); match != nil {
		return strings.Contains(evidence, strings.ToLower(match[1])) && strings.Contains(evidence, match[2])
	}
	return isConfigFieldSupported(claim, evidence)
}

func isConfigFieldSupported(fieldName, evidence string) bool {
	if match := sectionFieldPattern.FindStringSubmatch(fieldName); match != nil {
		fieldName = match[1] + "." + match[2]
	}
	if strings.Contains(evidence, fieldName) {
		return true
	}
	index := strings.LastIndex(fieldName, ".")
	if index < 0 {
		return false
	}
	section := fieldName[:index]
	leaf := fieldName[index+1:]
	return strings.Contains(evidence, "["+section+"]") && strings.Contains(evidence, leaf)
}

// ExtractVerifiableClaims 对齐 ai_search_grounding.extract_verifiable_claims
func ExtractVerifiableClaims(text string) []string {
	return extractVerifiableClaims(text)
}

// NormalizeVerifiableClaim 对齐 ai_search_grounding.normalize_verifiable_claim
func NormalizeVerifiableClaim(claim string) string {
	return normalizeVerifiableClaim(claim)
}

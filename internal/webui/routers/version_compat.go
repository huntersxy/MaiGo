package webuiapi

import (
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	appversion "maibot-go/internal/common/version"
)

const (
	// requiredWebUIVersion 对齐 MaiBot 1.3.19 pyproject.toml:32 "maibot-dashboard==1.9.13"
	requiredWebUIVersion = appversion.RequiredWebUIVersion
	// fallbackMainProgramVersion 是 Python read_project_version 在 pyproject.toml:7 读取的目标基线版本
	// 唯一定义在 internal/version.Baseline，Dockerfile:35 可通过 -X maibot-go/internal/version.Build=<version> 覆盖真实构建版本
	fallbackMainProgramVersion = appversion.Baseline

	webUICompatibilityCompatible          = "compatible"
	webUICompatibilityWebUIOutdated       = "webui_outdated"
	webUICompatibilityMainProgramOutdated = "main_program_outdated"
)

// webUIVersionPattern 对齐 MaiBot src/webui/version_compatibility.py:23-31 的 PEP440 子集
var webUIVersionPattern = regexp.MustCompile(`(?i)^v?(\d+(?:\.\d+)*)(?:(a|b|rc)(\d+))?(?:\.?dev(\d+))?(?:\.?post(\d+))?(?:\+[0-9a-z.-]+)?$`)

type parsedWebUIVersion struct {
	release []*big.Int
	phase   [4]*big.Int
}

type webUIVersionCompatibilityResponse struct {
	Status               string `json:"status"`
	MainProgramVersion   string `json:"main_program_version"`
	WebUIVersion         string `json:"webui_version"`
	RequiredWebUIVersion string `json:"required_webui_version"`
}

// fastAPIValidationIssue 复刻 FastAPI Query 校验失败时的 detail 元素
type fastAPIValidationIssue struct {
	Type  string `json:"type"`
	Loc   []any  `json:"loc"`
	Msg   string `json:"msg"`
	Input any    `json:"input"`
	Ctx   any    `json:"ctx,omitempty"`
}

// parseWebUIVersion 对齐 Python _parse_version：先 strip 再整体匹配，失败错误带原始入参
func parseWebUIVersion(version string) (parsedWebUIVersion, error) {
	matches := webUIVersionPattern.FindStringSubmatch(strings.TrimSpace(version))
	if matches == nil {
		return parsedWebUIVersion{}, fmt.Errorf("不支持的版本号格式: %s", version)
	}

	releaseParts := strings.Split(matches[1], ".")
	release := make([]*big.Int, len(releaseParts))
	for index, part := range releaseParts {
		release[index] = decimalVersionNumber(part)
	}

	pre := strings.ToLower(matches[2])
	preNumber := decimalVersionNumber(matches[3])
	devNumber := decimalVersionNumber(matches[4])
	postNumber := decimalVersionNumber(matches[5])

	var phase [4]*big.Int
	switch {
	case pre != "":
		rank := big.NewInt(0)
		switch pre {
		case "a":
			rank = big.NewInt(1)
		case "b":
			rank = big.NewInt(2)
		case "rc":
			rank = big.NewInt(3)
		}
		devFlag := int64(1)
		if matches[4] != "" {
			devFlag = 0
		}
		phase = [4]*big.Int{rank, preNumber, big.NewInt(devFlag), devNumber}
	case matches[4] != "":
		phase = [4]*big.Int{big.NewInt(0), devNumber, big.NewInt(0), big.NewInt(0)}
	case matches[5] != "":
		phase = [4]*big.Int{big.NewInt(5), postNumber, big.NewInt(0), big.NewInt(0)}
	default:
		phase = [4]*big.Int{big.NewInt(4), big.NewInt(0), big.NewInt(0), big.NewInt(0)}
	}

	return parsedWebUIVersion{release: release, phase: phase}, nil
}

// decimalVersionNumber 用大整数保存版本号数字，避免 Python 任意精度整数在 Go 侧溢出
func decimalVersionNumber(raw string) *big.Int {
	if raw == "" {
		return big.NewInt(0)
	}
	value, ok := new(big.Int).SetString(raw, 10)
	if !ok {
		return big.NewInt(0)
	}
	return value
}

// compareParsedWebUIVersions 对齐 Python compare_versions，release 右补 0 后依次比较
func compareParsedWebUIVersions(left, right parsedWebUIVersion) int {
	releaseLength := len(left.release)
	if len(right.release) > releaseLength {
		releaseLength = len(right.release)
	}
	for index := 0; index < releaseLength; index++ {
		leftValue := big.NewInt(0)
		if index < len(left.release) {
			leftValue = left.release[index]
		}
		rightValue := big.NewInt(0)
		if index < len(right.release) {
			rightValue = right.release[index]
		}
		if comparison := leftValue.Cmp(rightValue); comparison != 0 {
			return comparison
		}
	}
	for index := 0; index < 4; index++ {
		if comparison := left.phase[index].Cmp(right.phase[index]); comparison != 0 {
			return comparison
		}
	}
	return 0
}

// sameParsedWebUIRelease 对齐 Python _has_same_release：忽略末尾 0 后 release 相等
func sameParsedWebUIRelease(left, right parsedWebUIVersion) bool {
	releaseLength := len(left.release)
	if len(right.release) > releaseLength {
		releaseLength = len(right.release)
	}
	for index := 0; index < releaseLength; index++ {
		leftValue := big.NewInt(0)
		if index < len(left.release) {
			leftValue = left.release[index]
		}
		rightValue := big.NewInt(0)
		if index < len(right.release) {
			rightValue = right.release[index]
		}
		if leftValue.Cmp(rightValue) != 0 {
			return false
		}
	}
	return true
}

// webUIVersionCompatibilityStatus 对齐 Python get_webui_version_compatibility 的三态判断
func webUIVersionCompatibilityStatus(webuiVersion string) (string, error) {
	required, err := parseWebUIVersion(requiredWebUIVersion)
	if err != nil {
		return "", fmt.Errorf("解析 required_webui_version 失败: %w", err)
	}
	webui, err := parseWebUIVersion(webuiVersion)
	if err != nil {
		return "", err
	}

	comparison := compareParsedWebUIVersions(webui, required)
	if required.phase[0].Cmp(big.NewInt(4)) < 0 {
		return webUICompatibilityCompatible, nil
	}
	if comparison < 0 {
		return webUICompatibilityWebUIOutdated, nil
	}
	if !sameParsedWebUIRelease(webui, required) {
		return webUICompatibilityMainProgramOutdated, nil
	}
	return webUICompatibilityCompatible, nil
}

// mainProgramVersion 返回 Python read_project_version 对等的主程序版本
// 版本来源统一由 Deps.buildVersion 解析，避免各端点各自回退
func (d Deps) mainProgramVersion() string {
	return d.buildVersion()
}

func writeFastAPIValidationError(w http.ResponseWriter, issue fastAPIValidationIssue) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"detail": []fastAPIValidationIssue{issue},
	})
}

func (d Deps) handleVersionCompatibility(w http.ResponseWriter, r *http.Request) {
	queryValues, present := r.URL.Query()["webui_version"]
	if !present {
		writeFastAPIValidationError(w, fastAPIValidationIssue{
			Type: "missing",
			Loc:  []any{"query", "webui_version"},
			Msg:  "Field required",
		})
		return
	}
	webuiVersion := ""
	if len(queryValues) > 0 {
		webuiVersion = queryValues[len(queryValues)-1]
	}
	length := utf8.RuneCountInString(webuiVersion)
	if length < 1 {
		writeFastAPIValidationError(w, fastAPIValidationIssue{
			Type:  "string_too_short",
			Loc:   []any{"query", "webui_version"},
			Msg:   "String should have at least 1 character",
			Input: webuiVersion,
			Ctx:   map[string]any{"min_length": 1},
		})
		return
	}
	if length > 64 {
		writeFastAPIValidationError(w, fastAPIValidationIssue{
			Type:  "string_too_long",
			Loc:   []any{"query", "webui_version"},
			Msg:   "String should have at most 64 characters",
			Input: webuiVersion,
			Ctx:   map[string]any{"max_length": 64},
		})
		return
	}

	status, err := webUIVersionCompatibilityStatus(webuiVersion)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, webUIVersionCompatibilityResponse{
		Status:               status,
		MainProgramVersion:   d.mainProgramVersion(),
		WebUIVersion:         webuiVersion,
		RequiredWebUIVersion: requiredWebUIVersion,
	})
}

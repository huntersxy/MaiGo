package search

// 答疑 Agent 的结构化成果发布，对齐 src/webui/services/ai_help_artifacts.py

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	"github.com/cloudwego/eino/schema"
)

const (
	publishHelpArtifactTool = "publish_help_artifact"
	maxHelpArtifacts        = 4
	maxHelpSVGChars         = 32000
	maxHelpMarkdownChars    = 16000
	maxHelpImageURLChars    = 2048
	maxHelpSVGElements      = 180
	maxHelpSVGDepth         = 12
	reasoningImagePath      = "/api/webui/reasoning-process/image"
)

var helpSVGGeometry = map[string]map[string]struct{}{
	"svg":      {"viewBox": {}, "width": {}, "height": {}},
	"g":        {},
	"rect":     {"x": {}, "y": {}, "width": {}, "height": {}, "rx": {}, "ry": {}},
	"circle":   {"cx": {}, "cy": {}, "r": {}},
	"ellipse":  {"cx": {}, "cy": {}, "rx": {}, "ry": {}},
	"line":     {"x1": {}, "y1": {}, "x2": {}, "y2": {}},
	"path":     {"d": {}},
	"polygon":  {"points": {}},
	"polyline": {"points": {}},
	"text":     {"x": {}, "y": {}, "dx": {}, "dy": {}},
	"tspan":    {"x": {}, "y": {}, "dx": {}, "dy": {}},
	"title":    {},
	"desc":     {},
}

var (
	helpNumber      = `[+-]?(?:[0-9]{1,5}(?:\.[0-9]{0,6})?|\.[0-9]{1,6})`
	helpNumberList  = regexp.MustCompile(`^` + helpNumber + `(?:[\s,]+` + helpNumber + `)*$`)
	helpLength      = regexp.MustCompile(`^` + helpNumber + `(?:px|%)?$`)
	helpColor       = regexp.MustCompile(`^(?:#[0-9a-fA-F]{3,8}|[a-zA-Z]{1,24}|rgba?\([0-9.,%\s]+\)|hsla?\([0-9.,%\s]+\))$`)
	helpTransform   = regexp.MustCompile(`^(?:matrix|translate|scale|rotate|skewX|skewY)\(\s*` + helpNumber + `(?:[\s,]+` + helpNumber + `)*\s*\)(?:\s+(?:matrix|translate|scale|rotate|skewX|skewY)\(\s*` + helpNumber + `(?:[\s,]+` + helpNumber + `)*\s*\))*$`)
	helpViewBox     = regexp.MustCompile(`^` + helpNumber + `(?:[\s,]+` + helpNumber + `){3}$`)
	helpPathData    = regexp.MustCompile(`^[MmZzLlHhVvCcSsQqTtAa0-9,+.\s-]+$`)
	helpSVGTextTags = map[string]struct{}{"text": {}, "tspan": {}, "title": {}, "desc": {}}
	helpPublicHost  = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	helpURLUnsafe   = regexp.MustCompile(`[\s\\\x00-\x1f\x7f]`)
	helpControlChar = regexp.MustCompile(`[\x00-\x1f\x7f]`)
)

// HelpArtifactStore 一次答疑中累积已校验成果
type HelpArtifactStore struct {
	Artifacts []HelpArtifact
}

func buildHelpArtifactToolInfo() *schema.ToolInfo {
	return buildToolInfo(
		publishHelpArtifactTool,
		"发布用户可查看的成果：Markdown 文档、静态 SVG 图表，或引用已知的 HTTPS/诊断图片。"+
			"SVG 使用基本形状、文本和展示属性。每轮最多四件。",
		map[string]any{
			"kind":  map[string]any{"type": "string", "enum": []string{"markdown", "svg", "image"}, "description": "成果类型"},
			"title": map[string]any{"type": "string", "minLength": 1, "maxLength": 100, "description": "简短标题"},
			"content": map[string]any{
				"type": "string", "minLength": 1, "maxLength": maxHelpSVGChars,
				"description": "Markdown、静态 SVG 或图片 URL",
			},
		},
		[]any{"kind", "title", "content"},
	)
}

// Publish 校验并保存一件成果
func (s *HelpArtifactStore) Publish(arguments map[string]any) (HelpArtifact, error) {
	kind, _ := arguments["kind"].(string)
	title, _ := arguments["title"].(string)
	content, _ := arguments["content"].(string)
	title = strings.TrimSpace(title)
	if kind != "markdown" && kind != "svg" && kind != "image" {
		return HelpArtifact{}, fmt.Errorf("成果格式或长度无效，请按照工具定义提交")
	}
	if title == "" || len([]rune(title)) > 100 || content == "" || len([]rune(content)) > maxHelpSVGChars {
		return HelpArtifact{}, fmt.Errorf("成果格式或长度无效，请按照工具定义提交")
	}
	artifact := HelpArtifact{Kind: kind, Title: title, Content: content}
	switch kind {
	case "svg":
		normalized, err := validateHelpSVG(content)
		if err != nil {
			return HelpArtifact{}, err
		}
		artifact.Content = normalized
	case "image":
		if err := validateHelpImageURL(content); err != nil {
			return HelpArtifact{}, err
		}
	case "markdown":
		if len([]rune(content)) > maxHelpMarkdownChars {
			return HelpArtifact{}, fmt.Errorf("Markdown 成果超过长度限制")
		}
	}
	for _, existing := range s.Artifacts {
		if existing == artifact {
			return artifact, nil
		}
	}
	if len(s.Artifacts) >= maxHelpArtifacts {
		return HelpArtifact{}, fmt.Errorf("单轮最多发布四件成果")
	}
	s.Artifacts = append(s.Artifacts, artifact)
	return artifact, nil
}

func validateHelpSVG(content string) (string, error) {
	if strings.Contains(content, "<!") || strings.Contains(content, "<?") {
		return "", fmt.Errorf("SVG 不允许 XML 声明、文档类型、实体或处理指令")
	}
	decoder := xml.NewDecoder(strings.NewReader(content))
	depth := 0
	count := 0
	rootSeen := false
	var textStack []bool
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("SVG 格式无效")
		}
		switch typed := token.(type) {
		case xml.StartElement:
			depth++
			count++
			if count > maxHelpSVGElements || depth > maxHelpSVGDepth {
				return "", fmt.Errorf("SVG 元素数量或层级超过限制")
			}
			tag := strings.ToLower(typed.Name.Local)
			geometry, ok := helpSVGGeometry[tag]
			if !ok || (tag == "svg" && depth != 1) {
				return "", fmt.Errorf("SVG 包含不支持的元素")
			}
			if tag == "svg" {
				if rootSeen {
					return "", fmt.Errorf("SVG 必须包含一个 svg 根元素")
				}
				rootSeen = true
			}
			textStack = append(textStack, hasHelpSVGTextTag(tag))
			for _, attr := range typed.Attr {
				name := attr.Name.Local
				if strings.HasPrefix(attr.Name.Space, "xmlns") || name == "xmlns" {
					continue
				}
				if _, ok := geometry[name]; !ok {
					if !validHelpSVGPresentation(name) {
						return "", fmt.Errorf("SVG 包含不支持的属性")
					}
				}
				value := strings.TrimSpace(attr.Value)
				if len(value) > 6000 || !validHelpSVGValue(name, value) {
					return "", fmt.Errorf("SVG 属性值无效")
				}
			}
		case xml.EndElement:
			if depth <= 0 || len(textStack) == 0 {
				return "", fmt.Errorf("SVG 格式无效")
			}
			depth--
			textStack = textStack[:len(textStack)-1]
		case xml.CharData:
			if strings.TrimSpace(string(typed)) != "" {
				if len(textStack) == 0 || !textStack[len(textStack)-1] {
					return "", fmt.Errorf("SVG 文本必须位于文本元素内")
				}
			}
		case xml.Comment, xml.ProcInst, xml.Directive:
			return "", fmt.Errorf("SVG 不允许 XML 声明、文档类型、实体或处理指令")
		}
	}
	if !rootSeen || depth != 0 {
		return "", fmt.Errorf("SVG 必须包含一个 svg 根元素")
	}
	return content, nil
}

func hasHelpSVGTextTag(tag string) bool {
	_, ok := helpSVGTextTags[tag]
	return ok
}

func validHelpSVGPresentation(name string) bool {
	switch name {
	case "fill", "stroke", "opacity", "fill-opacity", "stroke-opacity", "stroke-width",
		"stroke-dasharray", "stroke-linecap", "stroke-linejoin", "font-size", "font-weight",
		"font-family", "text-anchor", "dominant-baseline", "transform":
		return true
	default:
		return false
	}
}

func validHelpSVGValue(name, value string) bool {
	switch name {
	case "viewBox":
		return helpViewBox.MatchString(value)
	case "d":
		return len(value) <= 6000 && helpPathData.MatchString(value)
	case "points":
		return helpNumberList.MatchString(value)
	case "fill", "stroke":
		return helpColor.MatchString(value)
	case "opacity", "fill-opacity", "stroke-opacity":
		return regexp.MustCompile(`^` + helpNumber + `$`).MatchString(value)
	case "stroke-width", "font-size":
		return helpLength.MatchString(value)
	case "stroke-dasharray":
		return value == "none" || helpNumberList.MatchString(value)
	case "stroke-linecap":
		return value == "butt" || value == "round" || value == "square"
	case "stroke-linejoin":
		return value == "miter" || value == "round" || value == "bevel"
	case "font-weight":
		return value == "normal" || value == "bold" || regexp.MustCompile(`^[1-9]00$`).MatchString(value)
	case "font-family":
		return value == "sans-serif" || value == "serif" || value == "monospace" || value == "system-ui"
	case "text-anchor":
		return value == "start" || value == "middle" || value == "end"
	case "dominant-baseline":
		return value == "auto" || value == "middle" || value == "central" || value == "hanging" || value == "alphabetic"
	case "transform":
		return helpTransform.MatchString(value)
	default:
		return helpLength.MatchString(value)
	}
}

func validateHelpImageURL(content string) error {
	if len(content) > maxHelpImageURLChars || helpURLUnsafe.MatchString(content) {
		return fmt.Errorf("图片 URL 格式或长度无效")
	}
	parsed, err := url.Parse(content)
	if err != nil {
		return fmt.Errorf("图片必须使用公开 HTTPS URL 或诊断图片路径")
	}
	if strings.HasPrefix(content, reasoningImagePath+"?") && !strings.Contains(content, "#") {
		values, err := url.ParseQuery(parsed.RawQuery)
		if err == nil && len(values) == 1 && len(values["path"]) == 1 {
			path := strings.TrimSpace(values["path"][0])
			if path != "" && !helpControlChar.MatchString(path) {
				return nil
			}
		}
	}
	host := parsed.Hostname()
	if parsed.Scheme == "https" && host != "" && helpPublicHost.MatchString(host) &&
		!strings.HasSuffix(host, ".localhost") && !strings.HasSuffix(host, ".local") &&
		!strings.HasSuffix(host, ".internal") && !strings.HasSuffix(host, ".lan") &&
		!strings.HasSuffix(host, ".home.arpa") && parsed.User == nil &&
		(parsed.Port() == "" || parsed.Port() == "443") {
		return nil
	}
	return fmt.Errorf("图片必须使用公开 HTTPS URL 或诊断图片路径")
}

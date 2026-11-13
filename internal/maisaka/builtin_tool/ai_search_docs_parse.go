package tools

import (
	"net/url"
	"strings"
)

// parseOfficialDocsBundle 解析官方站点提供的 llms-full.txt 文档包
func parseOfficialDocsBundle(bundle string) []OfficialDocument {
	matches := officialDocHeaderPattern.FindAllStringSubmatchIndex(bundle, -1)
	documents := make([]OfficialDocument, 0, len(matches))
	for index, match := range matches {
		path := parseOfficialDocURLPath(bundle[match[2]:match[3]])
		if path == "" {
			continue
		}
		contentEnd := len(bundle)
		if index+1 < len(matches) {
			contentEnd = matches[index+1][0]
		}
		content := strings.TrimSpace(bundle[match[1]:contentEnd])
		title := ""
		if titleMatch := officialDocTitlePattern.FindStringSubmatch(content); len(titleMatch) > 1 {
			title = strings.TrimSpace(titleMatch[1])
		}
		if title == "" {
			title = path
			if slash := strings.LastIndex(path, "/"); slash >= 0 {
				title = path[slash+1:]
			}
		}
		documents = append(documents, newOfficialDocument(path, title, content))
	}
	return documents
}

func newOfficialDocument(path, title, content string) OfficialDocument {
	return OfficialDocument{
		Path:         path,
		Title:        title,
		Content:      content,
		pathLower:    strings.ToLower(path),
		titleLower:   strings.ToLower(title),
		contentLower: strings.ToLower(content),
	}
}

func parseOfficialDocURLPath(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if value[0] == '\'' || value[0] == '"' {
		quote := value[0]
		if len(value) < 2 || value[len(value)-1] != quote {
			return ""
		}
		value = value[1 : len(value)-1]
	} else if strings.ContainsAny(value, "'\"") {
		return ""
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return ""
	}
	if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") && !strings.HasPrefix(value, "/") {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	path := parsed.EscapedPath()
	if path == "" {
		path = parsed.Path
	}
	return path
}

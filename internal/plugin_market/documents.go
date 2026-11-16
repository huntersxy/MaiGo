package pluginmarket

import (
	"context"
	"fmt"
	"golang.org/x/text/cases"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

// DocumentKind 对齐 documents.DocumentKind
type DocumentKind string

const (
	DocumentReadme    DocumentKind = "readme"
	DocumentChangelog DocumentKind = "changelog"
	documentCacheTTL               = 30 * time.Minute
)

// findLocalPlugin 对齐 documents._find_local_plugin
func findLocalPlugin(instanceDir, reference string) (string, map[string]any, bool, error) {
	root, err := ResolvePluginsDir(instanceDir)
	if err != nil {
		return "", nil, false, err
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", nil, false, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", nil, false, err
	}
	folded := cases.Fold().String(reference)
	type match struct {
		folder   string
		manifest map[string]any
	}
	matches := make([]match, 0, 1)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "__") || !entry.IsDir() {
			continue
		}
		folder := filepath.Join(root, name)
		manifestPath := filepath.Join(folder, "_manifest.json")
		if stat, statErr := os.Stat(manifestPath); statErr != nil || stat.IsDir() {
			continue
		}
		raw, readErr := os.ReadFile(manifestPath)
		if readErr != nil {
			continue
		}
		var manifest map[string]any
		if err := sonic.Unmarshal(raw, &manifest); err != nil {
			continue
		}
		if safeInt(manifest["manifest_version"], 1) < 2 {
			continue
		}
		manifestID := cases.Fold().String(safeString(manifest["id"]))
		manifestName := cases.Fold().String(safeString(manifest["name"]))
		if folded == cases.Fold().String(name) || folded == manifestID || folded == manifestName {
			matches = append(matches, match{folder: folder, manifest: manifest})
		}
	}
	if len(matches) > 1 {
		return "", nil, false, fmt.Errorf("多个本地插件匹配该名称，请指定准确 ID")
	}
	if len(matches) == 0 {
		return "", nil, false, nil
	}
	return matches[0].folder, matches[0].manifest, true, nil
}

// readLocalDocument 对齐 documents.read_local_document
func readLocalDocument(folder string, manifest map[string]any, kind DocumentKind) (string, bool, error) {
	declared := ""
	if kind == DocumentChangelog {
		declared = safeString(manifest["changelog"])
	}
	if inlineDocument(declared) {
		return declared, true, nil
	}
	var target string
	if declared != "" && !strings.HasPrefix(declared, "https://") && !strings.HasPrefix(declared, "http://") {
		target = filepath.Join(folder, declared)
	} else {
		filename := "readme.md"
		if kind == DocumentChangelog {
			filename = "changelog.md"
		}
		entries, err := os.ReadDir(folder)
		if err != nil {
			return "", false, err
		}
		for _, entry := range entries {
			if cases.Fold().String(entry.Name()) == filename {
				target = filepath.Join(folder, entry.Name())
				break
			}
		}
	}
	if target == "" {
		return "", false, nil
	}
	info, err := os.Stat(target)
	if err != nil || info.IsDir() {
		return "", false, nil
	}
	resolvedFolder, err := filepath.Abs(folder)
	if err != nil {
		return "", false, err
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", false, err
	}
	if !isPathWithin(resolvedFolder, resolvedTarget) {
		return "", false, fmt.Errorf("插件文档路径超出插件目录: %s", declared)
	}
	raw, err := os.ReadFile(resolvedTarget)
	if err != nil {
		return "", false, err
	}
	return string(raw), true, nil
}

// inlineDocument 对齐 "内容内联还是路径" 的判断
func inlineDocument(declared string) bool {
	if declared == "" {
		return false
	}
	return strings.Contains(declared, "\n") || strings.HasPrefix(declared, "#") || strings.HasPrefix(declared, "- ")
}

// remoteDocument 对齐 documents._remote_document
func (s *Service) remoteDocument(ctx context.Context, plugin PluginCatalogItem, kind DocumentKind, source, branch string, forceRefresh bool) (map[string]any, error) {
	declared := ""
	if kind == DocumentChangelog {
		declared = safeString(plugin.Changelog)
		if declared == "" {
			declared = safeString(plugin.Manifest["changelog"])
		}
	}
	if inlineDocument(declared) {
		return map[string]any{"content": declared, "source": "manifest", "source_type": "inline"}, nil
	}
	if kind == DocumentChangelog && declared == "" {
		return map[string]any{"content": "", "source": "manifest", "source_type": "missing"}, nil
	}
	var targetURL string
	if strings.HasPrefix(declared, "https://") || strings.HasPrefix(declared, "http://") {
		targetURL = declared
	} else {
		parsed, err := url.Parse(plugin.RepositoryURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || !isGitHubRepositoryPath(parsed.Path) {
			return nil, fmt.Errorf("插件仓库必须是 https://github.com/owner/repo 地址")
		}
		segments := strings.Split(strings.Trim(strings.TrimSuffix(parsed.Path, ".git"), "/"), "/")
		if len(segments) != 2 {
			return nil, fmt.Errorf("插件仓库必须是 https://github.com/owner/repo 地址")
		}
		owner, repo := segments[0], segments[1]
		var gitSource GitSource
		if kind == DocumentReadme {
			// README 跟随下载源，并保留所选发布版本的提交
			gitSource, err = s.downloadSource()
		} else {
			selected := source
			if selected == "official" {
				selected = "github"
			}
			gitSource, err = s.selectSource(selected, "")
		}
		if err != nil {
			return nil, err
		}
		filePath := declared
		if kind == DocumentReadme {
			filePath = "README.md"
		}
		targetURL, err = gitSource.RawURL(owner, repo, branch, filePath)
		if err != nil {
			return nil, err
		}
	}
	loader := func() (any, error) {
		gitSource := GitSource{}
		if declared != "" && (strings.HasPrefix(declared, "https://") || strings.HasPrefix(declared, "http://")) {
			if _, verifyErr := validatePublicURL(targetURL, s.guardForRequest()); verifyErr != nil {
				return nil, verifyErr
			}
		}
		return gitSource.ReadText(ctx, targetURL, requestTimeout)
	}
	content, cache, err := s.cache.Get(targetURL, loader, documentCacheTTL, forceRefresh)
	if err != nil {
		if statusErr, ok := AsHTTPStatusError(err); ok && statusErr.Code == 404 {
			return map[string]any{"content": "", "source": targetURL, "source_type": "missing"}, nil
		}
		return nil, err
	}
	text, _ := content.(string)
	return map[string]any{"content": text, "source": targetURL, "source_type": "remote", "cache": cache}, nil
}

func isGitHubRepositoryPath(path string) bool {
	trimmed := strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	segments := strings.Split(trimmed, "/")
	if len(segments) != 2 {
		return false
	}
	for _, segment := range segments {
		if segment == "" {
			return false
		}
		for _, r := range segment {
			valid := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.'
			if !valid {
				return false
			}
		}
	}
	return true
}

// GetReadme 对齐 service.get_readme
func (s *Service) GetReadme(ctx context.Context, pluginID string, maxChars int, preferLocal bool, branch string, forceRefresh bool) (DocumentResponse, error) {
	return s.getDocument(ctx, pluginID, DocumentReadme, maxChars, preferLocal, branch, forceRefresh)
}

// GetChangelog 对齐 service.get_changelog
func (s *Service) GetChangelog(ctx context.Context, pluginID string, maxChars int, preferLocal bool, branch string, forceRefresh bool) (DocumentResponse, error) {
	return s.getDocument(ctx, pluginID, DocumentChangelog, maxChars, preferLocal, branch, forceRefresh)
}

// getDocument 对齐 documents.get_document
func (s *Service) getDocument(ctx context.Context, pluginID string, kind DocumentKind, maxChars int, preferLocal bool, branch string, forceRefresh bool) (DocumentResponse, error) {
	if strings.TrimSpace(pluginID) == "" || strings.TrimSpace(branch) == "" || maxChars < 500 || maxChars > 200000 {
		return DocumentResponse{}, fmt.Errorf("plugin_id 和 branch 不能为空，max_chars 必须在 500 到 200000 之间")
	}
	var (
		plugin PluginCatalogItem
		source string
	)
	if preferLocal {
		folder, manifest, found, err := findLocalPlugin(s.instanceDirectory(), strings.TrimSpace(pluginID))
		if err != nil {
			return DocumentResponse{}, err
		}
		if found {
			plugin = PluginCatalogItem{
				ID:            safeString(manifest["id"]),
				Name:          safeString(manifest["name"]),
				Manifest:      manifest,
				RepositoryURL: manifestRepositoryURL(manifest),
			}
			content, ok, err := readLocalDocument(folder, manifest, kind)
			if err != nil {
				return DocumentResponse{}, err
			}
			if ok {
				return buildDocumentResponse(plugin, content, folder, "local", nil, maxChars), nil
			}
			source, err = s.informationSource()
			if err != nil {
				return DocumentResponse{}, err
			}
		}
	}
	if plugin.ID == "" {
		resolved, resolvedSource, err := s.documentPlugin(ctx, pluginID, forceRefresh)
		if err != nil {
			return DocumentResponse{}, err
		}
		plugin = resolved
		source = resolvedSource
	}
	result, err := s.remoteDocument(ctx, plugin, kind, source, branch, forceRefresh)
	if err != nil {
		return DocumentResponse{}, err
	}
	content := safeString(contentOf(result))
	var cache *CacheInfo
	if info, ok := result["cache"].(CacheInfo); ok {
		cache = &info
	}
	response := buildDocumentResponse(plugin, content, safeString(result["source"]), safeString(result["source_type"]), cache, maxChars)
	return response, nil
}

func contentOf(result map[string]any) any {
	if value, ok := result["content"]; ok {
		return value
	}
	return ""
}

// documentPlugin 对齐 _document_plugin：解析插件并返回信息源
func (s *Service) documentPlugin(ctx context.Context, pluginID string, forceRefresh bool) (PluginCatalogItem, string, error) {
	snapshot, err := s.loadSnapshot(ctx, forceRefresh)
	if err != nil {
		return PluginCatalogItem{}, "", err
	}
	plugin, err := s.resolvePlugin(pluginID, snapshot.Plugins)
	if err != nil {
		return PluginCatalogItem{}, "", err
	}
	return plugin, snapshot.Source(), nil
}

// buildDocumentResponse 对齐 get_document 的截断与字符统计
func buildDocumentResponse(plugin PluginCatalogItem, content, source, sourceType string, cache *CacheInfo, maxChars int) DocumentResponse {
	runes := []rune(content)
	truncated := len(runes) > maxChars
	if truncated {
		runes = runes[:maxChars]
	}
	return DocumentResponse{
		Success:    true,
		Plugin:     map[string]string{"id": plugin.ID, "name": plugin.Name},
		Content:    string(runes),
		Chars:      len([]rune(content)),
		Truncated:  truncated,
		Source:     source,
		SourceType: sourceType,
		Cache:      cache,
	}
}

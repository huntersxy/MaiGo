package host

// 已发布单实例配置迁入最终工作区结构：统一校验、备份和事务提交。
// 对齐 src/host/workspace_migration.py、config_migration.py、retention_migration.py 与 model_migration.py。

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	maibotcfg "MaiGo/internal/config/maibot"
)

var maimNetworkFields = []string{
	"ws_server_host", "ws_server_port", "api_server_host", "api_server_port",
	"api_server_use_wss", "api_server_cert_file", "api_server_key_file",
}

var webuiHostFields = []string{
	"enabled", "host", "port", "secure_cookie", "anti_crawler_mode", "allowed_ips",
	"trusted_proxies", "trust_xff", "mode", "webui_style", "enforce_public_outbound_url",
}

// MigrateWorkspaceConfig 把所有 Bot 的共享配置并入宿主文件
func MigrateWorkspaceConfig(workspace Workspace) error {
	hostDir := workspace.HostDirectory()
	modelLock, lockErr := LockFilePath(filepath.Join(hostDir, "model-config.lock"))
	if lockErr != nil {
		return ErrInstanceBusy
	}
	defer modelLock.Release()
	RecoverConfigTransaction(hostDir)
	hostPath := filepath.Join(hostDir, "config.toml")
	catalogPath := filepath.Join(hostDir, maibotcfg.CatalogFilename)
	hostDoc, err := readTOMLMap(hostPath)
	if err != nil {
		return err
	}
	catalog, err := readCatalogDocument(catalogPath)
	if err != nil {
		return err
	}
	bots, err := workspace.Registry.ListBots()
	if err != nil {
		return err
	}
	documents := map[string]map[string]any{}
	originals := map[string]string{}
	originalDocuments := map[string]map[string]any{}
	addDocument := func(path string) error {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				return nil
			}
			return readErr
		}
		var doc map[string]any
		if err := toml.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("当前配置无法解析：%s", path)
		}
		documents[path] = doc
		originals[path] = string(raw)
		originalDocuments[path] = deepCopyMap(doc)
		return nil
	}
	for _, bot := range bots {
		if bot.Initialization != "initialized" {
			continue
		}
		for _, name := range []string{"bot_config.toml", "model_config.toml"} {
			path := filepath.Join(bot.Directory, "config", name)
			if err := addDocument(path); err != nil {
				return err
			}
			if _, exists := documents[path]; !exists {
				return fmt.Errorf("实例配置缺失: %s", path)
			}
		}
		versionRoot := filepath.Join(bot.Directory, "config", "versions", "model")
		entries, _ := os.ReadDir(versionRoot)
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
				continue
			}
			_ = addDocument(filepath.Join(versionRoot, entry.Name()))
		}
	}
	originalDocuments[hostPath] = deepCopyMap(hostDoc)
	originalDocuments[catalogPath] = deepCopyMap(catalog)
	botConfigs := map[string]map[string]any{}
	for path, doc := range documents {
		if filepath.Base(path) == "bot_config.toml" {
			botConfigs[path] = doc
		}
	}
	if err := planHostSettings(hostPath, hostDoc, botConfigs); err != nil {
		return err
	}
	if err := planRetention(hostDoc, botConfigs); err != nil {
		return err
	}
	if err := planModelCatalog(catalog, bots, documents); err != nil {
		return err
	}
	files := map[string]string{}
	for path, doc := range documents {
		_, existed := originals[path]
		encoded, err := encodeTOMLDocument(doc)
		if err != nil {
			return err
		}
		if !existed || !documentsEqual(doc, originalDocuments[path]) {
			files[path] = encoded
		}
	}
	if !documentsEqual(hostDoc, originalDocuments[hostPath]) {
		encoded, err := encodeTOMLDocument(hostDoc)
		if err != nil {
			return err
		}
		files[hostPath] = encoded
	}
	if !documentsEqual(catalog, originalDocuments[catalogPath]) {
		encoded, err := encodeTOMLDocument(catalog)
		if err != nil {
			return err
		}
		files[catalogPath] = encoded
	}
	if len(files) == 0 {
		return nil
	}
	if err := backupMigration(hostDir, files, originals); err != nil {
		return err
	}
	return CommitConfigFiles(hostDir, files)
}

func backupMigration(hostDir string, files, originals map[string]string) error {
	if len(files) == 0 {
		return nil
	}
	backupDir := filepath.Join(hostDir, "backups", fmt.Sprintf("workspace-migration-%d-%d", os.Getpid(), time.Now().UnixNano()))
	if err := os.MkdirAll(backupDir, 0o777); err != nil {
		return err
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	manifest := make([]map[string]any, 0, len(paths))
	for index, path := range paths {
		var backup any
		if original, existed := originals[path]; existed {
			name := fmt.Sprintf("%d.toml", index)
			if err := WritePrivate(filepath.Join(backupDir, name), original); err != nil {
				return err
			}
			backup = name
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			absolute = path
		}
		manifest = append(manifest, map[string]any{"path": absolute, "backup": backup})
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return WritePrivate(filepath.Join(backupDir, "manifest.json"), string(encoded))
}

func planHostSettings(hostPath string, hostDoc map[string]any, botConfigs map[string]map[string]any) error {
	webui, _ := hostDoc["webui"].(map[string]any)
	if webui == nil {
		webui = map[string]any{}
		hostDoc["webui"] = webui
	}
	defaults := maibotcfg.SharedWebUIDefaults()
	oldSections := make([]map[string]any, 0, len(botConfigs))
	for _, doc := range botConfigs {
		section, _ := doc["webui"].(map[string]any)
		if section == nil {
			section = map[string]any{}
		}
		oldSections = append(oldSections, section)
		merged := map[string]any{}
		for key, value := range defaults {
			merged[key] = value
			if override, exists := section[key]; exists {
				merged[key] = override
			}
		}
		if _, err := maibotcfg.ValidateSharedWebUI(merged); err != nil {
			return fmt.Errorf("%s: %w", hostPath, err)
		}
	}
	for key, defaultValue := range defaults {
		if _, exists := webui[key]; exists {
			continue
		}
		values := make([]any, 0)
		unique := map[any]struct{}{}
		for _, section := range oldSections {
			if value, exists := section[key]; exists {
				values = append(values, value)
				unique[value] = struct{}{}
			}
		}
		if len(values) == 0 {
			webui[key] = defaultValue
			continue
		}
		if key == "webui_style" && len(unique) > 1 {
			return errors.New("旧 Bot 的界面风格不一致，请统一风格后迁移公共设置")
		}
		if key == "enforce_public_outbound_url" {
			enabled := false
			for _, value := range values {
				if typed, ok := value.(bool); ok && typed {
					enabled = true
				}
			}
			webui[key] = enabled
		} else {
			webui[key] = values[0]
		}
	}
	if _, err := maibotcfg.ValidateSharedWebUI(webui); err != nil {
		return err
	}
	candidates := map[string]map[string][]string{"public_base_url": {}, "ssl_certfile": {}, "ssl_keyfile": {}}
	sourcesChanged := false
	for path, doc := range botConfigs {
		directory := filepath.Dir(filepath.Dir(path))
		files, _ := doc["files"].(map[string]any)
		message, _ := doc["maim_message"].(map[string]any)
		values := map[string]string{}
		if base, ok := files["transfer_base_url"].(string); ok {
			values["public_base_url"] = base
		}
		if useWSS, _ := message["api_server_use_wss"].(bool); useWSS {
			for target, source := range map[string]string{"ssl_certfile": "api_server_cert_file", "ssl_keyfile": "api_server_key_file"} {
				value, _ := message[source].(string)
				if strings.TrimSpace(value) == "" {
					return fmt.Errorf("%s: 已启用 WSS，但 %s 未填写；旧配置未清理", path, source)
				}
				certificate := filepath.FromSlash(value)
				if !filepath.IsAbs(certificate) {
					certificate = filepath.Join(directory, certificate)
				}
				values[target] = filepath.Clean(certificate)
			}
		}
		for key, value := range values {
			value = strings.TrimSpace(value)
			if key == "public_base_url" {
				value = strings.TrimRight(value, "/")
			}
			if value != "" {
				candidates[key][value] = append(candidates[key][value], path)
			}
		}
		if stripHostSettings(doc) {
			sourcesChanged = true
		}
	}
	for key, options := range candidates {
		current, _ := webui[key].(string)
		if strings.TrimSpace(current) != "" {
			options[current] = append(options[current], hostPath)
		}
		if len(options) > 1 {
			names := make([]string, 0, len(options))
			for value := range options {
				names = append(names, value)
			}
			sort.Strings(names)
			details := make([]string, 0, len(names))
			for _, value := range names {
				details = append(details, value+": "+strings.Join(options[value], ", "))
			}
			return fmt.Errorf("宿主配置迁移冲突 %s（%s）；请统一配置后重试，旧字段未清理", key, strings.Join(details, "; "))
		}
		for value := range options {
			webui[key] = value
		}
	}
	hostDoc["webui"] = webui
	config, err := validateHostConfig(hostDoc, hostPath)
	if err != nil {
		return err
	}
	if config.SSLCertFile != "" && sourcesChanged {
		if _, err := tls.LoadX509KeyPair(config.SSLCertFile, config.SSLKeyFile); err != nil {
			return fmt.Errorf("SSL 证书或私钥无效: %w", err)
		}
	}
	return nil
}

func stripHostSettings(document map[string]any) bool {
	changed := false
	if _, exists := document["http"]; exists {
		delete(document, "http")
		changed = true
	}
	if files, ok := document["files"].(map[string]any); ok {
		if _, exists := files["transfer_base_url"]; exists {
			delete(files, "transfer_base_url")
			changed = true
		}
	}
	if maim, ok := document["maim_message"].(map[string]any); ok {
		for _, key := range maimNetworkFields {
			if _, exists := maim[key]; exists {
				delete(maim, key)
				changed = true
			}
		}
	}
	if webui, ok := document["webui"].(map[string]any); ok {
		for _, key := range webuiHostFields {
			if _, exists := webui[key]; exists {
				delete(webui, key)
				changed = true
			}
		}
		if len(webui) == 0 {
			delete(document, "webui")
			changed = true
		}
	}
	return changed
}

func planRetention(hostDoc map[string]any, botConfigs map[string]map[string]any) error {
	policy, _ := hostDoc["retention"].(map[string]any)
	if policy != nil {
		return nil
	}
	policy = maibotcfg.SharedRetentionDefaults()
	for _, doc := range botConfigs {
		visual, _ := doc["visual"].(map[string]any)
		emoji, _ := doc["emoji"].(map[string]any)
		logSection, _ := doc["log"].(map[string]any)
		images, _ := visual["image_cache_cleanup"].(map[string]any)
		emojis, _ := emoji["cache_cleanup"].(map[string]any)
		mergeRetentionSection(policy, "images", images)
		mergeRetentionSection(policy, "emoji", emojis)
		if logs, ok := policy["logs"].(map[string]any); ok {
			for key := range logs {
				if value, exists := logSection[key]; exists {
					logs[key] = value
				}
			}
		}
		delete(visual, "image_cache_cleanup")
		delete(emoji, "cache_cleanup")
		if logs, ok := policy["logs"].(map[string]any); ok {
			for key := range logs {
				delete(logSection, key)
			}
		}
	}
	hostDoc["retention"] = policy
	return nil
}

func mergeRetentionSection(policy map[string]any, section string, values map[string]any) {
	target, _ := policy[section].(map[string]any)
	if target == nil {
		return
	}
	for key := range target {
		value, exists := values[key]
		if !exists {
			continue
		}
		if key == "enabled" {
			if current, ok := target[key].(bool); !ok || !current {
				if typed, ok := value.(bool); ok {
					target[key] = current && typed
				}
			}
			continue
		}
		if current, ok := numberValue(target[key]); ok {
			if candidate, ok := numberValue(value); ok && candidate > current {
				target[key] = value
			}
		}
	}
}

func numberValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	default:
		return 0, false
	}
}

func planModelCatalog(catalog map[string]any, bots []BotRecord, documents map[string]map[string]any) error {
	if catalog == nil {
		return errors.New("公共模型目录未初始化")
	}
	if inner, ok := catalog["inner"].(map[string]any); ok {
		if version, exists := inner["version"]; exists && version != nil && hostString(version) != maibotcfg.CatalogVersion {
			return errors.New("不支持的公共模型配置版本")
		}
	}
	for _, section := range maibotcfg.CatalogSections {
		if _, ok := catalog[section].([]any); !ok {
			catalog[section] = []any{}
		}
	}
	web, _ := catalog["web"].(map[string]any)
	if web == nil {
		web = map[string]any{}
		catalog["web"] = web
	}
	if _, ok := web["services"].(map[string]any); !ok {
		web["services"] = map[string]any{}
	}
	for _, bot := range bots {
		if bot.Initialization != "initialized" {
			continue
		}
		localPath := filepath.Join(bot.Directory, "config", "model_config.toml")
		if local, ok := documents[localPath]; ok && hasCatalogDefinitions(local) {
			if err := moveDefinitionsToCatalog(catalog, local, bot, bot.BotID[:8]); err != nil {
				return err
			}
		}
		versionRoot := filepath.Join(bot.Directory, "config", "versions", "model")
		entries, _ := os.ReadDir(versionRoot)
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
				continue
			}
			path := filepath.Join(versionRoot, entry.Name())
			snapshot, ok := documents[path]
			if !ok || !hasCatalogDefinitions(snapshot) {
				continue
			}
			candidate := deepCopyMap(catalog)
			working := deepCopyMap(snapshot)
			sum := sha256.Sum256([]byte(entry.Name()))
			namespace := bot.BotID[:8] + "-" + hex.EncodeToString(sum[:])[:8]
			if err := moveDefinitionsToCatalog(candidate, working, bot, namespace); err != nil {
				continue
			}
			if err := maibotcfg.ValidateCatalogDoc(candidate); err != nil {
				continue
			}
			for _, section := range append(append([]string(nil), maibotcfg.CatalogSections...), "web") {
				catalog[section] = candidate[section]
			}
			documents[path] = working
		}
	}
	catalog["inner"] = map[string]any{"version": maibotcfg.CatalogVersion}
	if err := maibotcfg.ValidateCatalogDoc(catalog); err != nil {
		return err
	}
	for _, section := range maibotcfg.CatalogSections {
		if items, ok := catalog[section].([]any); !ok || len(items) == 0 {
			catalog[section] = []any{}
		}
	}
	return nil
}

func moveDefinitionsToCatalog(catalog, local map[string]any, bot BotRecord, namespace string) error {
	if !hasCatalogDefinitions(local) {
		return nil
	}
	if models, ok := local["models"].([]any); ok && len(models) > 0 {
		upgraded, version, err := maibotcfg.UpgradeModelDefinitions(local)
		if err != nil {
			return err
		}
		local["models"] = upgraded
		if version != "" {
			inner, _ := local["inner"].(map[string]any)
			if inner == nil {
				inner = map[string]any{}
				local["inner"] = inner
			}
			inner["version"] = version
		}
	}
	providerRenames := map[string]string{}
	modelRenames := map[string]string{}
	for _, section := range []string{"api_providers", "models"} {
		items, _ := local[section].([]any)
		known := sectionEntries(catalog, section)
		renames := modelRenames
		if section == "api_providers" {
			renames = providerRenames
		}
		for _, raw := range items {
			entry, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			name := strings.TrimSpace(hostString(entry["name"]))
			if name == "" {
				continue
			}
			if section == "models" {
				if provider := strings.TrimSpace(hostString(entry["api_provider"])); provider != "" {
					if renamed, ok := providerRenames[provider]; ok {
						entry["api_provider"] = renamed
					}
				}
			}
			resolved, err := resolveCatalogName(known, entry, name, namespace)
			if err != nil {
				return fmt.Errorf("%s 的模型迁移名称冲突: %s", bot.Name, name)
			}
			entry["name"] = resolved
			renames[name] = resolved
			if _, exists := known[resolved]; !exists {
				appendCatalog(catalog, section, entry)
				known[resolved] = entry
			}
		}
		delete(local, section)
	}
	web, _ := local["web"].(map[string]any)
	if services, ok := web["services"].(map[string]any); ok {
		catalogWeb, _ := catalog["web"].(map[string]any)
		if catalogWeb == nil {
			catalogWeb = map[string]any{}
			catalog["web"] = catalogWeb
		}
		catalogServices, _ := catalogWeb["services"].(map[string]any)
		if catalogServices == nil {
			catalogServices = map[string]any{}
			catalogWeb["services"] = catalogServices
		}
		webRenames := map[string]string{}
		for originalName, service := range services {
			entry, ok := service.(map[string]any)
			if !ok {
				entry = map[string]any{}
			}
			name, err := resolveServiceName(catalogServices, entry, originalName, namespace)
			if err != nil {
				return fmt.Errorf("%s 的联网服务迁移名称冲突: %s", bot.Name, originalName)
			}
			catalogServices[name] = entry
			webRenames[originalName] = name
		}
		delete(web, "services")
		if len(web) == 0 {
			delete(local, "web")
		}
		renameTaskReferences(local, webRenames, true)
	}
	renameTaskReferences(local, modelRenames, false)
	return nil
}

func resolveCatalogName(known map[string]map[string]any, entry map[string]any, name, namespace string) (string, error) {
	existing, exists := known[name]
	if !exists {
		return name, nil
	}
	if catalogEntriesEqual(existing, entry) {
		return name, nil
	}
	for key, value := range known {
		if catalogEntriesEqual(value, entry) {
			return key, nil
		}
	}
	candidate := name + " (" + namespace + ")"
	if existing, exists := known[candidate]; exists {
		if catalogEntriesEqual(existing, entry) {
			return candidate, nil
		}
		return "", fmt.Errorf("名称冲突: %s", candidate)
	}
	return candidate, nil
}

func catalogEntriesEqual(left, right map[string]any) bool {
	leftCopy := deepCopyMap(left)
	rightCopy := deepCopyMap(right)
	delete(leftCopy, "name")
	delete(rightCopy, "name")
	leftEncoded, leftErr := encodeTOMLDocument(leftCopy)
	rightEncoded, rightErr := encodeTOMLDocument(rightCopy)
	return leftErr == nil && rightErr == nil && leftEncoded == rightEncoded
}

func renameTaskReferences(document map[string]any, renames map[string]string, webSearch bool) {
	tasks, _ := document["model_task_config"].(map[string]any)
	for taskName, raw := range tasks {
		if (taskName == "web_search") != webSearch {
			continue
		}
		task, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		renameModelList(task, renames)
	}
}

func renameModelList(node map[string]any, renames map[string]string) {
	for key, value := range node {
		if key == "model_list" {
			if list, ok := value.([]any); ok {
				updated := make([]any, 0, len(list))
				for _, item := range list {
					name := hostString(item)
					if renamed, exists := renames[name]; exists {
						updated = append(updated, renamed)
					} else {
						updated = append(updated, item)
					}
				}
				node[key] = updated
			}
			continue
		}
		if child, ok := value.(map[string]any); ok {
			renameModelList(child, renames)
		}
	}
}

func sectionEntries(document map[string]any, section string) map[string]map[string]any {
	entries := map[string]map[string]any{}
	items, _ := document[section].([]any)
	for _, raw := range items {
		if item, ok := raw.(map[string]any); ok {
			if name := strings.TrimSpace(hostString(item["name"])); name != "" {
				entries[name] = item
			}
		}
	}
	return entries
}

func deepCopyMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	copied := make(map[string]any, len(source))
	for key, value := range source {
		copied[key] = deepCopyValue(value)
	}
	return copied
}

func deepCopyValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return deepCopyMap(typed)
	case []any:
		copied := make([]any, len(typed))
		for index, item := range typed {
			copied[index] = deepCopyValue(item)
		}
		return copied
	default:
		return value
	}
}

func appendCatalog(catalog map[string]any, section string, entry map[string]any) {
	items, _ := catalog[section].([]any)
	catalog[section] = append(items, entry)
}

func hasCatalogDefinitions(document map[string]any) bool {
	for _, section := range maibotcfg.CatalogSections {
		if _, exists := document[section]; exists {
			return true
		}
	}
	if web, ok := document["web"].(map[string]any); ok {
		if _, exists := web["services"]; exists {
			return true
		}
	}
	return false
}

func resolveServiceName(known map[string]any, entry map[string]any, name, namespace string) (string, error) {
	existing, exists := known[name]
	if !exists {
		return name, nil
	}
	if existingEntry, ok := existing.(map[string]any); ok && catalogEntriesEqual(existingEntry, entry) {
		return name, nil
	}
	for key, value := range known {
		if existingEntry, ok := value.(map[string]any); ok && catalogEntriesEqual(existingEntry, entry) {
			return key, nil
		}
	}
	candidate := name + " (" + namespace + ")"
	if existing, exists := known[candidate]; exists {
		if existingEntry, ok := existing.(map[string]any); ok && catalogEntriesEqual(existingEntry, entry) {
			return candidate, nil
		}
		return "", fmt.Errorf("名称冲突: %s", candidate)
	}
	return candidate, nil
}

package host

// 工作区公共文件初始化：统一 WebUI/保留策略与共享模型目录。
// 对齐 src/host/initialize.py。

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"

	maibotcfg "MaiGo/internal/config/maibot"
)

// InitializeHostFiles 生成缺失的宿主公共配置与共享模型目录
func InitializeHostFiles(workspace Workspace) error {
	hostDir := workspace.HostDirectory()
	if err := os.MkdirAll(hostDir, 0o777); err != nil {
		return err
	}
	configPath := filepath.Join(hostDir, "config.toml")
	if raw, err := os.ReadFile(configPath); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		document := map[string]any{
			"webui":     sharedWebUIHostDefaults(workspace.Config),
			"retention": maibotcfg.SharedRetentionDefaults(),
		}
		encoded, err := toml.Marshal(document)
		if err != nil {
			return err
		}
		if err := WritePrivate(configPath, string(encoded)); err != nil {
			return err
		}
	} else {
		var document map[string]any
		if err := toml.Unmarshal(raw, &document); err != nil {
			return err
		}
		{
			changed := false
			if _, exists := document["retention"]; !exists {
				document["retention"] = maibotcfg.SharedRetentionDefaults()
				changed = true
			}
			if webui, ok := document["webui"].(map[string]any); ok {
				for key, value := range maibotcfg.SharedWebUIDefaults() {
					if _, exists := webui[key]; !exists {
						webui[key] = value
						changed = true
					}
				}
			}
			if changed {
				encoded, _ := toml.Marshal(document)
				if err := WritePrivate(configPath, string(encoded)); err != nil {
					return err
				}
			}
		}
	}
	catalogPath := filepath.Join(hostDir, maibotcfg.CatalogFilename)
	if _, err := os.Stat(catalogPath); os.IsNotExist(err) {
		if err := writeDefaultCatalog(catalogPath); err != nil {
			return err
		}
	}
	if err := os.Setenv(maibotcfg.SharedModelsEnv, catalogPath); err != nil {
		return err
	}
	return nil
}

func sharedWebUIHostDefaults(config HostConfig) map[string]any {
	return map[string]any{
		"enabled":                     config.WebUIEnabled,
		"hosts":                       config.Hosts,
		"port":                        config.Port,
		"secure_cookie":               config.SecureCookie,
		"anti_crawler_mode":           config.AntiCrawlerMode,
		"allowed_ips":                 config.AllowedIPs,
		"trusted_proxies":             config.TrustedProxies,
		"trust_xff":                   config.TrustXFF,
		"public_base_url":             config.PublicBaseURL,
		"ssl_certfile":                config.SSLCertFile,
		"ssl_keyfile":                 config.SSLKeyFile,
		"webui_style":                 2,
		"enforce_public_outbound_url": true,
	}
}

func writeDefaultCatalog(path string) error {
	var document map[string]any
	if err := toml.Unmarshal(maibotcfg.DefaultModelConfigTOML(), &document); err != nil {
		return fmt.Errorf("读取默认模型配置失败: %w", err)
	}
	catalog := maibotcfg.ExtractCatalog(document)
	encoded, err := toml.Marshal(catalog)
	if err != nil {
		return err
	}
	return WritePrivate(path, string(encoded))
}

// stripBotSharedDefinitions 新建 Bot 的本地配置只保留任务等独立设置
func stripBotSharedDefinitions(directory string) error {
	modelPath := filepath.Join(directory, "config", "model_config.toml")
	raw, err := os.ReadFile(modelPath)
	if err != nil {
		return err
	}
	var document map[string]any
	if err := toml.Unmarshal(raw, &document); err != nil {
		return err
	}
	stripped := maibotcfg.ExtractBotModelSettings(document)
	if catalogPath := maibotcfg.SharedCatalogPath(); catalogPath != "" {
		if catalog, err := readCatalogDocument(catalogPath); err == nil {
			filterTaskModelSelections(stripped, catalog)
		}
	}
	encoded, err := toml.Marshal(stripped)
	if err != nil {
		return err
	}
	return WritePrivate(modelPath, string(encoded))
}

// filterTaskModelSelections 去掉默认任务中指向当前公共目录已不存在模型的选择
func filterTaskModelSelections(document, catalog map[string]any) {
	models := sectionNames(catalog, "models")
	services := map[string]struct{}{}
	if web, ok := catalog["web"].(map[string]any); ok {
		if entries, ok := web["services"].(map[string]any); ok {
			for name := range entries {
				services[name] = struct{}{}
			}
		}
	}
	tasks, _ := document["model_task_config"].(map[string]any)
	for taskName, raw := range tasks {
		task, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		allowed := models
		if taskName == "web_search" {
			allowed = services
		}
		filterModelListValues(task, allowed)
	}
}

func filterModelListValues(node map[string]any, allowed map[string]struct{}) {
	for key, value := range node {
		if key == "model_list" {
			list, ok := value.([]any)
			if !ok {
				continue
			}
			kept := make([]any, 0, len(list))
			for _, item := range list {
				if _, exists := allowed[hostString(item)]; exists {
					kept = append(kept, item)
				}
			}
			node[key] = kept
			continue
		}
		if child, ok := value.(map[string]any); ok {
			filterModelListValues(child, allowed)
		}
	}
}

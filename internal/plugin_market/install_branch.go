package pluginmarket

import (
	"context"
	"fmt"
	"strings"
)

// InstallFromBranch 对齐 management.install_plugin 的分支克隆安装路径
func (s *Service) InstallFromBranch(ctx context.Context, pluginID, repositoryURL, branch, mirrorID string) (map[string]any, error) {
	pluginsRoot, err := ResolvePluginsDir(s.instanceDirectory())
	if err != nil {
		return nil, err
	}
	s.reportStep("install", pluginID, 5, "开始安装插件: "+pluginID)
	repoURL, owner, repo, err := parseRepositoryURL(repositoryURL)
	if err != nil {
		return nil, err
	}
	s.reportStep("install", pluginID, 10, fmt.Sprintf("解析仓库信息: %s/%s", owner, repo))

	target, oldFormat, err := CandidatePaths(pluginsRoot, pluginID)
	if err != nil {
		return nil, err
	}
	for _, candidate := range []string{target, oldFormat} {
		if IsPluginInstallResidue(candidate) {
			if removeErr := RemoveTree(candidate); removeErr != nil {
				return nil, removeErr
			}
		}
	}
	if pathExists(target) || pathExists(oldFormat) {
		s.reportProgressError("install", pluginID, "插件已存在", "插件已安装，请先卸载")
		return nil, badRequestError("插件已安装")
	}
	s.reportStep("install", pluginID, 15, "准备克隆到: "+target)

	var outcome cloneOutcome
	if strings.Contains(repoURL, "github.com") {
		outcome = s.cloneRepository(ctx, owner, repo, target, branch, "", 1, "install", pluginID)
	} else {
		outcome = s.cloneRepository(ctx, owner, repo, target, branch, repoURL, 1, "install", pluginID)
	}
	if !outcome.Success {
		status := outcome.StatusCode
		if status == 0 {
			status = 500
		}
		s.reportProgressError("install", pluginID, "克隆仓库失败", outcome.Error)
		return nil, &MarketError{StatusCode: status, Message: outcome.Error}
	}

	s.reportStep("install", pluginID, 85, "验证插件文件...")
	manifestPath, err := resolvePluginFilePath(target, "_manifest.json", true)
	if err != nil {
		return nil, err
	}
	if !pathExists(manifestPath) {
		_ = RemoveTree(target)
		s.reportProgressError("install", pluginID, "插件缺少 _manifest.json", "无效的插件格式")
		return nil, badRequestError("无效的插件：缺少 _manifest.json")
	}
	s.reportStep("install", pluginID, 90, "读取插件配置...")
	manifest, err := LoadManifestJSON(manifestPath)
	if err != nil || manifest == nil {
		_ = RemoveTree(target)
		detail := "manifest 无法读取"
		if err != nil {
			detail = err.Error()
		}
		s.reportProgressError("install", pluginID, "_manifest.json 无效", detail)
		return nil, badRequestError("无效的 _manifest.json: %s", detail)
	}
	for _, field := range []string{"manifest_version", "id", "name", "version", "author"} {
		if _, ok := manifest[field]; !ok {
			_ = RemoveTree(target)
			s.reportProgressError("install", pluginID, "_manifest.json 无效", "缺少必需字段: "+field)
			return nil, badRequestError("无效的 _manifest.json: 缺少必需字段: %s", field)
		}
	}
	manifestPluginID := strings.TrimSpace(safeString(manifest["id"]))
	if manifestPluginID != pluginID {
		_ = RemoveTree(target)
		detail := fmt.Sprintf("插件 ID 不匹配：期望 %s，实际 %s", pluginID, manifestPluginID)
		s.reportProgressError("install", pluginID, "_manifest.json 无效", detail)
		return nil, badRequestError("无效的 _manifest.json: %s", detail)
	}
	pluginName := safeString(manifest["name"])
	version := safeString(manifest["version"])
	s.reportSuccess("install", pluginID, fmt.Sprintf("成功安装插件: %s v%s", pluginName, version))
	return map[string]any{
		"success":     true,
		"message":     "插件安装成功",
		"plugin_id":   pluginID,
		"plugin_name": pluginName,
		"version":     version,
		"path":        target,
	}, nil
}

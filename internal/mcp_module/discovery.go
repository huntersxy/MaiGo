package mcp

import (
	"context"
	"errors"
	"strings"

	"github.com/mark3labs/mcp-go/client"
	mcpprotocol "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// PromptPreview MCP Prompt 摘要，供运行态快照与 WebUI 展示
type PromptPreview struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// ResourcePreview MCP Resource 摘要
type ResourcePreview struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	MIMEType    string `json:"mime_type,omitempty"`
}

// ResourceTemplatePreview MCP Resource Template 摘要
type ResourceTemplatePreview struct {
	URITemplate string `json:"uri_template"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	MIMEType    string `json:"mime_type,omitempty"`
}

type serverFeatureDiscovery struct {
	Prompts           []PromptPreview
	Resources         []ResourcePreview
	ResourceTemplates []ResourceTemplatePreview
}

func serverSupportsTools(caps mcpprotocol.ServerCapabilities) bool {
	return caps.Tools != nil
}

func serverSupportsPrompts(caps mcpprotocol.ServerCapabilities) bool {
	return caps.Prompts != nil
}

func serverSupportsResources(caps mcpprotocol.ServerCapabilities) bool {
	return caps.Resources != nil
}

// discoverServerFeatures 在已初始化的客户端上发现 Prompt/Resource/Template
func discoverServerFeatures(ctx context.Context, cli *client.Client) serverFeatureDiscovery {
	if cli == nil || !cli.IsInitialized() {
		return serverFeatureDiscovery{}
	}
	caps := cli.GetServerCapabilities()
	out := serverFeatureDiscovery{}
	if serverSupportsPrompts(caps) {
		if prompts, err := listAllPrompts(ctx, cli); err == nil {
			for _, item := range prompts {
				out.Prompts = append(out.Prompts, promptPreviewFromMCP(item))
			}
		} else {
			zap.L().Debug("MCP ListPrompts 失败", zap.Error(err))
		}
	}
	if serverSupportsResources(caps) {
		if resources, err := listAllResources(ctx, cli); err == nil {
			for _, item := range resources {
				out.Resources = append(out.Resources, resourcePreviewFromMCP(item))
			}
		} else {
			zap.L().Debug("MCP ListResources 失败", zap.Error(err))
		}
		if templates, err := listAllResourceTemplates(ctx, cli); err == nil {
			for _, item := range templates {
				out.ResourceTemplates = append(out.ResourceTemplates, resourceTemplatePreviewFromMCP(item))
			}
		} else if !errors.Is(err, mcpprotocol.ErrMethodNotFound) {
			zap.L().Debug("MCP ListResourceTemplates 失败", zap.Error(err))
		}
	}
	return out
}

// listAllTools 分页加载服务端暴露的全部工具，对齐 MCPConnection._list_tools
func listAllTools(ctx context.Context, cli *client.Client) ([]mcpprotocol.Tool, error) {
	if cli == nil {
		return nil, nil
	}
	out := make([]mcpprotocol.Tool, 0)
	request := mcpprotocol.ListToolsRequest{}
	for {
		result, err := cli.ListTools(ctx, request)
		if err != nil {
			return out, err
		}
		if result == nil {
			return out, nil
		}
		out = append(out, result.Tools...)
		if result.NextCursor == "" {
			return out, nil
		}
		request.Params.Cursor = result.NextCursor
	}
}

// listAllPrompts 分页加载服务端暴露的全部 Prompt，对齐 MCPConnection._list_prompts
func listAllPrompts(ctx context.Context, cli *client.Client) ([]mcpprotocol.Prompt, error) {
	if cli == nil {
		return nil, nil
	}
	out := make([]mcpprotocol.Prompt, 0)
	request := mcpprotocol.ListPromptsRequest{}
	for {
		result, err := cli.ListPrompts(ctx, request)
		if err != nil {
			return out, err
		}
		if result == nil {
			return out, nil
		}
		out = append(out, result.Prompts...)
		if result.NextCursor == "" {
			return out, nil
		}
		request.Params.Cursor = result.NextCursor
	}
}

// listAllResources 分页加载服务端暴露的全部 Resource，对齐 MCPConnection._list_resources
func listAllResources(ctx context.Context, cli *client.Client) ([]mcpprotocol.Resource, error) {
	if cli == nil {
		return nil, nil
	}
	out := make([]mcpprotocol.Resource, 0)
	request := mcpprotocol.ListResourcesRequest{}
	for {
		result, err := cli.ListResources(ctx, request)
		if err != nil {
			return out, err
		}
		if result == nil {
			return out, nil
		}
		out = append(out, result.Resources...)
		if result.NextCursor == "" {
			return out, nil
		}
		request.Params.Cursor = result.NextCursor
	}
}

// listAllResourceTemplates 分页加载服务端暴露的全部 Resource Template
func listAllResourceTemplates(ctx context.Context, cli *client.Client) ([]mcpprotocol.ResourceTemplate, error) {
	if cli == nil {
		return nil, nil
	}
	out := make([]mcpprotocol.ResourceTemplate, 0)
	request := mcpprotocol.ListResourceTemplatesRequest{}
	for {
		result, err := cli.ListResourceTemplates(ctx, request)
		if err != nil {
			return out, err
		}
		if result == nil {
			return out, nil
		}
		out = append(out, result.ResourceTemplates...)
		if result.NextCursor == "" {
			return out, nil
		}
		request.Params.Cursor = result.NextCursor
	}
}

func promptPreviewFromMCP(prompt mcpprotocol.Prompt) PromptPreview {
	title := strings.TrimSpace(prompt.Title)
	if title == "" {
		title = strings.TrimSpace(prompt.Name)
	}
	return PromptPreview{
		Name:        prompt.Name,
		Title:       title,
		Description: prompt.Description,
	}
}

func resourcePreviewFromMCP(resource mcpprotocol.Resource) ResourcePreview {
	title := strings.TrimSpace(resource.Title)
	if title == "" {
		title = strings.TrimSpace(resource.Name)
	}
	return ResourcePreview{
		URI:         resource.URI,
		Name:        resource.Name,
		Title:       title,
		Description: resource.Description,
		MIMEType:    resource.MIMEType,
	}
}

func resourceTemplatePreviewFromMCP(template mcpprotocol.ResourceTemplate) ResourceTemplatePreview {
	title := strings.TrimSpace(template.Title)
	if title == "" {
		title = strings.TrimSpace(template.Name)
	}
	uriTemplate := ""
	if template.URITemplate != nil && template.URITemplate.Template != nil {
		uriTemplate = template.URITemplate.Raw()
	}
	return ResourceTemplatePreview{
		URITemplate: uriTemplate,
		Name:        template.Name,
		Title:       title,
		Description: template.Description,
		MIMEType:    template.MIMEType,
	}
}

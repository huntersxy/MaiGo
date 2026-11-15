package agent

// 传话附图解析，对齐 MaiBot src/agent/images.py 的 resolve_images。
//
// 引用来源按 Python 优先级依次为：当前会话的聊天图片（SourceImages 索引）、
// 工作区 relay_images 下的 hash 文件、宿主提供的截图。解析结果写入工作区
// relay_images，并以组件形式交给传话链路。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"MaiGo/internal/common/media"
)

// relayImageDir 工作区内放传话图片的子目录，对齐 Python 的 relay_images
const relayImageDir = "relay_images"

// relayHashPattern 工作区图片的 hash 形式，对齐 Python 的 [a-f0-9]{64}
var relayHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// RelayImage 一张已落盘的传话图片
type RelayImage struct {
	// Hash 图片内容哈希
	Hash string `json:"hash"`
	// Path 落盘路径
	Path string `json:"path"`
	// Content 供模型阅读的引用文本
	Content string `json:"content"`
}

// ImageResolver 提供解析传话图片所需的外部能力
type ImageResolver interface {
	// SourceImages 返回当前会话可用的图片来源索引
	SourceImages() map[string][]media.Ref
	// Workspace 返回 Agent 工作区目录
	Workspace() string
	// Screenshot 返回宿主当前截图，仅在调用方为 Agent 自身时需要
	Screenshot(ctx context.Context) ([]byte, error)
	// AllowAgentOnlyRefs 对齐 caller == agent_id：允许 screenshot / 工作区 hash / 工作区路径
	AllowAgentOnlyRefs() bool
	// ResolveWorkspacePath 对齐 AgentService.image_path；未实现时返回空路径
	ResolveWorkspacePath(reference string) (string, error)
}

// ResolveRelayImages 解析传话图片引用
//
// 对齐 Python resolve_images：引用必须是非空字符串列表；
// 空列表直接返回；每个引用先在会话图片索引里找，找不到再按
// 「截图」「工作区 hash 文件」的顺序解析，都不匹配则报错。
func ResolveRelayImages(ctx context.Context, resolver ImageResolver, references []string) ([]RelayImage, error) {
	if resolver == nil {
		if len(references) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("图片解析器未接线")
	}
	for _, reference := range references {
		if strings.TrimSpace(reference) == "" {
			return nil, fmt.Errorf("images 必须是非空图片引用组成的列表")
		}
	}
	if len(references) == 0 {
		return nil, nil
	}

	available := resolver.SourceImages()
	workspace := strings.TrimSpace(resolver.Workspace())
	out := make([]RelayImage, 0, len(references))

	for _, reference := range references {
		reference = strings.TrimSpace(reference)

		// 1) 当前会话的图片索引命中，可能一对多
		if refs, ok := available[reference]; ok && len(refs) > 0 {
			for _, item := range refs {
				data, err := readMediaRef(item)
				if err != nil {
					return nil, err
				}
				image, err := saveRelayImage(workspace, data, item.Mime)
				if err != nil {
					return nil, err
				}
				out = append(out, image)
			}
			continue
		}

		// 2/3/4) screenshot、工作区 hash、工作区路径仅 Agent 自身可用
		if resolver.AllowAgentOnlyRefs() {
			if reference == "screenshot" {
				data, err := resolver.Screenshot(ctx)
				if err != nil {
					return nil, err
				}
				image, err := saveRelayImage(workspace, data, "")
				if err != nil {
					return nil, err
				}
				out = append(out, image)
				continue
			}
			if relayHashPattern.MatchString(reference) {
				pattern := filepath.Join(workspace, relayImageDir, reference+".*")
				matches, err := filepath.Glob(pattern)
				if err != nil {
					return nil, err
				}
				if len(matches) != 1 {
					return nil, fmt.Errorf("没有唯一的工作区图片：%s", reference)
				}
				data, err := os.ReadFile(matches[0])
				if err != nil {
					return nil, err
				}
				image, err := saveRelayImage(workspace, data, "")
				if err != nil {
					return nil, err
				}
				out = append(out, image)
				continue
			}
			path, err := resolver.ResolveWorkspacePath(reference)
			if err != nil {
				return nil, err
			}
			if path != "" {
				data, err := os.ReadFile(path)
				if err != nil {
					return nil, err
				}
				image, err := saveRelayImage(workspace, data, "")
				if err != nil {
					return nil, err
				}
				out = append(out, image)
				continue
			}
		}

		return nil, fmt.Errorf("当前聊天上下文中没有图片：%s；请使用带图消息 ID 或图片 hash", reference)
	}
	return out, nil
}

// readMediaRef 读取媒体引用指向的文件
func readMediaRef(ref media.Ref) ([]byte, error) {
	path := strings.TrimSpace(ref.Path)
	if path == "" {
		return nil, fmt.Errorf("图片引用缺少路径: hash=%s", ref.Hash)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取图片失败: %w", err)
	}
	return data, nil
}

// saveRelayImage 把图片写入工作区 relay_images
//
// 对齐 Python save()：文件名用内容哈希，扩展名取 MIME 子类型，
// 并生成供模型阅读的引用文本。
func saveRelayImage(workspace string, data []byte, mime string) (RelayImage, error) {
	if strings.TrimSpace(workspace) == "" {
		return RelayImage{}, fmt.Errorf("Agent 工作区未配置")
	}
	if len(data) == 0 {
		return RelayImage{}, fmt.Errorf("图片内容为空")
	}
	// 对齐 Python ImageComponent: sha256(binary_data).hexdigest()
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	folder := filepath.Join(workspace, relayImageDir)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return RelayImage{}, fmt.Errorf("创建传话图片目录失败: %w", err)
	}
	suffix := mimeSubtype(mime, data)
	path := filepath.Join(folder, hash+"."+suffix)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return RelayImage{}, fmt.Errorf("写入传话图片失败: %w", err)
	}
	return RelayImage{
		Hash:    hash,
		Path:    path,
		Content: fmt.Sprintf("[传话图片 hash=%s，附件文件=%s]", hash, path),
	}, nil
}

// maiWorkspacePrefix 对齐 Python PureWindowsPath(r"C:\MaiWorkspace")
const maiWorkspacePrefix = `C:\MaiWorkspace`

// ResolveAgentImagePath 对齐 AgentService.image_path：
// - C:\MaiWorkspace\... 映射到电脑宿主工作目录（需 requireEnabled）
// - 其它路径必须落在 agentWorkspace 内
func ResolveAgentImagePath(reference, agentWorkspace, computerWorkspace string, requireEnabled func() error) (string, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return "", fmt.Errorf("附图必须位于当前 Agent 工作目录或已启用的电脑工作目录。")
	}
	normalized := strings.ReplaceAll(reference, "/", "\\")
	lower := strings.ToLower(normalized)
	prefix := strings.ToLower(maiWorkspacePrefix)
	var workspace string
	var rel string
	if lower == prefix || strings.HasPrefix(lower, prefix+"\\") {
		if requireEnabled != nil {
			if err := requireEnabled(); err != nil {
				return "", err
			}
		}
		workspace = strings.TrimSpace(computerWorkspace)
		if workspace == "" {
			return "", fmt.Errorf("电脑服务未启用或正在关闭，请检查 computer.enabled。")
		}
		if lower == prefix {
			rel = "."
		} else {
			rel = normalized[len(maiWorkspacePrefix)+1:]
		}
	} else {
		workspace = strings.TrimSpace(agentWorkspace)
		if workspace == "" {
			return "", fmt.Errorf("Agent 工作区未配置")
		}
		raw := reference
		if filepath.IsAbs(raw) {
			path, err := filepath.Abs(raw)
			if err != nil {
				return "", err
			}
			return ensureUnderWorkspace(workspace, path)
		}
		rel = raw
	}
	base, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(base, filepath.FromSlash(strings.ReplaceAll(rel, "\\", "/")))
	path, err := filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	return ensureUnderWorkspace(base, path)
}

func ensureUnderWorkspace(workspace, path string) (string, error) {
	base, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(base, target)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return "", fmt.Errorf("附图必须位于当前 Agent 工作目录或已启用的电脑工作目录。")
	}
	return target, nil
}

// mimeSubtype 从 MIME 取子类型作为扩展名，缺失时按内容嗅探
func mimeSubtype(mime string, data []byte) string {
	if parts := strings.SplitN(strings.TrimSpace(mime), "/", 2); len(parts) == 2 {
		sub := strings.ToLower(strings.TrimSpace(parts[1]))
		sub = strings.TrimPrefix(sub, "x-")
		if sub == "jpeg" {
			return "jpg"
		}
		if sub != "" && sub != "octet-stream" {
			return sub
		}
	}
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 {
		return "jpg"
	}
	if len(data) >= 4 && string(data[:4]) == "\x89PNG" {
		return "png"
	}
	if len(data) >= 6 && string(data[:3]) == "GIF" {
		return "gif"
	}
	return "png"
}

package selfupdate

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// 本文件对齐 ../MaiBot/src/common/self_update.py _sync_dependencies：
// uv 依赖源网络故障时，用同名空本地索引临时覆盖故障源后重试

const dependencyTimeout = 30 * time.Minute

var failedFetchPattern = regexp.MustCompile("Failed to fetch:\\s*`(https?://[^`]+)`")

type uvIndex struct {
	Name    string `toml:"name"`
	URL     string `toml:"url"`
	Default bool   `toml:"default"`
}

type uvIndexConfig struct {
	Index []uvIndex `toml:"index"`
	Tool  struct {
		UV struct {
			Index []uvIndex `toml:"index"`
		} `toml:"uv"`
	} `toml:"tool"`
}

// syncUVDependencies 执行 uv sync；遇到索引网络故障时临时排除该源后重试
func syncUVDependencies(ctx context.Context, root string) error {
	emptyIndex, err := os.MkdirTemp("", "maibot-update-index-")
	if err != nil {
		return errorf("dependency_failed", "创建临时索引目录失败: %v", err)
	}
	defer os.RemoveAll(emptyIndex)

	args := []string{"sync", "--inexact", "--no-dev", "--no-install-project"}
	excluded := make(map[string]string)
	deadline := time.Now().Add(dependencyTimeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return errorf("dependency_timeout", "依赖同步超时（%g 秒）: uv %s",
				dependencyTimeout.Seconds(), strings.Join(args, " "))
		}
		output, runErr := runDependencyCommandOutput(ctx, remaining, root, "uv", args...)
		if runErr == nil {
			return nil
		}
		if errors.Is(runErr, context.DeadlineExceeded) {
			return errorf("dependency_timeout", "依赖同步超时（%g 秒）: uv %s\n%s",
				dependencyTimeout.Seconds(), strings.Join(args, " "), outputTail(output))
		}
		failureURL := parseFailedFetchURL(output)
		index := (*uvIndex)(nil)
		if failureURL != "" {
			index = failedDependencyIndex(root, failureURL)
		}
		_, alreadyExcluded := excluded[strings.ToLower(index.Name)]
		if index == nil || alreadyExcluded {
			detail := ""
			if len(excluded) > 0 {
				names := make([]string, 0, len(excluded))
				for _, name := range excluded {
					names = append(names, name)
				}
				sort.Strings(names)
				detail = "\n本次已临时排除故障源：" + strings.Join(names, ", ")
			}
			return errorf("dependency_failed", "依赖同步失败: uv %s\n%s%s",
				strings.Join(args, " "), outputTail(output), detail)
		}
		excluded[strings.ToLower(index.Name)] = index.Name
		option := "--index"
		if index.Default {
			option = "--default-index"
		}
		args = append(args, option, index.Name+"="+fileURI(emptyIndex))
	}
}

// runDependencyCommandOutput 执行依赖命令并返回合并输出
func runDependencyCommandOutput(ctx context.Context, timeout time.Duration, root, name string, args ...string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(runCtx, name, args...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return string(output), context.DeadlineExceeded
	}
	return string(output), err
}

func parseFailedFetchURL(output string) string {
	match := failedFetchPattern.FindStringSubmatch(output)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

// failedDependencyIndex 对齐 Python _failed_dependency_index
func failedDependencyIndex(root, failedURL string) *uvIndex {
	configPath := filepath.Join(root, "uv.toml")
	var indexes []uvIndex
	if info, err := os.Stat(configPath); err == nil && !info.IsDir() {
		raw, err := os.ReadFile(configPath)
		if err != nil {
			return nil
		}
		var config uvIndexConfig
		if err := toml.Unmarshal(raw, &config); err != nil {
			return nil
		}
		indexes = config.Index
	} else {
		raw, err := os.ReadFile(filepath.Join(root, "pyproject.toml"))
		if err != nil {
			return nil
		}
		var config uvIndexConfig
		if err := toml.Unmarshal(raw, &config); err != nil {
			return nil
		}
		indexes = config.Tool.UV.Index
	}
	for index := range indexes {
		item := indexes[index]
		prefix := strings.TrimRight(item.URL, "/") + "/"
		if item.Name != "" && item.URL != "" && strings.HasPrefix(failedURL, prefix) {
			return &item
		}
	}
	return nil
}

func outputTail(output string) string {
	text := strings.TrimSpace(output)
	if len(text) > 4000 {
		text = text[len(text)-4000:]
	}
	return text
}

func fileURI(path string) string {
	slashPath := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	return (&url.URL{Scheme: "file", Path: slashPath}).String()
}

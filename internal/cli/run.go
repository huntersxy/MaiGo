package cli

import (
	"fmt"
	"os"
	"path/filepath"
)

func validateInstance(directory string) ([]string, error) {
	cfgDir := filepath.Join(directory, "config")
	var missing []string
	for _, name := range []string{"bot_config.toml", "model_config.toml"} {
		path := filepath.Join(cfgDir, name)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				missing = append(missing, name)
				continue
			}
			return nil, fmt.Errorf("检查 %s: %w", path, err)
		}
		if info.IsDir() {
			missing = append(missing, name)
		}
	}
	return missing, nil
}

// prepareRun 设置实例环境并切换工作目录，供 main 继续启动 bot
func prepareRun(directory string) error {
	missing, err := validateInstance(directory)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf("实例未初始化，缺少 %s；请先执行 %s init --directory %q",
			joinMissing(missing), programName(), directory)
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("创建实例目录: %w", err)
	}
	if err := os.Setenv(InstanceDirEnv, directory); err != nil {
		return fmt.Errorf("设置 %s: %w", InstanceDirEnv, err)
	}
	if err := os.Chdir(directory); err != nil {
		return fmt.Errorf("切换工作目录: %w", err)
	}
	return nil
}

func joinMissing(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + "、" + names[1]
	default:
		out := names[0]
		for _, n := range names[1:] {
			out += "、" + n
		}
		return out
	}
}

// prepareWorkspace 设置工作区环境并切换目录；不要求配置已存在
func prepareWorkspace(directory string) error {
	if err := os.MkdirAll(directory, 0o777); err != nil {
		return fmt.Errorf("创建工作区目录: %w", err)
	}
	if err := os.Setenv(InstanceDirEnv, directory); err != nil {
		return fmt.Errorf("设置 %s: %w", InstanceDirEnv, err)
	}
	if err := os.Chdir(directory); err != nil {
		return fmt.Errorf("切换工作目录: %w", err)
	}
	return nil
}

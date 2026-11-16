package desktoppet

// 探索产物的精简结构；工具声明与持久化校验共用同一份定义，对齐 desktop_pet/exploration_results.py

import (
	"fmt"
	"strings"
)

// ExplorationResource 一条探索资源
type ExplorationResource struct {
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	Summary  string `json:"summary"`
	Content  string `json:"content,omitempty"`
	Source   string `json:"source,omitempty"`
	Location string `json:"location,omitempty"`
}

// ExplorationExperience 一条探索经验
type ExplorationExperience struct {
	Title          string   `json:"title"`
	Conditions     string   `json:"conditions"`
	Path           []string `json:"path"`
	Outcome        string   `json:"outcome"`
	Result         string   `json:"result"`
	Feedback       string   `json:"feedback"`
	FeedbackSource string   `json:"feedback_source"`
}

// ExplorationProducts 一次探索的完整产物快照
type ExplorationProducts struct {
	Resources   []ExplorationResource   `json:"resources"`
	Experiences []ExplorationExperience `json:"experiences"`
}

// ExperienceReuse 复用反馈，额外要求可执行的改进或失败原因
type ExperienceReuse struct {
	ExplorationExperience
	Improvement   string `json:"improvement"`
	FailureReason string `json:"failure_reason"`
}

// ValidateExplorationResource 校验资源字段与媒体地址
func ValidateExplorationResource(resource ExplorationResource) error {
	if length := runeLen(resource.Title); length < 1 || length > 80 {
		return fmt.Errorf("资源标题必须为 1 至 80 字")
	}
	switch resource.Kind {
	case "text", "image", "audio", "video", "file":
	default:
		return fmt.Errorf("资源类型必须为 text/image/audio/video/file")
	}
	if length := runeLen(resource.Summary); length < 1 || length > 240 {
		return fmt.Errorf("资源摘要必须为 1 至 240 字")
	}
	if runeLen(resource.Content) > 1200 || runeLen(resource.Source) > 2000 || runeLen(resource.Location) > 2000 {
		return fmt.Errorf("资源字段超出长度上限")
	}
	if resource.Kind == "text" && strings.TrimSpace(resource.Content) == "" {
		return fmt.Errorf("文本资源必须提供精简正文 content")
	}
	if resource.Kind != "text" && strings.TrimSpace(resource.Location) == "" {
		return fmt.Errorf("媒体或文件资源必须提供实际地址 location")
	}
	return nil
}

// ValidateExplorationExperience 校验经验字段
func ValidateExplorationExperience(experience ExplorationExperience) error {
	if length := runeLen(experience.Title); length < 1 || length > 80 {
		return fmt.Errorf("经验标题必须为 1 至 80 字")
	}
	if length := runeLen(experience.Conditions); length < 1 || length > 240 {
		return fmt.Errorf("经验适用条件必须为 1 至 240 字")
	}
	if len(experience.Path) < 1 || len(experience.Path) > 6 {
		return fmt.Errorf("经验路径必须为 1 至 6 步")
	}
	for _, step := range experience.Path {
		length := runeLen(strings.TrimSpace(step))
		if length < 1 || length > 160 {
			return fmt.Errorf("经验路径每步必须为 1 至 160 字")
		}
	}
	switch experience.Outcome {
	case "success", "partial", "failed":
	default:
		return fmt.Errorf("经验结果必须为 success/partial/failed")
	}
	if length := runeLen(experience.Result); length < 1 || length > 300 {
		return fmt.Errorf("经验结果描述必须为 1 至 300 字")
	}
	if length := runeLen(experience.Feedback); length < 1 || length > 300 {
		return fmt.Errorf("经验反馈必须为 1 至 300 字")
	}
	switch experience.FeedbackSource {
	case "observation", "user", "unverified":
	default:
		return fmt.Errorf("反馈来源必须为 observation/user/unverified")
	}
	return nil
}

// ValidateExplorationProducts 校验完整产物快照
func ValidateExplorationProducts(products ExplorationProducts) error {
	if len(products.Resources) > 12 || len(products.Experiences) > 8 {
		return fmt.Errorf("产物数量超出上限")
	}
	for _, resource := range products.Resources {
		if err := ValidateExplorationResource(resource); err != nil {
			return err
		}
	}
	for _, experience := range products.Experiences {
		if err := ValidateExplorationExperience(experience); err != nil {
			return err
		}
	}
	return nil
}

// ValidateExperienceReuse 校验复用反馈
func ValidateExperienceReuse(reuse ExperienceReuse) error {
	if err := ValidateExplorationExperience(reuse.ExplorationExperience); err != nil {
		return err
	}
	if runeLen(reuse.Improvement) > 300 || runeLen(reuse.FailureReason) > 300 {
		return fmt.Errorf("改进或失败原因超出长度上限")
	}
	if reuse.Outcome == "success" && strings.TrimSpace(reuse.Improvement) == "" {
		return fmt.Errorf("成功复用必须记录验证到的适用条件或步骤改进")
	}
	if (reuse.Outcome == "partial" || reuse.Outcome == "failed") && strings.TrimSpace(reuse.FailureReason) == "" {
		return fmt.Errorf("失败或部分成功必须记录原因，未查明时明确说明")
	}
	return nil
}

func runeLen(text string) int {
	return len([]rune(text))
}

package replyer

import (
	"strings"
	"testing"
)

func TestPipelineRemoveBracketContentToggle(t *testing.T) {
	base := Config{
		EnablePostProcess:    true,
		RemoveBracketContent: false,
		TypingSpeed:          1,
		Splitter:             SplitterConfig{Mode: "rule", MaxLength: 512, MaxSentenceNum: 8, MaxSplitNum: 3},
	}
	kept := ApplyPipeline("你好（笑）", Options{Config: base})
	if len(kept.Segments) == 0 || !strings.Contains(kept.Segments[0].Text, "（笑）") {
		t.Fatalf("关闭清理后括号内容应保留: %#v", kept.Segments)
	}
	base.RemoveBracketContent = true
	stripped := ApplyPipeline("你好（笑）", Options{Config: base})
	if len(stripped.Segments) == 0 || strings.Contains(stripped.Segments[0].Text, "（笑）") {
		t.Fatalf("开启清理后括号内容应删除: %#v", stripped.Segments)
	}
}

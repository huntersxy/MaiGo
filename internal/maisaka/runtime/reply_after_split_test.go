package runtime

import (
	"context"
	"testing"

	"MaiGo/internal/chat/replyer"
	"MaiGo/internal/plugin_runtime/hooks"
)

type replySplitEmitter struct {
	segments []any
}

func (e replySplitEmitter) EmitHook(context.Context, string, map[string]any) hooks.DispatchResult {
	return hooks.DispatchResult{Kwargs: map[string]any{"segments": e.segments}}
}

func TestReplyAfterSplitAppliesPluginSegments(t *testing.T) {
	hooks.SetGlobalEmitter(replySplitEmitter{segments: []any{
		map[string]any{"text": "错字", "separator": "，", "quote_previous": false, "is_correction": false},
		map[string]any{"text": "纠正", "separator": "", "quote_previous": true, "is_correction": true},
	}})
	defer hooks.SetGlobalEmitter(nil)
	agent := &Agent{}
	segments := agent.replyAfterSplit(context.Background(), []replyer.Segment{{Text: "原文"}}, true)
	if len(segments) != 2 || segments[0].Text != "错字" || !segments[1].IsCorrection || !segments[1].QuotePrevious {
		t.Fatalf("分段 Hook 结果未生效: %#v", segments)
	}
}

func TestReplyAfterSplitRejectsInvalidSegments(t *testing.T) {
	hooks.SetGlobalEmitter(replySplitEmitter{segments: []any{
		map[string]any{"text": "", "separator": "", "quote_previous": false, "is_correction": false},
	}})
	defer hooks.SetGlobalEmitter(nil)
	agent := &Agent{}
	original := []replyer.Segment{{Text: "原文"}}
	segments := agent.replyAfterSplit(context.Background(), original, true)
	if len(segments) != 1 || segments[0].Text != "原文" {
		t.Fatalf("非法分段应保持原值: %#v", segments)
	}
}

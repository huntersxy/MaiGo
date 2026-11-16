package emojisystem

import (
	"math"
	"path/filepath"
	"testing"
)

func TestValidateEmojiTags(t *testing.T) {
	tags, err := validateEmojiTags(`{"emotion_tags":["开心"," 开心 ","","难过"],"content_tags":["猫","猫","捂脸"]}`)
	if err != nil {
		t.Fatalf("解析标签失败: %v", err)
	}
	if len(tags.EmotionTags) != 2 || tags.EmotionTags[0] != "开心" || tags.EmotionTags[1] != "难过" {
		t.Fatalf("情绪标签归一化不符合预期: %+v", tags.EmotionTags)
	}
	if len(tags.ContentTags) != 2 {
		t.Fatalf("画面标签应去重: %+v", tags.ContentTags)
	}
	if _, err := validateEmojiTags(`{"emotion_tags":[],"content_tags":[]}`); err == nil {
		t.Fatal("空 content_tags 应当报错")
	}
}

func TestEmojiSemanticIndexRoundTrip(t *testing.T) {
	previous := emojiSemanticIndexPathOverride
	emojiSemanticIndexPathOverride = filepath.Join(t.TempDir(), "emoji_semantic_index.sqlite3")
	defer func() { emojiSemanticIndexPathOverride = previous }()

	tags := EmojiTags{EmotionTags: []string{"自嘲"}, ContentTags: []string{"猫", "捂脸"}}
	if err := saveEmojiTags("hash-a", tags); err != nil {
		t.Fatalf("写入标签失败: %v", err)
	}
	loaded, ok := GetEmojiTags("hash-a")
	if !ok || len(loaded.ContentTags) != 2 || loaded.EmotionTags[0] != "自嘲" {
		t.Fatalf("读取标签不符合预期: %+v ok=%v", loaded, ok)
	}
	configKey := "config"
	space := emojiSemanticAxisKey(configKey, "content")
	if err := saveEmojiSemanticVector(configKey, "猫、捂脸", space, []float64{1, 0}); err != nil {
		t.Fatalf("写入向量失败: %v", err)
	}
	vectors, err := readEmojiSemanticIndex(configKey, space, false)
	if err != nil {
		t.Fatalf("读取向量失败: %v", err)
	}
	if len(vectors["猫、捂脸"]) != 2 || vectors["猫、捂脸"][0] != 1 {
		t.Fatalf("向量内容不符合预期: %+v", vectors)
	}
	labels, err := readEmojiSemanticIndexLabels(configKey, space)
	if err != nil {
		t.Fatalf("读取标签索引失败: %v", err)
	}
	if _, ok := labels["猫、捂脸"]; !ok {
		t.Fatalf("标签索引缺少描述: %+v", labels)
	}
}

func TestScoreEmojiCandidates(t *testing.T) {
	scores, err := scoreEmojiCandidates([]float64{1, 0}, [][2]any{
		{"a", []float64{1, 0}},
		{"b", []float64{0, 1}},
		{"invalid", []float64{}},
	})
	if err != nil {
		t.Fatalf("余弦匹配失败: %v", err)
	}
	if math.Abs(scores["a"]-1) > 1e-9 || math.Abs(scores["b"]) > 1e-9 {
		t.Fatalf("余弦分数不符合预期: %+v", scores)
	}
	if _, ok := scores["invalid"]; ok {
		t.Fatalf("无效向量不应参与评分: %+v", scores)
	}
	if _, err := scoreEmojiCandidates([]float64{0, 0}, [][2]any{{"a", []float64{1, 0}}}); err == nil {
		t.Fatal("零向量应报错")
	}
}

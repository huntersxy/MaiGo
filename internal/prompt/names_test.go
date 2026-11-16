package prompt

import "testing"

func TestReplyPromptNameAliases(t *testing.T) {
	cases := map[string]string{
		"retro_replyer":              "reply_group",
		"retro_replyer_light":        "reply_group",
		"retro_private_replyer":      "reply_private",
		"retro_private_replyer_self": "reply_private_followup",
	}
	for input, want := range cases {
		got, err := NormalizePromptName(input)
		if err != nil || got != want {
			t.Fatalf("NormalizePromptName(%q) = %q, %v; 期望 %q", input, got, err, want)
		}
	}
	if got := CustomPromptStorageName("reply_private_followup"); got != "retro_private_replyer_self" {
		t.Fatalf("存储名 = %q", got)
	}
}

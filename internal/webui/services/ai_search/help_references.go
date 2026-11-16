package search

// 答疑组合检索中的少量维护者核实参考，对齐 src/webui/services/ai_help_references.py

import "strings"

type helpReference struct {
	SourceID string
	Source   Source
	Content  string
	Groups   [][]string
}

var helpReferences = []helpReference{
	{
		SourceID: "help:ollama",
		Source: Source{
			Title: "Ollama · OpenAI compatibility",
			URL:   "https://docs.ollama.com/api/openai-compatibility",
		},
		Groups: [][]string{{"ollama"}},
		Content: "For a local Ollama service on the same host as the MaiBot process, the OpenAI-compatible Base URL " +
			"is http://localhost:11434/v1/ (also http://127.0.0.1:11434/v1/). If MaiBot runs in a separate container " +
			"or on another host, use an Ollama address reachable from MaiBot; localhost refers to MaiBot's own network " +
			"namespace. Ollama's SDK examples use a dummy API key, which the local Ollama service ignores. " +
			"In MaiBot WebUI → 模型配置 / Model Management, add the provider with client_type=openai, auth_type=none " +
			"and an empty api_key. Add a model with the actual Ollama model_identifier and set its api_provider to " +
			"that provider. Use the provider connection test, then send a model test request. Assign the model to a " +
			"task only after confirming that the request succeeds.",
	},
	{
		SourceID: "help:nginx-websocket",
		Source: Source{
			Title: "NGINX · WebSocket proxying",
			URL:   "https://nginx.org/en/docs/http/websocket.html",
		},
		Groups: [][]string{
			{"nginx", "反代", "reverse proxy", "リバースプロキシ"},
			{"websocket", "ws://", "wss", "握手", "https"},
		},
		Content: "If an HTTPS WebUI tries ws:// and the browser blocks mixed content, first inspect the actual URL in " +
			"Developer Tools → Network → WS. The browser-facing connection must use wss://; fix the client or public " +
			"URL before debugging the upstream handshake. When wss:// reaches NGINX, check that the same proxy route " +
			"forwards Upgrade and Connection headers. The linked NGINX documentation supports this proxy step; " +
			"the mixed-content diagnosis follows from the reported browser URL. A successful upgrade returns HTTP 101 Switching " +
			"Protocols. Verify a live log update or chat event after the handshake.",
	},
	{
		SourceID: "help:group-reply",
		Source: Source{
			Title: "MaiBot · 群聊静默处理",
			URL:   "https://github.com/Mai-with-u/MaiBot/blob/5553542622d1a8d96a9ab73484ce3530dca202d4/src/maisaka/reasoning_engine.py#L1127",
		},
		Groups: [][]string{
			{"群聊", "群消息", "group chat", "グループ"},
			{"不回复", "不回", "禁用", "暂停", "no reply", "返信しない"},
		},
		Content: "MaiBot can keep receiving group messages even when the effective group reply frequency is 0. " +
			"In this case it consumes messages and maintains history without entering Planner. In WebUI, open the " +
			"actual group conversation settings and check its effective reply frequency before changing adapter rules. " +
			"Adapter filtering is a separate stage and can prevent messages reaching the bot at all. If the frequency " +
			"is above 0, reproduce once and correlate the group message with Planner and Replyer requests. A recorded " +
			"timeout or provider error on a request establishes a model failure; an absent request alone does not.",
	},
}

// MatchHelpReferences 返回所有关键词组都命中的核实参考
func MatchHelpReferences(query string) []map[string]string {
	normalized := strings.ToLower(query)
	out := make([]map[string]string, 0, len(helpReferences))
	for _, reference := range helpReferences {
		matched := true
		for _, group := range reference.Groups {
			found := false
			for _, term := range group {
				if strings.Contains(normalized, term) {
					found = true
					break
				}
			}
			if !found {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		out = append(out, map[string]string{
			"source_id": reference.SourceID,
			"title":     reference.Source.Title,
			"url":       reference.Source.URL,
			"content":   reference.Content,
		})
	}
	return out
}

// GetHelpReference 按 ID 返回参考来源
func GetHelpReference(sourceID string) (Source, bool) {
	for _, reference := range helpReferences {
		if reference.SourceID == sourceID {
			return reference.Source, true
		}
	}
	return Source{}, false
}

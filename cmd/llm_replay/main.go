package main

// LLM 失败请求快照重放工具
// 用法: go run ./cmd/llm_replay <snapshot.json>
// 凭据: MAIBOT_REPLAY_API_KEY（快照里的凭据已脱敏）
// 支持 chat.completions、openai responses 与 Gemini generateContent；其他协议返回明确错误

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"MaiGo/internal/llm_models"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "用法: go run ./cmd/llm_replay <snapshot.json>")
		os.Exit(2)
	}
	request, err := llm.ReplaySnapshot(os.Args[1], "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "构造重放请求失败:", err)
		os.Exit(1)
	}
	client := &http.Client{Timeout: 120 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		fmt.Fprintln(os.Stderr, "重放请求失败:", err)
		os.Exit(1)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	fmt.Fprintf(os.Stderr, "HTTP %d %s\n", response.StatusCode, request.URL)
	os.Stdout.Write(body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		os.Exit(1)
	}
}

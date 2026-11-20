package host

// 宿主独占终端输入；选择目标后经私有 IPC 交给 Bot 的本地操作员链路。
// 对齐 src/host/console.py 的命令语义。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// HostConsole 宿主终端输入
type HostConsole struct {
	supervisor *Supervisor
	stopping   chan struct{}
	botID      string
	botName    string
	reader     *bufio.Reader
}

// NewHostConsole 创建宿主终端
func NewHostConsole(supervisor *Supervisor, stopping chan struct{}) *HostConsole {
	return &HostConsole{supervisor: supervisor, stopping: stopping, botName: "未选择 Bot"}
}

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// Run 读取宿主终端；非交互环境立即返回，不阻塞 Bot
func (c *HostConsole) Run(ctx context.Context) {
	if !stdinIsTerminal() {
		return
	}
	enabled := make([]BotStatus, 0)
	statuses, err := c.supervisor.Statuses()
	if err != nil {
		return
	}
	for _, status := range statuses {
		if c.inputEnabled(status) {
			enabled = append(enabled, status)
		}
	}
	if len(enabled) == 0 {
		return
	}
	c.reader = bufio.NewReader(os.Stdin)
	selectCtx, cancelSelect := context.WithCancel(ctx)
	defer cancelSelect()
	go c.selectDefault(selectCtx, enabled)
	_, _ = c.handle("/host-help")
	for {
		select {
		case <-c.stopping:
			fmt.Println("终端输入已关闭，Bot 继续运行。")
			return
		case <-ctx.Done():
			return
		default:
		}
		if c.botID != "" {
			fmt.Printf("[%s] > ", c.botName)
		} else {
			fmt.Print("> ")
		}
		line, err := c.reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				fmt.Println()
				return
			}
			fmt.Fprintf(os.Stderr, "终端输入失败：%v（未自动重发）\n", err)
			return
		}
		stop, err := c.handle(strings.TrimSpace(line))
		if err != nil {
			fmt.Fprintf(os.Stderr, "终端输入失败：%v（未自动重发）\n", err)
		}
		if stop {
			return
		}
	}
}

func (c *HostConsole) inputEnabled(status BotStatus) bool {
	if status.Initialization != "initialized" {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(status.Directory, "config", "bot_config.toml"))
	if err != nil {
		return false
	}
	var document map[string]any
	if err := toml.Unmarshal(raw, &document); err != nil {
		return false
	}
	debug, _ := document["debug"].(map[string]any)
	value, exists := debug["enable_console_input"]
	if !exists {
		return true
	}
	enabled, ok := value.(bool)
	return ok && enabled
}

func (c *HostConsole) selectDefault(ctx context.Context, enabled []BotStatus) {
	allowed := map[string]struct{}{}
	for _, status := range enabled {
		allowed[status.BotID] = struct{}{}
	}
	for c.botID == "" {
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
		statuses, err := c.supervisor.Statuses()
		if err != nil {
			continue
		}
		running := make([]BotStatus, 0, 1)
		for _, status := range statuses {
			if status.Status == "running" {
				if _, ok := allowed[status.BotID]; ok {
					running = append(running, status)
				}
			}
		}
		if len(running) == 1 && c.botID == "" {
			c.setTarget(running[0])
			return
		}
	}
}

func (c *HostConsole) setTarget(status BotStatus) {
	c.botID, c.botName = status.BotID, status.Name
	fmt.Printf("终端目标：%s（%s）\n", c.botName, c.botID)
}

func (c *HostConsole) selectBot(selector string) error {
	statuses, err := c.supervisor.Statuses()
	if err != nil {
		return err
	}
	matches := make([]BotStatus, 0)
	for _, status := range statuses {
		if status.BotID == selector {
			matches = []BotStatus{status}
			break
		}
		if status.Name == selector || strings.HasPrefix(status.BotID, selector) {
			matches = append(matches, status)
		}
	}
	if selector == "" || len(matches) != 1 {
		return errors.New("Bot 不存在或名称/ID前缀不唯一，请用 /bots 查看完整 ID")
	}
	c.setTarget(matches[0])
	return nil
}

func (c *HostConsole) handle(text string) (bool, error) {
	switch {
	case text == "":
		return false, nil
	case strings.EqualFold(text, "exit()"):
		return true, nil
	case text == "/bots":
		statuses, err := c.supervisor.Statuses()
		if err != nil {
			return false, err
		}
		for _, status := range statuses {
			marker := " "
			if status.BotID == c.botID {
				marker = "*"
			}
			fmt.Printf("%s %s  %s  %s\n", marker, status.Name, status.BotID, status.Status)
		}
		return false, nil
	case text == "/use" || strings.HasPrefix(text, "/use "):
		return false, c.selectBot(strings.TrimSpace(strings.TrimPrefix(text, "/use")))
	case text == "/host-help":
		fmt.Println("/bots 列出 Bot；/use 名称或ID前缀 切换；/help 查看 Bot 指令；exit() 关闭输入；Ctrl+C 关闭宿主。")
		return false, nil
	default:
		return false, c.request(http.MethodPost, "", text)
	}
}

func (c *HostConsole) request(method, path, text string) error {
	if c.botID == "" {
		return errors.New("请先用 /bots 查看，再用 /use 名称或ID前缀选择 Bot")
	}
	endpoint, err := c.supervisor.ProxyEndpoint(c.botID)
	if err != nil {
		return err
	}
		transport := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialEndpoint(ctx, endpoint)
			},
		}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	var body io.Reader
	if text != "" {
		encoded, err := json.Marshal(map[string]any{"text": text})
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, "http://bot/_host/console"+path, body)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Bot 拒绝终端请求（%d）：%s", response.StatusCode, strings.TrimSpace(string(payload)))
	}
	return nil
}

package host

// 宿主运行主流程与本机控制通道，对齐 src/host/control.py。

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	controlTimeout       = 120 * time.Second
	controlActionTimeout = 1800 * time.Second
)

// Control 宿主控制通道
type Control struct {
	supervisor *Supervisor
	stopping   chan struct{}
	stopOnce   sync.Once
	webui      *WebUI
	server     net.Listener
	mu         sync.Mutex
}

// NewControl 创建控制通道
func NewControl(supervisor *Supervisor) *Control {
	return &Control{supervisor: supervisor, stopping: make(chan struct{})}
}

// Stop 请求关闭宿主
func (c *Control) Stop() {
	c.stopOnce.Do(func() { close(c.stopping) })
}

// Dispatch 执行一次控制请求
func (c *Control) Dispatch(request map[string]any) (any, error) {
	action, _ := request["action"].(string)
	select {
	case <-c.stopping:
		if action != "status" {
			return nil, errors.New("宿主正在关闭")
		}
	default:
	}
	switch action {
	case "status":
		return c.supervisor.Statuses()
	case "open_webui":
		if c.webui == nil || !c.webui.Management() {
			return nil, errors.New("宿主 WebUI 未启用")
		}
		url, err := c.webui.OpenURL()
		if err != nil {
			return nil, err
		}
		return map[string]any{"url": url}, nil
	case "shutdown":
		c.Stop()
		return map[string]any{"status": "stopping"}, nil
	case "start", "stop", "restart":
		botID, _ := request["bot_id"].(string)
		if strings.TrimSpace(botID) == "" {
			return nil, errors.New("缺少 Bot ID")
		}
		switch action {
		case "start":
			return c.supervisor.Start(botID)
		case "stop":
			return c.supervisor.Stop(botID)
		default:
			return c.supervisor.Restart(botID)
		}
	default:
		return nil, fmt.Errorf("未知宿主操作: %s", action)
	}
}

func (c *Control) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return
	}
	var request map[string]any
	if err := json.Unmarshal(line, &request); err != nil || request == nil {
		writeControlResponse(conn, map[string]any{"ok": false, "error": "控制请求必须是 JSON 对象"})
		return
	}
	action, _ := request["action"].(string)
	timeout := controlTimeout
	if action == "start" || action == "restart" {
		timeout = controlActionTimeout
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	result, dispatchErr := c.Dispatch(request)
	response := map[string]any{"ok": dispatchErr == nil}
	if dispatchErr != nil {
		response["error"] = dispatchErr.Error()
	} else {
		response["result"] = result
	}
	writeControlResponse(conn, response)
}

func writeControlResponse(conn net.Conn, payload map[string]any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = conn.Write(append(encoded, '\n'))
}

// RunHost 持有工作区运行锁启动宿主，直到信号或控制通道请求关闭
func RunHost(ctx context.Context, workspace Workspace, confirmed map[string]struct{}) error {
	descriptorPath := filepath.Join(workspace.HostDirectory(), "control.json")
	supervisor := NewSupervisor(workspace)
	for botID := range confirmed {
		supervisor.upgradeConfirmed[botID] = struct{}{}
	}
	control := NewControl(supervisor)
	webui, err := NewWebUI(supervisor, workspace.Config, control.stopping)
	if err != nil {
		return err
	}
	control.webui = webui
	if endpoint, listener, endpointErr := allocateEndpoint("control"); endpointErr == nil {
		defer releaseEndpoint(listener)
		if server, listenErr := listenControl(endpoint); listenErr == nil {
			control.server = server
			defer server.Close()
			go func() {
				for {
					conn, acceptErr := server.Accept()
					if acceptErr != nil {
						return
					}
					go control.handle(conn)
				}
			}()
			_ = publishControlDescriptor(descriptorPath, endpoint)
			defer os.Remove(descriptorPath)
		}
	}

	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		select {
		case <-signals:
			control.Stop()
		case <-ctx.Done():
		}
	}()

	fmt.Printf("MaiBot 宿主已启动：%s\n", workspace.Root)
	if webui.Management() {
		if url, urlErr := webui.OpenURL(); urlErr == nil {
			fmt.Printf("WebUI：%s（登录链接一分钟内有效）\n", url)
		}
		if token := webui.TemporaryToken(); token != "" {
			fmt.Printf("WebUI 临时登录密码：%s\n", token)
		}
	}
	fmt.Println("可用 maigo host status/start/stop/restart 管理 Bot；Ctrl+C 关闭宿主。")

	go func() {
		for _, failure := range supervisor.StartDesired() {
			fmt.Fprintf(os.Stderr, "Bot 启动失败 %s\n", failure)
		}
	}()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- webui.ListenAndServe(runCtx)
	}()
	select {
	case <-control.stopping:
	case <-ctx.Done():
	case err := <-serveErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			cancel()
			_ = supervisor.Shutdown()
			return fmt.Errorf("宿主 WebUI 异常退出: %w", err)
		}
	}
	cancel()
	shutdownErr := supervisor.Shutdown()
	closeErr := webui.Close()
	return errors.Join(shutdownErr, closeErr)
}

func publishControlDescriptor(path, endpoint string) error {
	encoded, err := json.Marshal(map[string]any{"version": 1, "endpoint": endpoint})
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o777); err != nil {
		return err
	}
	temporary := filepath.Join(directory, "."+filepath.Base(path)+"."+fmt.Sprint(time.Now().UnixNano())+".tmp")
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

// ControlRequest 连接运行中的宿主执行一次控制请求
func ControlRequest(root, action, botID string) (any, error) {
	descriptorPath := filepath.Join(root, "host", "control.json")
	raw, err := os.ReadFile(descriptorPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.New("宿主尚未运行，请先执行 maigo run")
		}
		return nil, err
	}
	var descriptor struct {
		Version  int    `json:"version"`
		Endpoint string `json:"endpoint"`
	}
	if err := json.Unmarshal(raw, &descriptor); err != nil || descriptor.Version != 1 || descriptor.Endpoint == "" {
		return nil, errors.New("宿主连接信息格式无效")
	}
	conn, err := dialControl(descriptor.Endpoint)
	if err != nil {
		return nil, errors.New("无法连接宿主，连接记录可能已过期；请检查宿主是否仍在运行")
	}
	defer conn.Close()
	request := map[string]any{"action": action}
	if botID != "" {
		request["bot_id"] = botID
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	timeout := controlTimeout
	if action == "start" || action == "restart" {
		timeout = controlActionTimeout
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(append(encoded, '\n')); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var response struct {
		OK     bool            `json:"ok"`
		Error  string          `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		return nil, errors.New("宿主返回了无效响应")
	}
	if !response.OK {
		return nil, errors.New(response.Error)
	}
	var result any
	if len(response.Result) > 0 {
		_ = json.Unmarshal(response.Result, &result)
	}
	return result, nil
}

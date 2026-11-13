package desktoppet

// 桌宠子进程的行协议，对齐 src/desktop_pet/service.py 的 _run/_handle_event/_command
// 与 pet_window.py 的 pump_commands：stdout 每行一个事件，stdin 每行一条带 id 的指令

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"
)

const (
	// commandTimeout 普通指令回执超时，对齐 COMMAND_TIMEOUT
	commandTimeout = 3 * time.Second
	// skinTimeout 皮肤安装回执超时，对齐 _command 的 skin 分支
	skinTimeout = 15 * time.Second
	// readyTimeout 首条 ready 事件的等待上限，对齐 _run 的 30 秒
	readyTimeout = 30 * time.Second
	// maxEventBytes 单行事件上限，皮肤回执携带最多 32 MiB 图片的 base64 资源
	maxEventBytes = 64 << 20
)

var (
	// errIPCNotReady 未就绪或正在关闭，对齐 _command 的前置守卫文案
	errIPCNotReady = errors.New("桌宠窗口尚未就绪，请检查 desktop_pet.enabled 和 desktop_pet.status")
	// errIPCClosed 窗口退出导致的回执失败，对齐 ConnectionError("桌宠窗口已退出")
	errIPCClosed = errors.New("桌宠窗口已退出")
)

type callResult struct {
	value any
	err   error
}

func newCommandID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("生成桌宠指令 ID 失败: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

// call 发送带 id 的指令并等待回执，超时与错误文案对齐 Python 侧
func (s *Service) call(ctx context.Context, command map[string]any) (any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	ready, closing, stdin := s.ready, s.closing, s.stdin
	if !ready || closing || stdin == nil {
		s.mu.Unlock()
		return nil, errIPCNotReady
	}
	id, err := newCommandID()
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	pending := make(chan callResult, 1)
	s.pending[id] = pending
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()

	kind, _ := command["type"].(string)
	timeout := commandTimeout
	if kind == "skin" {
		timeout = skinTimeout
	}

	payload := make(map[string]any, len(command)+1)
	for key, value := range command {
		payload[key] = value
	}
	payload["id"] = id
	encoded, err := sonic.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("编码桌宠指令失败: %w", err)
	}
	encoded = append(encoded, '\n')

	s.writeMu.Lock()
	_, writeErr := stdin.Write(encoded)
	s.writeMu.Unlock()
	if writeErr != nil {
		return nil, fmt.Errorf("向桌宠窗口写入指令失败: %w", writeErr)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-pending:
		if result.err != nil {
			return nil, result.err
		}
		s.applyVisibility(ctx, kind)
		return result.value, nil
	case <-timer.C:
		return nil, fmt.Errorf("桌宠窗口未在 %s 内回执", timeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// applyVisibility show/hide 成功后同步可见状态并触发 visibility_changed
func (s *Service) applyVisibility(ctx context.Context, kind string) {
	if kind != "show" && kind != "hide" {
		return
	}
	visible := kind == "show"
	s.mu.Lock()
	changed := s.visible != visible
	s.visible = visible
	s.mu.Unlock()
	if changed {
		s.emitHook(ctx, "visibility_changed", map[string]any{"visible": visible})
	}
}

// readEvents 逐行读取窗口事件，解析失败或未知事件按 Python 侧语义终止窗口
func (s *Service) readEvents(ctx context.Context, stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), maxEventBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event map[string]any
		if err := sonic.UnmarshalString(line, &event); err != nil {
			s.fail(fmt.Errorf("解析桌宠事件失败: %w", err))
			return
		}
		normalized, _ := normalizeJSON(event).(map[string]any)
		if err := s.handleEvent(ctx, normalized); err != nil {
			s.fail(err)
			return
		}
	}
	if err := scanner.Err(); err != nil {
		s.fail(fmt.Errorf("读取桌宠事件失败: %w", err))
	}
	// stdout 关闭表示窗口进程已退出，收尾交给 watchProcess
}

func (s *Service) handleEvent(ctx context.Context, event map[string]any) error {
	kind, _ := event["type"].(string)
	switch kind {
	case "ready":
		s.markReady(ctx)
		return nil
	case "buttons_refresh":
		s.buttonsChanged()
		return nil
	case "button_click":
		pluginID, _ := event["plugin_id"].(string)
		buttonID, _ := event["button_id"].(string)
		go s.clickButton(ctx, pluginID, buttonID)
		return nil
	case "clicked":
		s.emitHook(ctx, "clicked", nil)
		return nil
	case "drag_started":
		s.emitHook(ctx, "drag_started", map[string]any{
			"gesture_id": event["gesture_id"],
			"offset_x":   event["offset_x"],
			"offset_y":   event["offset_y"],
			"scale":      event["scale"],
		})
		return nil
	case "drag_ended":
		s.emitHook(ctx, "drag_ended", map[string]any{"gesture_id": event["gesture_id"]})
		return nil
	case "chat":
		requestID, _ := event["request_id"].(string)
		s.handleChat(ctx, requestID)
		return nil
	case "ack":
		s.deliverAck(event)
		return nil
	default:
		return fmt.Errorf("未知的桌宠事件: %s", kind)
	}
}

// markReady 对齐 ready 分支：置 ready/visible 并应用配置皮肤、触发 ready 钩子
func (s *Service) markReady(ctx context.Context) {
	s.mu.Lock()
	s.ready = true
	s.visible = true
	skinID := s.skinID
	s.mu.Unlock()

	zap.L().Info("内置桌宠已出现在桌面上")
	go func() {
		if err := s.ApplySkin(ctx, skinID); err != nil {
			s.setError(err)
			zap.L().Error("桌宠皮肤应用失败", zap.Error(err))
		}
	}()
	s.emitHook(ctx, "ready", nil)
	s.buttonsChanged()
}

// deliverAck 把回执交给等待方；超时或已取消的迟到回执直接丢弃
func (s *Service) deliverAck(event map[string]any) {
	id, _ := event["id"].(string)
	s.mu.Lock()
	pending := s.pending[id]
	s.mu.Unlock()
	if pending == nil {
		return
	}
	if message, ok := event["error"].(string); ok && message != "" {
		pending <- callResult{err: errors.New(message)}
		return
	}
	pending <- callResult{value: event["result"]}
}

// normalizeJSON 把 sonic 解出的浮点整数还原为 int64，与 Python json.loads 的整数语义一致
func normalizeJSON(value any) any {
	switch typed := value.(type) {
	case float64:
		if typed == math.Trunc(typed) && math.Abs(typed) < 1<<53 {
			return int64(typed)
		}
		return typed
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = normalizeJSON(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = normalizeJSON(item)
		}
		return out
	default:
		return value
	}
}

// fail 记录错误并结束窗口进程，对齐 _run 的 except 分支
func (s *Service) fail(err error) {
	s.setError(err)
	zap.L().Error("桌宠窗口运行失败", zap.Error(err))
	s.terminate()
}

func (s *Service) terminate() {
	s.mu.RLock()
	cmd := s.cmd
	s.mu.RUnlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (s *Service) setError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	s.errMsg = err.Error()
	s.mu.Unlock()
}

// drainPetStderr 收集窗口 stderr：平时记 debug，异常退出时整体输出尾部若干行
func (s *Service) drainPetStderr(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	tail := make([]string, 0, stderrTailLines)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if len(tail) == stderrTailLines {
			tail = tail[1:]
		}
		tail = append(tail, line)
		zap.L().Debug("桌宠窗口: " + line)
	}
	s.mu.Lock()
	s.stderrTail = tail
	s.mu.Unlock()
}

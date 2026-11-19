package qq_official

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

const (
	qrBindBaseURL   = "https://q.qq.com/lite"
	qrConnectURL    = "https://q.qq.com/qqbot/openclaw/connect.html"
	qrPollInterval  = 2 * time.Second
	qrTaskTTL       = 5 * time.Minute
	qrHTTPTimeout   = 10 * time.Second
)

// QRLoginSnapshot 公开快照，不含 AES 密钥与密文
type QRLoginSnapshot struct {
	State     string
	Message   string
	Image     string
	AppID     string
	ExpiresAt *float64
}

// CredentialApplier 扫码成功后保存凭据并热启连接
type CredentialApplier func(ctx context.Context, appID, secret string) error

// QRLogin 对齐 plugins/built_in/qq_official_adapter/qr_login.py
type QRLogin struct {
	apply CredentialApplier

	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	saving   bool
	state    string
	message  string
	image    string
	appID    string
	deadline time.Time
}

// NewQRLogin 构造扫码登录状态机
func NewQRLogin(apply CredentialApplier) *QRLogin {
	return &QRLogin{
		apply:   apply,
		state:   "idle",
		message: "点击扫码登录，使用手机 QQ 授权机器人。",
	}
}

// Snapshot 返回公开状态
func (q *QRLogin) Snapshot() QRLoginSnapshot {
	if q == nil {
		return QRLoginSnapshot{State: "idle", Message: "点击扫码登录，使用手机 QQ 授权机器人。"}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	var expires *float64
	if q.image != "" && !q.deadline.IsZero() {
		remaining := time.Until(q.deadline).Seconds()
		if remaining < 0 {
			remaining = 0
		}
		v := float64(time.Now().Unix()) + remaining
		expires = &v
	}
	return QRLoginSnapshot{
		State:     q.state,
		Message:   q.message,
		Image:     q.image,
		AppID:     q.appID,
		ExpiresAt: expires,
	}
}

// Start 发起扫码；若已有任务则取消后重建
func (q *QRLogin) Start(ctx context.Context) (QRLoginSnapshot, error) {
	if q == nil {
		return QRLoginSnapshot{}, fmt.Errorf("QRLogin 未初始化")
	}
	q.mu.Lock()
	q.cancelLocked()
	q.state = "creating"
	q.message = "正在生成二维码…"
	q.image = ""
	q.appID = ""
	q.deadline = time.Time{}
	runCtx, cancel := context.WithCancel(context.Background())
	q.cancel = cancel
	done := make(chan struct{})
	q.done = done
	q.mu.Unlock()

	go func() {
		defer close(done)
		q.run(runCtx)
	}()
	_ = ctx
	return q.Snapshot(), nil
}

// Cancel 取消当前扫码；saving 阶段等待写盘完成
func (q *QRLogin) Cancel(ctx context.Context) error {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	done := q.done
	saving := q.saving
	cancel := q.cancel
	if cancel != nil && !saving {
		cancel()
	}
	q.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	q.mu.Lock()
	if q.state != "success" && q.state != "error" && q.state != "expired" {
		q.state = "cancelled"
		q.message = "已取消扫码登录。"
	}
	q.image = ""
	q.cancel = nil
	q.done = nil
	q.mu.Unlock()
	return nil
}

func (q *QRLogin) cancelLocked() {
	if q.cancel != nil && !q.saving {
		q.cancel()
	}
	done := q.done
	q.mu.Unlock()
	if done != nil {
		<-done
	}
	q.mu.Lock()
	q.cancel = nil
	q.done = nil
	q.image = ""
}

func (q *QRLogin) run(ctx context.Context) {
	stage := "创建绑定任务"
	defer func() {
		q.mu.Lock()
		q.image = ""
		q.saving = false
		q.mu.Unlock()
	}()
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		q.fail(stage, err)
		return
	}
	client := &http.Client{Timeout: qrHTTPTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	data, err := q.post(ctx, client, "create_bind_task", map[string]any{
		"key": base64.StdEncoding.EncodeToString(key),
	})
	if err != nil {
		q.fail(stage, err)
		return
	}
	taskID, _ := data["task_id"].(string)
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		q.fail(stage, fmt.Errorf("QQ 未返回绑定任务 ID"))
		return
	}
	connect := qrConnectURL + "?" + url.Values{
		"task_id": {taskID},
		"source":  {"maibot"},
		"_wv":     {"2"},
	}.Encode()
	image, err := renderQRDataURL(connect)
	if err != nil {
		q.fail(stage, err)
		return
	}
	deadline := time.Now().Add(qrTaskTTL)
	q.mu.Lock()
	q.image = image
	q.deadline = deadline
	q.state = "waiting"
	q.message = "请使用手机 QQ 扫码并完成授权。"
	q.mu.Unlock()

	stage = "查询扫码结果"
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		default:
		}
		result, err := q.post(ctx, client, "poll_bind_result", map[string]any{"task_id": taskID})
		if err != nil {
			q.fail(stage, err)
			return
		}
		status := asJSONInt(result["status"])
		switch status {
		case 2:
			appID := strings.TrimSpace(fmt.Sprint(result["bot_appid"]))
			encrypted, _ := result["bot_encrypt_secret"].(string)
			if !isASCIIDigits(appID) || strings.TrimSpace(encrypted) == "" {
				q.fail(stage, fmt.Errorf("QQ 返回的机器人凭据不完整"))
				return
			}
			stage = "解密授权凭据"
			secret, err := decryptSecret(encrypted, key)
			if err != nil {
				q.fail(stage, err)
				return
			}
			q.mu.Lock()
			q.saving = true
			q.state = "saving"
			q.message = "授权成功，正在保存并连接…"
			q.image = ""
			q.mu.Unlock()
			stage = "保存凭据并启动连接"
			if q.apply == nil {
				q.fail(stage, fmt.Errorf("未配置凭据应用回调"))
				return
			}
			if err := q.apply(ctx, appID, secret); err != nil {
				q.fail(stage, err)
				return
			}
			q.mu.Lock()
			q.appID = appID
			q.state = "success"
			q.message = "凭据已保存，已启动连接。"
			q.saving = false
			q.mu.Unlock()
			return
		case 3:
			q.mu.Lock()
			q.state = "expired"
			q.message = "二维码已过期，请重新扫码。"
			q.mu.Unlock()
			return
		case 0, 1:
		default:
			q.fail(stage, fmt.Errorf("QQ 返回了未知绑定状态"))
			return
		}
		timer := time.NewTimer(qrPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	q.mu.Lock()
	q.state = "expired"
	q.message = "二维码已过期，请重新扫码。"
	q.mu.Unlock()
}

func (q *QRLogin) fail(stage string, err error) {
	detail := "Error"
	if err != nil {
		msg := err.Error()
		if strings.HasPrefix(msg, "QQ 扫码接口") {
			detail = msg
		} else if _, ok := err.(interface{ Timeout() bool }); ok {
			detail = "Timeout"
		} else {
			detail = fmt.Sprintf("%T", err)
			if detail == "*errors.errorString" || detail == "*fmt.wrapError" {
				detail = "Error"
			}
		}
	}
	q.mu.Lock()
	q.state = "error"
	q.message = fmt.Sprintf("%s失败（%s）。请检查网络或配置文件写入权限后重试。", stage, detail)
	q.saving = false
	q.mu.Unlock()
}

func (q *QRLogin) post(ctx context.Context, client *http.Client, endpoint string, payload map[string]any) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, qrBindBaseURL+"/"+endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("QQ 扫码接口 HTTP %d", resp.StatusCode)
	}
	var envelope struct {
		Retcode int            `json:"retcode"`
		Data    map[string]any `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("QQ 扫码接口返回格式无效")
	}
	if envelope.Retcode != 0 || envelope.Data == nil {
		return nil, fmt.Errorf("QQ 扫码接口返回失败")
	}
	return envelope.Data, nil
}

func renderQRDataURL(content string) (string, error) {
	png, err := qrcode.Encode(content, qrcode.Medium, 256)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

func decryptSecret(encrypted string, key []byte) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return "", fmt.Errorf("扫码凭据密文格式无效")
	}
	if len(key) != 32 || len(data) <= 28 {
		return "", fmt.Errorf("扫码凭据密文格式无效")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	iv := data[:12]
	plaintext, err := aead.Open(nil, iv, data[12:], nil)
	if err != nil {
		return "", fmt.Errorf("扫码凭据密文格式无效")
	}
	secret := strings.TrimSpace(string(plaintext))
	if secret == "" {
		return "", fmt.Errorf("扫码返回了空凭据")
	}
	return secret, nil
}

// SaveCredentials 保留现有配置与注释，原子写入 app_id / app_secret 并启用插件
func SaveCredentials(path, appID, secret string) error {
	path = filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	text := string(raw)
	text = upsertTOMLString(text, "qq_official", "app_id", appID)
	text = upsertTOMLString(text, "qq_official", "app_secret", secret)
	text = upsertTOMLBool(text, "plugin", "enabled", true)
	fd, err := os.CreateTemp(filepath.Dir(path), ".qq-login-*.tmp")
	if err != nil {
		return err
	}
	tmp := fd.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := fd.WriteString(text); err != nil {
		_ = fd.Close()
		return err
	}
	if err := fd.Sync(); err != nil {
		_ = fd.Close()
		return err
	}
	if err := fd.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

var (
	sectionHeaderPattern = regexp.MustCompile(`^\s*\[([^\]]+)\]\s*(?:#.*)?$`)
	assignmentPattern    = regexp.MustCompile(`^(\s*)([A-Za-z0-9_]+)(\s*=\s*)(.*?)(\s*(?:#.*)?)\s*$`)
)

func upsertTOMLString(raw, section, key, value string) string {
	return upsertTOMLValue(raw, section, key, quoteTOMLString(value))
}

func upsertTOMLBool(raw, section, key string, value bool) string {
	text := "false"
	if value {
		text = "true"
	}
	return upsertTOMLValue(raw, section, key, text)
}

func upsertTOMLValue(raw, section, key, encoded string) string {
	newline := "\n"
	if strings.Contains(raw, "\r\n") {
		newline = "\r\n"
	}
	lines := strings.Split(raw, "\n")
	header := -1
	for i, line := range lines {
		m := sectionHeaderPattern.FindStringSubmatch(strings.TrimSuffix(line, "\r"))
		if m != nil && strings.TrimSpace(m[1]) == section {
			header = i
			break
		}
	}
	if header < 0 {
		if raw != "" && !strings.HasSuffix(raw, newline) {
			raw += newline
		}
		if raw != "" && !strings.HasSuffix(raw, newline+newline) {
			raw += newline
		}
		return raw + "[" + section + "]" + newline + key + " = " + encoded + newline
	}
	end := len(lines)
	for i := header + 1; i < len(lines); i++ {
		if sectionHeaderPattern.MatchString(strings.TrimSuffix(lines[i], "\r")) {
			end = i
			break
		}
	}
	for i := header + 1; i < end; i++ {
		line := strings.TrimSuffix(lines[i], "\r")
		m := assignmentPattern.FindStringSubmatch(line)
		if m == nil || m[2] != key {
			continue
		}
		updated := m[1] + m[2] + m[3] + encoded + m[5]
		if strings.HasSuffix(lines[i], "\r") {
			updated += "\r"
		}
		lines[i] = updated
		return strings.Join(lines, "\n")
	}
	insertAt := end
	cr := ""
	if insertAt > header+1 && strings.TrimSpace(lines[insertAt-1]) == "" {
		insertAt--
	}
	if insertAt < len(lines) && strings.HasSuffix(lines[insertAt], "\r") {
		cr = "\r"
	}
	lines = append(lines[:insertAt], append([]string{key + " = " + encoded + cr}, lines[insertAt:]...)...)
	return strings.Join(lines, "\n")
}

func quoteTOMLString(v string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range v {
		switch r {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func isASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func asJSONInt(v any) int {
	switch typed := v.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		n, _ := typed.Int64()
		return int(n)
	default:
		return -1
	}
}

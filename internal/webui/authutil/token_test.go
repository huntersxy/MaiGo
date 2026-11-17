package authutil

import (
	"testing"

	"MaiGo/internal/host"
)

func TestHostManagedBypassesBotToken(t *testing.T) {
	t.Setenv(host.RuntimeContextEnv, `{"bot_id":"0123456789abcdef0123456789abcdef","http_endpoint":"/tmp/x.sock","public_base_url":"http://127.0.0.1","status_path":"/tmp/status.json"}`)
	if !HostManaged() {
		t.Fatal("运行契约存在时应由宿主接管认证")
	}
	if !ValidSessionToken(nil, "") {
		t.Fatal("宿主接管的 Worker 不应因缺少 Bot Cookie 拒绝请求")
	}
	if session := HandshakeSession(nil, nil); session != "host" {
		t.Fatalf("WebSocket 握手会话 = %q", session)
	}
}

package snowluma

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestDialOnceReadsBeforeOnOpen(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	host, portStr, _ := strings.Cut(ln.Addr().String(), ":")
	port, _ := strconv.Atoi(portStr)

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
			if err != nil {
				return
			}
			defer conn.Close(websocket.StatusNormalClosure, "done")
			for {
				_, data, err := conn.Read(r.Context())
				if err != nil {
					return
				}
				var payload map[string]any
				if err := json.Unmarshal(data, &payload); err != nil {
					continue
				}
				if asString(payload["action"]) != "get_login_info" {
					continue
				}
				resp, _ := json.Marshal(map[string]any{
					"status":  "ok",
					"retcode": 0,
					"data":    map[string]any{"user_id": "12345"},
					"echo":    payload["echo"],
				})
				_ = conn.Write(r.Context(), websocket.MessageText, resp)
			}
		}))
	}()

	transport := NewTransport(nil, nil, nil)
	transport.Configure(ClientConfig{
		Server:            host,
		Port:              port,
		HeartbeatInterval: 5,
		ActionTimeoutSec:  3,
		ReconnectDelaySec: 1,
	}, NewProfileState(OneBotV11Profile))

	var (
		gotUser   string
		onOpenErr error
		wg        sync.WaitGroup
	)
	wg.Add(1)
	transport.onOpen = func(ctx context.Context) error {
		defer wg.Done()
		resp, err := transport.CallAction(ctx, "get_login_info", nil)
		if err != nil {
			onOpenErr = err
			return err
		}
		data := asMap(resp["data"])
		gotUser = asString(data["user_id"])
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := transport.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = transport.Stop(context.Background()) }()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("onOpen did not finish")
	}
	if onOpenErr != nil {
		t.Fatalf("onOpen: %v", onOpenErr)
	}
	if gotUser != "12345" {
		t.Fatalf("user_id=%q", gotUser)
	}
	_ = ln.Close()
	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
	}
}

package pythoncompat

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnsureRunningNotConfigured(t *testing.T) {
	client := NewClient(Config{})
	release, err := client.ensureRunning(context.Background())
	if err != nil {
		t.Fatalf("未配置时 ensureRunning 应成功: %v", err)
	}
	release()
	if _, err := client.CreateTypoSentences(context.Background(), []string{"a"}, TypoConfig{}, nil); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("期望 ErrNotConfigured，得到 %v", err)
	}
}

func TestEnsureRunningSpawnsOnce(t *testing.T) {
	python, err := exec.LookPath("python")
	if err != nil {
		python, err = exec.LookPath("python3")
	}
	if err != nil {
		t.Skip("未找到 python，跳过按需拉起测试")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fake_sidecar.py")
	counterPath := filepath.Join(dir, "starts.txt")
	if err := os.WriteFile(script, []byte(fakeSidecarSource), 0o644); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	t.Setenv("MAIBOT_COMPAT_SIDECAR_PYTHON", python)
	t.Setenv("MAIBOT_COMPAT_SIDECAR_SCRIPT", script)
	t.Setenv("MAIBOT_COMPAT_SIDECAR_CWD", dir)
	t.Setenv("MAIBOT_COMPAT_SIDECAR_IDLE", "0")
	t.Setenv("MAIBOT_SOURCE_ROOT", dir)
	t.Setenv("MAIBOT_INSTANCE_DIR", dir)
	t.Setenv("FAKE_SIDECAR_STARTS", counterPath)
	t.Cleanup(stopManagedSidecar)

	client := NewClient(Config{
		BaseURL: "http://127.0.0.1:" + strconv.Itoa(port),
		Timeout: 10 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	errCh := make(chan error, 8)
	var started atomic.Int32
	for i := 0; i < 8; i++ {
		go func() {
			release, err := client.ensureRunning(ctx)
			if err != nil {
				errCh <- err
				return
			}
			defer release()
			started.Add(1)
			errCh <- nil
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("并发 ensureRunning 失败: %v", err)
		}
	}
	if started.Load() != 8 {
		t.Fatalf("并发完成数异常: %d", started.Load())
	}
	raw, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatalf("读取启动计数失败: %v", err)
	}
	if string(raw) != "1" {
		t.Fatalf("期望只启动一次，实际 %q", raw)
	}

	result, err := client.RunKMeans(ctx, KMeansRequest{
		Rows: 2, Cols: 2, Vectors: []float32{1, 0, 0, 1}, ClusterCount: 1,
	})
	if err != nil {
		t.Fatalf("RunKMeans 失败: %v", err)
	}
	if len(result.Labels) != 2 {
		t.Fatalf("标签异常: %+v", result)
	}
}

const fakeSidecarSource = `
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

port = int(os.environ.get("MAIBOT_COMPAT_SIDECAR_PORT", "0") or "0")
host = os.environ.get("MAIBOT_COMPAT_SIDECAR_HOST", "127.0.0.1")
counter = os.environ.get("FAKE_SIDECAR_STARTS", "")
if counter:
    with open(counter, "w", encoding="utf-8") as f:
        f.write("1")

class H(BaseHTTPRequestHandler):
    def log_message(self, *args):
        return

    def _json(self, code, payload):
        body = json.dumps(payload).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        self._json(200, {"status": "ok", "routes": ["/kmeans", "/typo"]})

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0") or "0")
        _ = self.rfile.read(length)
        if self.path.rstrip("/") == "/kmeans":
            self._json(200, {
                "labels": [0, 0],
                "centers": [0.5, 0.5],
                "centers_rows": 1,
                "centers_cols": 2,
            })
            return
        if self.path.rstrip("/") == "/typo":
            self._json(200, {"sentences": [{"text": "x", "correction": None}]})
            return
        self._json(404, {"detail": "not found"})

if __name__ == "__main__":
    httpd = ThreadingHTTPServer((host, port), H)
    try:
        httpd.serve_forever()
    finally:
        httpd.server_close()
`

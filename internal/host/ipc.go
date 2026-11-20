package host

// 本机进程间通道地址分配，对齐 src/common/ipc.py。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// endpointListener 分配出的监听地址；Unix 下需要在使用后删除 socket 文件
type endpointListener struct {
	Address string
	path    string
}

func allocateEndpoint(purpose string) (string, *endpointListener, error) {
	name := "maigo-" + purpose + "-" + randomEndpointSuffix()
	if runtime.GOOS == "windows" {
		address := `\\.\pipe\` + name
		return address, &endpointListener{Address: address}, nil
	}
	directory := filepath.Join(os.TempDir(), fmt.Sprintf("maigo-%d", os.Getuid()))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return "", nil, fmt.Errorf("进程间通信目录不安全: %s", directory)
	}
	path := filepath.Join(directory, name+".sock")
	return path, &endpointListener{Address: path, path: path}, nil
}

func releaseEndpoint(listener *endpointListener) {
	if listener == nil || listener.path == "" {
		return
	}
	_ = os.Remove(listener.path)
}

func randomEndpointSuffix() string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return strings.Repeat("0", 24)
	}
	return hex.EncodeToString(buffer)
}

// dialEndpointTimeout 对齐 Python ClientTimeout(sock_connect=5)
const dialEndpointTimeout = 5 * time.Second

// dialEndpoint 连接进程间通道，供代理转发使用；尊重 ctx，默认最多等 5 秒。
func dialEndpoint(ctx context.Context, address string) (net.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	dialCtx, cancel := context.WithTimeout(ctx, dialEndpointTimeout)
	defer cancel()
	if runtime.GOOS == "windows" && strings.HasPrefix(address, `\\.\pipe\`) {
		return openNamedPipe(dialCtx, address)
	}
	var dialer net.Dialer
	return dialer.DialContext(dialCtx, "unix", address)
}

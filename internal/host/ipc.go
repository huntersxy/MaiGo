package host

// 本机进程间通道地址分配，对齐 src/common/ipc.py。

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// dialEndpoint 连接进程间通道，供代理转发使用
func dialEndpoint(address string) (net.Conn, error) {
	if runtime.GOOS == "windows" && strings.HasPrefix(address, `\\.\pipe\`) {
		return openNamedPipe(address)
	}
	return net.Dial("unix", address)
}

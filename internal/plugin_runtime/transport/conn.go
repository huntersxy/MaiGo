package transport

import (
	"net"
	"sync"
)

// FramedConn 在 net.Conn 上提供分帧读写（写操作串行化，保证帧完整）
type FramedConn struct {
	net.Conn
	writeMu sync.Mutex
}

func NewFramedConn(conn net.Conn) *FramedConn {
	return &FramedConn{Conn: conn}
}

func (c *FramedConn) SendFrame(payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return WriteFrame(c.Conn, payload)
}

func (c *FramedConn) RecvFrame() ([]byte, error) {
	return ReadFrame(c.Conn)
}

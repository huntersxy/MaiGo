package transport

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	// FrameHeaderSize 长度前缀字节数
	FrameHeaderSize = 4
	// MaxFrameSize 单帧 payload 上限
	MaxFrameSize = 16 * 1024 * 1024
)

// ReadFrame 从连接读取一帧（大端 uint32 长度 + payload）
func ReadFrame(r io.Reader) ([]byte, error) {
	var header [FrameHeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header[:])
	if length > MaxFrameSize {
		return nil, fmt.Errorf("帧大小 %d 超过最大限制 %d", length, MaxFrameSize)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// WriteFrame 向连接写入一帧
func WriteFrame(w io.Writer, payload []byte) error {
	n := len(payload)
	if n > MaxFrameSize {
		return fmt.Errorf("帧大小 %d 超过最大限制 %d", n, MaxFrameSize)
	}
	var header [FrameHeaderSize]byte
	binary.BigEndian.PutUint32(header[:], uint32(n))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

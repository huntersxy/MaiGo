package runtime

import (
	"crypto/rand"
	"encoding/hex"
)

func randomHex(nBytes int) string {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		for i := range buf {
			buf[i] = byte(i + 1)
		}
	}
	return hex.EncodeToString(buf)
}

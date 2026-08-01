package challenge

import (
	crand "crypto/rand"
)

func NewID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	const n = 12
	buf := make([]byte, n)
	_, _ = crand.Read(buf)
	for i := range buf {
		buf[i] = chars[int(buf[i])%len(chars)]
	}
	return "chal-" + string(buf)
}

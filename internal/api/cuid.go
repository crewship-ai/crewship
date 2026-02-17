package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"
)

var counter atomic.Uint64

func generateCUID() string {
	ts := time.Now().UnixMilli()
	c := counter.Add(1)
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("c%s%04x%s", encodeBase36(ts), c%65536, hex.EncodeToString(b)[:8])
}

func encodeBase36(n int64) string {
	const chars = "0123456789abcdefghijklmnopqrstuvwxyz"
	if n == 0 {
		return "0"
	}
	var result []byte
	for n > 0 {
		result = append([]byte{chars[n%36]}, result...)
		n /= 36
	}
	return string(result)
}

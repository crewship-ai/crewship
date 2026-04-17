package api

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"sync/atomic"
	"time"
)

var counter atomic.Uint64

func generateCUID() string {
	ts := time.Now().UnixMilli()
	c := counter.Add(1)
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		b[0] = byte(c >> 24)
		b[1] = byte(c >> 16)
		b[2] = byte(ts >> 8)
		b[3] = byte(ts)
	}

	// Layout: "c" + base36(ts) + zero-padded hex counter (%04x) + hex(4 rand bytes).
	var buf [32]byte
	out := append(buf[:0], 'c')
	out = strconv.AppendInt(out, ts, 36)
	tail := c % 65536
	// Manual 4-char lowercase-hex for the counter — 4x cheaper than fmt.Sprintf("%04x").
	const hexdigits = "0123456789abcdef"
	out = append(out,
		hexdigits[(tail>>12)&0xF],
		hexdigits[(tail>>8)&0xF],
		hexdigits[(tail>>4)&0xF],
		hexdigits[tail&0xF],
	)
	out = hex.AppendEncode(out, b)
	return string(out)
}

// encodeBase36 remains a thin wrapper around strconv.FormatInt so existing
// tests keep exercising the public contract.
func encodeBase36(n int64) string {
	return strconv.FormatInt(n, 36)
}

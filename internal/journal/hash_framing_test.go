package journal

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// This vector was independently calculated from UTF-8 byte lengths and
// big-endian uint64 framing, including an empty field and an embedded NUL.
func TestFramedHMACStableWireBytes(t *testing.T) {
	h := hmac.New(sha256.New, []byte("review-public-fixture-key"))
	for _, s := range []string{"", "český", "<script>alert(1)</script>", "a\x00b"} {
		writeFramed(h, s)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != "25769623b4391655600dd3c96378b866c55ad47508099e46a0e6cdd472da79d0" {
		t.Fatalf("changed framing: %s", got)
	}
}

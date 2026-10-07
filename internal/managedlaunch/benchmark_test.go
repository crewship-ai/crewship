package managedlaunch

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func BenchmarkManagedDescriptor(b *testing.B) {
	raw, _ := json.Marshal(fuzzDescriptor())
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := DecodeDescriptor(encoded); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkManagedCapture(b *testing.B) {
	for _, size := range []int{1 << 20, 32 << 20} {
		b.Run(fmt.Sprintf("%dMiB", size>>20), func(b *testing.B) {
			raw := make([]byte, size)
			copy(raw, nativeFixture())
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := Capture("/opt/native/codex", raw); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkManagedEnvironment(b *testing.B) {
	input := []string{"HOME=/home/agent", "http_proxy=http://fixture", "LD_PRELOAD=discard", "DATA=" + strings.Repeat("x", 1024)}
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := Environment(input); err != nil {
			b.Fatal(err)
		}
	}
}

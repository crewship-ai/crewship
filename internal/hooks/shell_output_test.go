package hooks

import (
	"io"
	"strings"
	"testing"
)

func TestShellOutputPreservesPresentationWithBoundedRetention(t *testing.T) {
	for _, size := range []int{0, 255, 256, 257, 4095, 4096, 4097, 16384} {
		input := strings.Repeat("x", size)
		for _, chunk := range []int{1, 23, 4096, 8192} {
			var out shellOutput
			for pos := 0; pos < len(input); pos += chunk {
				end := pos + chunk
				if end > len(input) {
					end = len(input)
				}
				n, err := out.Write([]byte(input[pos:end]))
				if err != nil || n != end-pos {
					t.Fatalf("drain: %d %v", n, err)
				}
				if out.Len() > maxShellCapturedBytes {
					t.Fatal("output retained beyond cap")
				}
			}
			for _, limit := range []int{256, 4096} {
				if got, want := truncate(out.String(), limit), truncate(input, limit); got != want {
					t.Fatalf("size=%d chunk=%d limit=%d: presentation changed", size, chunk, limit)
				}
			}
		}
	}
}

func TestShellOutputIOCopyCannotBypassCaptureLimit(t *testing.T) {
	var out shellOutput
	// exec's pipe copy uses io.Copy; prevent optional fast-path interfaces
	// from bypassing the writer limit.
	source := struct{ io.Reader }{strings.NewReader(strings.Repeat("x", 1024*1024))}
	n, err := io.Copy(&out, source)
	if err != nil || n != 1024*1024 || out.Len() != maxShellCapturedBytes {
		t.Fatalf("io.Copy bypassed capture: n=%d retained=%d err=%v", n, out.Len(), err)
	}
}

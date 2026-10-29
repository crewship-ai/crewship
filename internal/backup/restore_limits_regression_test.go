package backup

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"github.com/klauspost/compress/zstd"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Proposed bounded-memory contract: metadata inspection must not retain a
// payload-sized buffer. Both fixtures exceed the encoder window so its required
// initial history allocation is equal. Two valid bundles isolate payload-dependent
// allocations from the decoder's fixed setup. This is not a peak-RSS test.
func TestAssessmentInspectPayloadIndependentAllocations(t *testing.T) {
	dir := t.TempDir()
	makeBundle := func(name string, n int) string {
		t.Helper()
		m := &Manifest{Scope: ScopeWorkspace, CompatibleTargets: []Target{TargetAnyInstance}, CreatedAt: nonZeroTime(), CreatedBy: Actor{UserID: "synthetic"}, Encryption: Encryption{Enabled: false}}
		payload := make([]byte, n)
		m.Checksums.PayloadSHA256 = fmt.Sprintf("sha256:%x", sha256.Sum256(payload))
		var compressed bytes.Buffer
		if err := WriteBundleStream(&compressed, m, bytes.NewReader(payload), int64(n)); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, compressed.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	small := makeBundle("small.tar.zst", 8<<20)
	large := makeBundle("large.tar.zst", 32<<20)
	windows := make([]uint64, 0, 2)
	for _, path := range []string{small, large} {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		prefix := make([]byte, 64)
		n, err := f.Read(prefix)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		var header zstd.Header
		if err := header.Decode(prefix[:n]); err != nil {
			t.Fatal(err)
		}
		windows = append(windows, header.WindowSize)
	}
	if windows[0] == 0 || windows[0] != windows[1] {
		t.Fatalf("fixtures need equal nonzero history windows: %v", windows)
	}
	t.Logf("zstd history windows: %v", windows)
	measure := func(p string) uint64 {
		t.Helper()
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		m, err := Inspect(t.Context(), p)
		runtime.ReadMemStats(&after)
		if err != nil || m == nil {
			t.Fatalf("Inspect: %v", err)
		}
		return after.TotalAlloc - before.TotalAlloc
	}
	smallAlloc := measure(small)
	largeAlloc := measure(large)
	t.Logf("inspect allocations small=%d large=%d; payload sizes 8MiB/32MiB (same encoder window)", smallAlloc, largeAlloc)
	if largeAlloc > smallAlloc+(4<<20) {
		t.Fatalf("metadata inspection allocations grow with payload: small=%d large=%d; proposed max growth 4MiB", smallAlloc, largeAlloc)
	}
}

//go:build linux

package managedlaunch

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type ownedCaptureZeroReader struct{}

func (ownedCaptureZeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// Run explicitly in a fresh test process with CREWSHIP_CAPTURE_RSS=file|archive
// and CREWSHIP_CAPTURE_RSS_BYTES. VmHWM is process RSS including the test runtime,
// not allocated bytes, and is deliberately not a timing-sensitive assertion.
// With neither profile variable present, only a tiny functional smoke runs;
// it validates native capture and VmHWM without reporting performance metrics.
func TestManagedStreamingRSSMeasurement(t *testing.T) {
	mode, modeSet := os.LookupEnv("CREWSHIP_CAPTURE_RSS")
	sizeText, sizeSet := os.LookupEnv("CREWSHIP_CAPTURE_RSS_BYTES")
	smoke := !modeSet && !sizeSet
	if smoke {
		mode = "file"
		sizeText = strconv.Itoa(len(nativeFixture()))
	}
	if mode != "file" && mode != "archive" {
		t.Fatal("unknown measurement mode")
	}
	size, e := strconv.ParseInt(sizeText, 10, 64)
	if e != nil || size < int64(len(nativeFixture())) || size > MaxArtifactBytes {
		t.Fatal("invalid owned fixture size")
	}
	directory := t.TempDir()
	t.Setenv("TMPDIR", directory)
	file, e := os.Create(filepath.Join(directory, "owned-fixture"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = file.Close() })
	writeBody := func(w io.Writer) {
		if _, e := w.Write(nativeFixture()); e != nil {
			t.Fatal(e)
		}
		if _, e := io.CopyN(w, ownedCaptureZeroReader{}, size-int64(len(nativeFixture()))); e != nil {
			t.Fatal(e)
		}
	}
	if mode == "archive" {
		writer := tar.NewWriter(file)
		if e := writer.WriteHeader(&tar.Header{Name: "codex", Mode: 0555, Uid: 0, Size: size, Typeflag: tar.TypeReg}); e != nil {
			t.Fatal(e)
		}
		writeBody(writer)
		if e := writer.Close(); e != nil {
			t.Fatal(e)
		}
	} else {
		writeBody(file)
	}
	if _, e := file.Seek(0, 0); e != nil {
		t.Fatal(e)
	}
	start := time.Now()
	var artifact *Artifact
	if mode == "file" {
		artifact, e = CaptureFile("/opt/native/codex", file)
	} else {
		artifact, e = CaptureArchive("/opt/native/codex", file)
	}
	elapsed := time.Since(start)
	if e != nil || artifact == nil {
		t.Fatalf("capture failed: %v", e)
	}
	status, e := os.ReadFile("/proc/self/status")
	if e != nil {
		t.Fatal(e)
	}
	peak := uint64(0)
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "VmHWM:") {
			fields := strings.Fields(line)
			if len(fields) != 3 || fields[2] != "kB" {
				t.Fatal("unexpected RSS units")
			}
			value, e := strconv.ParseUint(fields[1], 10, 64)
			if e != nil {
				t.Fatal(e)
			}
			peak = value * 1024
		}
	}
	if peak == 0 {
		t.Fatal("peak RSS unavailable")
	}
	if smoke {
		if artifact.Path != "/opt/native/codex" || artifact.Format != "static_elf" || artifact.SHA256 != fmt.Sprintf("%x", sha256.Sum256(nativeFixture())) {
			t.Fatal("smoke capture did not preserve the exact native fixture")
		}
		fmt.Println("OWNED_CAPTURE_RSS_SMOKE native capture and VmHWM validated; functional smoke only")
		return
	}
	encoded, e := json.Marshal(map[string]any{"mode": mode, "fixture_bytes": size, "capture_wall_ns": elapsed.Nanoseconds(), "process_peak_rss_bytes": peak, "artifact_sha256": artifact.SHA256})
	if e != nil {
		t.Fatal(e)
	}
	fmt.Println("OWNED_CAPTURE_RSS " + string(encoded))
}

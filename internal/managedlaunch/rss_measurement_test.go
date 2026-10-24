//go:build linux

package managedlaunch

import (
	"archive/tar"
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
func TestManagedStreamingRSSMeasurement(t *testing.T) {
	mode := os.Getenv("CREWSHIP_CAPTURE_RSS")
	if mode == "" {
		t.Skip("explicit fresh-process RSS measurement helper")
	}
	if mode != "file" && mode != "archive" {
		t.Fatal("unknown measurement mode")
	}
	size, e := strconv.ParseInt(os.Getenv("CREWSHIP_CAPTURE_RSS_BYTES"), 10, 64)
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
	encoded, e := json.Marshal(map[string]any{"mode": mode, "fixture_bytes": size, "capture_wall_ns": elapsed.Nanoseconds(), "process_peak_rss_bytes": peak, "artifact_sha256": artifact.SHA256})
	if e != nil {
		t.Fatal(e)
	}
	fmt.Println("OWNED_CAPTURE_RSS " + string(encoded))
}

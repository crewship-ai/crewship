package managedlaunch

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"testing"
)

type countingArtifactReader struct {
	io.ReaderAt
	calls int
}

func (r *countingArtifactReader) ReadAt(p []byte, off int64) (int, error) {
	r.calls++
	return r.ReaderAt.ReadAt(p, off)
}
func TestManagedDynamicMetadataUsesBoundedBlockReads(t *testing.T) {
	const segment = 2 << 20
	raw := append(nativeFixture(), make([]byte, segment)...)
	binary.LittleEndian.PutUint16(raw[16:], 3)
	binary.LittleEndian.PutUint32(raw[64:], 2)
	binary.LittleEndian.PutUint64(raw[72:], 120)
	binary.LittleEndian.PutUint64(raw[96:], segment)
	for i := 120; i < len(raw)-16; i += 16 {
		binary.LittleEndian.PutUint64(raw[i:], 5)
	}
	expected, err := legacyByteCapture("/opt/native/codex", raw)
	if err != nil {
		t.Fatal(err)
	}
	file := streamOwnedFile(t, raw)
	reader := &countingArtifactReader{ReaderAt: file}
	got, err := qualifyReader("/opt/native/codex", reader, int64(len(raw)), expected.SHA256)
	if err != nil || *got != *expected {
		t.Fatalf("qualification differs: %v %v", got, err)
	}
	if reader.calls > segment/(64<<10)+20 {
		t.Fatalf("metadata reads not bounded by block count: %d", reader.calls)
	}
	// A dependency beyond the first block must still be rejected.
	binary.LittleEndian.PutUint64(raw[120+(64<<10):], 1)
	if _, err := Capture("/opt/native/codex", raw); err == nil {
		t.Fatal("late dependency accepted")
	}
}
func TestManagedStreamingArtifactSizeBounds(t *testing.T) {
	file := streamOwnedFile(t, nativeFixture())
	writable, err := os.OpenFile(file.Name(), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writable.Close()
	if err := writable.Truncate(MaxArtifactBytes + 1); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureFile("/opt/native/codex", file); err == nil {
		t.Fatal("oversized sparse file accepted")
	}
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(&tar.Header{Name: "codex", Mode: 0555, Uid: 0, Typeflag: tar.TypeReg, Size: MaxArtifactBytes + 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureArchive("/opt/native/codex", bytes.NewReader(archive.Bytes())); err == nil {
		t.Fatal("oversized declared archive accepted")
	}
	if _, err := CaptureFile("/opt/native/codex", nil); err == nil {
		t.Fatal("nil file accepted")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureFile("/opt/native/codex", file); err == nil {
		t.Fatal("closed file accepted")
	}
}
func TestManagedVerifyArchiveFileMatchesFrozenBytes(t *testing.T) {
	raw := nativeFixture()
	file := streamOwnedFile(t, raw)
	wrong := append([]byte(nil), raw...)
	wrong[len(wrong)-1] ^= 1
	good := artifactArchive(t, "codex", 0555, 0, raw, false)
	for _, archive := range [][]byte{good, good[:len(good)/4], artifactArchive(t, "codex", 0555, 0, wrong, false), artifactArchive(t, "codex", 0555, 0, raw, true)} {
		want := legacyLauncherArchive(bytes.NewReader(archive), "codex", raw)
		got := VerifyArchiveFile(bytes.NewReader(archive), "codex", file)
		if (want == nil) != (got == nil) {
			t.Fatalf("archive verification differs: got%v want%v", got, want)
		}
	}
	if VerifyArchiveFile(bytes.NewReader(good), "codex", nil) == nil {
		t.Fatal("nil expected file accepted")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if VerifyArchiveFile(bytes.NewReader(good), "codex", file) == nil {
		t.Fatal("closed expected file accepted")
	}
}

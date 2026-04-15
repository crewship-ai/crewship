package backup

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"testing"
	"time"
)

// buildPayloadWithEntry constructs a minimal payload tar.zst that
// contains exactly one custom entry plus the db/dump.json the
// extractor tolerates. ExtractPayload can then be fed this to verify
// path-traversal and symlink rejection without touching the network or
// a real docker daemon.
func buildPayloadWithEntry(t *testing.T, name string, typeflag byte, linkname string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw, err := NewTarZstWriter(&buf)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	hdr := &tar.Header{
		Name:     name,
		Mode:     0o644,
		ModTime:  time.Now(),
		Typeflag: typeflag,
		Linkname: linkname,
	}
	if typeflag == tar.TypeReg {
		hdr.Size = int64(len("hello"))
	}
	if err := tw.tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if typeflag == tar.TypeReg {
		if _, err := tw.tw.Write([]byte("hello")); err != nil {
			t.Fatalf("write body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return buf.Bytes()
}

func TestExtractPayload_RejectsParentTraversal(t *testing.T) {
	payload := buildPayloadWithEntry(t, "workspace/../../etc/shadow", tar.TypeReg, "")
	_, err := ExtractPayload(bytes.NewReader(payload))
	if err == nil {
		t.Fatal("expected ExtractPayload to reject a '..' entry")
	}
	if !errors.Is(err, ErrInvalidManifest) {
		t.Errorf("expected ErrInvalidManifest, got %v", err)
	}
}

func TestExtractPayload_RejectsSymlink(t *testing.T) {
	payload := buildPayloadWithEntry(t, "workspace/my-crew/link", tar.TypeSymlink, "/etc/passwd")
	_, err := ExtractPayload(bytes.NewReader(payload))
	if err == nil {
		t.Fatal("expected ExtractPayload to reject a symlink entry")
	}
	if !errors.Is(err, ErrInvalidManifest) {
		t.Errorf("expected ErrInvalidManifest, got %v", err)
	}
}

func TestExtractPayload_RejectsHardLink(t *testing.T) {
	payload := buildPayloadWithEntry(t, "workspace/my-crew/hard", tar.TypeLink, "/etc/passwd")
	_, err := ExtractPayload(bytes.NewReader(payload))
	if err == nil {
		t.Fatal("expected ExtractPayload to reject a hardlink entry")
	}
	if !errors.Is(err, ErrInvalidManifest) {
		t.Errorf("expected ErrInvalidManifest, got %v", err)
	}
}

func TestExtractPayload_AcceptsValidLayout(t *testing.T) {
	payload := buildPayloadWithEntry(t, "workspace/my-crew/file.txt", tar.TypeReg, "")
	out, err := ExtractPayload(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("valid payload should succeed, got %v", err)
	}
	if _, ok := out.WorkspaceTarsBySlug["my-crew"]; !ok {
		t.Errorf("expected workspace tar for my-crew, got %v", out.WorkspaceTarsBySlug)
	}
}

func TestExtractPayload_EmptyPayload(t *testing.T) {
	// An empty payload (no entries at all) should produce an empty
	// ExtractedPayload rather than panic.
	var buf bytes.Buffer
	tw, err := NewTarZstWriter(&buf)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	out, err := ExtractPayload(&buf)
	if err != nil {
		t.Fatalf("empty payload: %v", err)
	}
	if out == nil {
		t.Fatal("expected non-nil ExtractedPayload")
	}
}

// Sanity check: io.Reader plumbing works on the common happy path.
func TestExtractPayload_ReadsDBDumpEntry(t *testing.T) {
	var buf bytes.Buffer
	tw, _ := NewTarZstWriter(&buf)
	_ = tw.WriteFile("db/dump.json", 0o644, time.Now(),
		[]byte(`{"workspace_id":"ws","tables":{}}`))
	_ = tw.Close()

	out, err := ExtractPayload(&buf)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if out.DBDump == nil {
		t.Fatal("expected DBDump to be parsed")
	}
	if out.DBDump.WorkspaceID != "ws" {
		t.Errorf("wrong workspace id: %v", out.DBDump.WorkspaceID)
	}
}

// Ensure the helper we use in tests does not itself swallow errors.
func TestBuildPayloadWithEntry_Sanity(t *testing.T) {
	payload := buildPayloadWithEntry(t, "workspace/x/file.txt", tar.TypeReg, "")
	tr, err := NewTarZstReader(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	defer func() { _ = tr.Close() }()
	hdr, err := tr.Next()
	if err == io.EOF {
		t.Fatal("unexpected EOF")
	}
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if hdr.Name != "workspace/x/file.txt" {
		t.Errorf("unexpected entry name: %q", hdr.Name)
	}
}

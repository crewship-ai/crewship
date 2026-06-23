package backup

// Coverage tests for selftest.go — the BackupSelfTest failure branches
// (probe error, copy failures, collect failure, no-op restore, content
// mismatch, missing workspace section) plus the small helpers.

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// selftestSeqOps wraps fakeDockerOps with per-call CopyTo behaviour so
// individual stages of the self-test pipeline can be sabotaged.
type selftestSeqOps struct {
	*fakeDockerOps
	copyToCalls    int
	failCopyToAt   int   // 1-based call index that returns failErr
	failErr        error //
	dropCopyToFrom int   // 1-based index from which CopyTo is silently swallowed
	garbageOnDrop  bool  // also corrupt the canary when dropping
}

func (s *selftestSeqOps) CopyTo(ctx context.Context, id, dst string, content io.Reader) error {
	s.copyToCalls++
	if s.failCopyToAt > 0 && s.copyToCalls == s.failCopyToAt {
		_, _ = io.Copy(io.Discard, content)
		return s.failErr
	}
	if s.dropCopyToFrom > 0 && s.copyToCalls >= s.dropCopyToFrom {
		_, _ = io.Copy(io.Discard, content)
		if s.garbageOnDrop {
			for name := range s.workspace {
				if strings.HasPrefix(name, "CANARY-") {
					s.workspace[name] = []byte("neither canary nor sentinel")
				}
			}
			s.garbageOnDrop = false
		}
		return nil
	}
	return s.fakeDockerOps.CopyTo(ctx, id, dst, content)
}

func selftestOpts() SelfTestOpts {
	return SelfTestOpts{
		ContainerID: "ctr-1",
		Crew:        CrewTarget{ID: "crew-1", Slug: "research", ContainerID: "ctr-1"},
	}
}

func TestBackupSelfTest_ProbeError(t *testing.T) {
	ops := &probeErrOps{fakeDockerOps: newFakeDockerOps(), err: errors.New("daemon down")}
	_, err := BackupSelfTest(context.Background(), ops, selftestOpts())
	if err == nil || !strings.Contains(err.Error(), "container probe") {
		t.Fatalf("err = %v", err)
	}
}

func TestBackupSelfTest_WriteCanaryError(t *testing.T) {
	inner := newFakeDockerOps()
	ops := &selftestSeqOps{fakeDockerOps: inner, failCopyToAt: 1, failErr: errors.New("cp refused")}
	_, err := BackupSelfTest(context.Background(), ops, selftestOpts())
	if err == nil || !strings.Contains(err.Error(), "write canary") {
		t.Fatalf("err = %v", err)
	}
}

func TestBackupSelfTest_MutateCanaryError(t *testing.T) {
	inner := newFakeDockerOps()
	ops := &selftestSeqOps{fakeDockerOps: inner, failCopyToAt: 2, failErr: errors.New("cp refused")}
	_, err := BackupSelfTest(context.Background(), ops, selftestOpts())
	if err == nil || !strings.Contains(err.Error(), "mutate canary") {
		t.Fatalf("err = %v", err)
	}
}

// readAfterWriteOps drops every CopyFrom on the floor with an error so
// the immediate readback check trips.
type readAfterWriteOps struct {
	*fakeDockerOps
}

func (r *readAfterWriteOps) CopyFrom(context.Context, string, string) (io.ReadCloser, error) {
	return nil, errors.New("simulated cp-from failure")
}

func TestBackupSelfTest_ReadAfterWriteError(t *testing.T) {
	ops := &readAfterWriteOps{fakeDockerOps: newFakeDockerOps()}
	_, err := BackupSelfTest(context.Background(), ops, selftestOpts())
	if err == nil || !strings.Contains(err.Error(), "read-after-write") {
		t.Fatalf("err = %v", err)
	}
}

// truncatingReadbackOps serves a TRUNCATED canary on the very first
// readback so the dropped-content guard fires.
type truncatingReadbackOps struct {
	*fakeDockerOps
}

func (o *truncatingReadbackOps) CopyFrom(ctx context.Context, id, srcPath string) (io.ReadCloser, error) {
	rc, err := o.fakeDockerOps.CopyFrom(ctx, id, srcPath)
	if err != nil || srcPath == ContainerWorkspacePath {
		return rc, err
	}
	// Single-file readback: re-tar with half the content.
	defer rc.Close()
	tr := tar.NewReader(rc)
	hdr, err := tr.Next()
	if err != nil {
		return nil, err
	}
	body, _ := io.ReadAll(tr)
	half := body[:len(body)/2]
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: hdr.Name, Mode: 0o644, Size: int64(len(half))})
	_, _ = tw.Write(half)
	_ = tw.Close()
	return io.NopCloser(&buf), nil
}

func TestBackupSelfTest_DroppedContentDetected(t *testing.T) {
	ops := &truncatingReadbackOps{fakeDockerOps: newFakeDockerOps()}
	res, err := BackupSelfTest(context.Background(), ops, selftestOpts())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.OK || !strings.Contains(res.Error, "docker cp is dropping content") {
		t.Fatalf("res = %+v", res)
	}
}

// collectFailOps fails only the whole-workspace CopyFrom (the collect
// stage); the single-file readback keeps working.
type collectFailOps struct {
	*fakeDockerOps
}

func (o *collectFailOps) CopyFrom(ctx context.Context, id, srcPath string) (io.ReadCloser, error) {
	if srcPath == ContainerWorkspacePath {
		return nil, errors.New("collect stage broken")
	}
	return o.fakeDockerOps.CopyFrom(ctx, id, srcPath)
}

func TestBackupSelfTest_CollectError(t *testing.T) {
	ops := &collectFailOps{fakeDockerOps: newFakeDockerOps()}
	_, err := BackupSelfTest(context.Background(), ops, selftestOpts())
	if err == nil || !strings.Contains(err.Error(), "collect workspace") {
		t.Fatalf("err = %v", err)
	}
}

// emptyCollectOps returns an empty tar for the workspace collect so the
// bundle ends up without a workspace section.
type emptyCollectOps struct {
	*fakeDockerOps
}

func (o *emptyCollectOps) CopyFrom(ctx context.Context, id, srcPath string) (io.ReadCloser, error) {
	if srcPath == ContainerWorkspacePath {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		_ = tw.Close()
		return io.NopCloser(&buf), nil
	}
	return o.fakeDockerOps.CopyFrom(ctx, id, srcPath)
}

func TestBackupSelfTest_NoWorkspaceSection(t *testing.T) {
	ops := &emptyCollectOps{fakeDockerOps: newFakeDockerOps()}
	res, err := BackupSelfTest(context.Background(), ops, selftestOpts())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.OK || !strings.Contains(res.Error, "no workspace section") {
		t.Fatalf("res = %+v", res)
	}
	if res.BundleBytes == 0 {
		t.Errorf("bundle bytes should reflect the (empty) collected archive")
	}
}

func TestBackupSelfTest_NoOpRestoreDetected(t *testing.T) {
	inner := newFakeDockerOps()
	// CopyTo #1 = write canary, #2 = mutate, #3 = restore (dropped).
	ops := &selftestSeqOps{fakeDockerOps: inner, dropCopyToFrom: 3}
	res, err := BackupSelfTest(context.Background(), ops, selftestOpts())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.OK || !strings.Contains(res.Error, "restore was a no-op") {
		t.Fatalf("res = %+v", res)
	}
}

func TestBackupSelfTest_ContentMismatchDetected(t *testing.T) {
	inner := newFakeDockerOps()
	ops := &selftestSeqOps{fakeDockerOps: inner, dropCopyToFrom: 3, garbageOnDrop: true}
	res, err := BackupSelfTest(context.Background(), ops, selftestOpts())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.OK || !strings.Contains(res.Error, "content mismatch") {
		t.Fatalf("res = %+v", res)
	}
}

// restoreFailOps fails CopyTo on the restore call (call 3) with a real
// error, exercising the RestoreCrew failure branch.
func TestBackupSelfTest_RestoreError(t *testing.T) {
	inner := newFakeDockerOps()
	ops := &selftestSeqOps{fakeDockerOps: inner, failCopyToAt: 3, failErr: errors.New("volume gone")}
	_, err := BackupSelfTest(context.Background(), ops, selftestOpts())
	if err == nil || !strings.Contains(err.Error(), "backup self-test: restore") {
		t.Fatalf("err = %v", err)
	}
}

// verifyReadFailOps lets the readback + collect work but fails every
// single-file CopyFrom after the collect stage, so BOTH the nested and
// the fallback verify reads error out.
type verifyReadFailOps struct {
	*fakeDockerOps
	singleFileReads int
}

func (o *verifyReadFailOps) CopyFrom(ctx context.Context, id, srcPath string) (io.ReadCloser, error) {
	if srcPath == ContainerWorkspacePath {
		return o.fakeDockerOps.CopyFrom(ctx, id, srcPath)
	}
	o.singleFileReads++
	if o.singleFileReads >= 2 { // 1 = post-write readback; 2+ = verify reads
		return nil, errors.New("verify read sabotaged")
	}
	return o.fakeDockerOps.CopyFrom(ctx, id, srcPath)
}

func TestBackupSelfTest_VerifyReadError(t *testing.T) {
	ops := &verifyReadFailOps{fakeDockerOps: newFakeDockerOps()}
	_, err := BackupSelfTest(context.Background(), ops, selftestOpts())
	if err == nil || !strings.Contains(err.Error(), "verify read") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildSingleFileTar_RoundTrip(t *testing.T) {
	raw, err := buildSingleFileTar("hello.txt", []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(raw))
	hdr, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Name != "hello.txt" || hdr.Mode != 0o644 || hdr.Size != 7 {
		t.Errorf("hdr = %+v", hdr)
	}
	body, _ := io.ReadAll(tr)
	if string(body) != "payload" {
		t.Errorf("body = %q", body)
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Errorf("expected single entry, next = %v", err)
	}
}

func TestReadCanary_MissingEntry(t *testing.T) {
	ops := newFakeDockerOps()
	ops.workspace["other.txt"] = []byte("x")
	_, err := readCanary(context.Background(), ops, "ctr", ContainerWorkspacePath+"/other.txt", "CANARY-want.txt")
	if err == nil || !strings.Contains(err.Error(), "missing from CopyFrom tar") {
		t.Fatalf("err = %v", err)
	}
}

func TestRandomToken(t *testing.T) {
	tok, err := randomToken(16)
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 32 {
		t.Errorf("token length = %d, want 32 hex chars", len(tok))
	}
	tok2, _ := randomToken(16)
	if tok == tok2 {
		t.Errorf("two tokens identical: %s", tok)
	}
}

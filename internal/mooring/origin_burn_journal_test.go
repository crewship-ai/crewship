//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package mooring

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func originBurnTestData(n int) OriginBurnData {
	return OriginBurnData{Origin: fmt.Sprint(n), SourceID: fmt.Sprintf("source-%d", n), BindingDigest: "sha256:" + strings.Repeat("a", 64)}
}
func originBurnTestCreate(t *testing.T) (*OriginBurnJournal, string, string) {
	t.Helper()
	root := t.TempDir()
	j, err := CreateFreshOriginBurnJournal(t.Context(), true, root, "synthetic-lineage")
	if err != nil {
		t.Fatal(err)
	}
	_, id, err := j.IdentityData(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j, root, id
}
func TestOriginBurnJournalActualAppendRetryReopenAndLock(t *testing.T) {
	j, root, id := originBurnTestCreate(t)
	before, err := os.ReadFile(filepath.Join(root, originBurnFilename))
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range []OriginBurnData{
		{Origin: "0", SourceID: "bad", BindingDigest: originBurnTestData(1).BindingDigest},
		{Origin: "01", SourceID: "bad", BindingDigest: originBurnTestData(1).BindingDigest},
		{Origin: "9007199254740992", SourceID: "bad", BindingDigest: originBurnTestData(1).BindingDigest},
		{Origin: "1", SourceID: "", BindingDigest: originBurnTestData(1).BindingDigest},
		{Origin: "1", SourceID: "bad", BindingDigest: "sha256:" + strings.Repeat("A", 64)},
	} {
		if digest, err := j.AppendBurnData(t.Context(), data); err == nil || digest != "" {
			t.Fatal("invalid scalar burned", data)
		}
	}
	if after, _ := os.ReadFile(filepath.Join(root, originBurnFilename)); !bytes.Equal(before, after) {
		t.Fatal("invalid data changed journal")
	}
	digest, err := j.AppendBurnData(t.Context(), originBurnTestData(1))
	if err != nil || !originBurnDigest(digest) {
		t.Fatal(err)
	}
	stat, _ := os.Stat(filepath.Join(root, originBurnFilename))
	if retry, err := j.AppendBurnData(t.Context(), originBurnTestData(1)); err != nil || retry != digest {
		t.Fatal("exact retry changed", err)
	}
	after, _ := os.Stat(filepath.Join(root, originBurnFilename))
	if stat.Size() != after.Size() {
		t.Fatal("exact retry appended")
	}
	conflict := originBurnTestData(1)
	conflict.BindingDigest = "sha256:" + strings.Repeat("b", 64)
	if _, err := j.AppendBurnData(t.Context(), conflict); err == nil {
		t.Fatal("identity rebound")
	}
	conflict = originBurnTestData(2)
	conflict.SourceID = "source-1"
	if _, err := j.AppendBurnData(t.Context(), conflict); err == nil {
		t.Fatal("source reused")
	}
	if other, err := OpenOriginBurnJournal(t.Context(), true, root, "synthetic-lineage", id); err == nil || other != nil {
		t.Fatal("second exclusive writer opened")
	}
	if other, err := CreateFreshOriginBurnJournal(t.Context(), true, root, "synthetic-lineage"); err == nil || other != nil {
		t.Fatal("existing file reset")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := j.AppendBurnData(t.Context(), originBurnTestData(2)); err == nil {
		t.Fatal("closed handle wrote")
	}
	reopened, err := OpenOriginBurnJournal(t.Context(), true, root, "synthetic-lineage", id)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	entries, err := reopened.EntriesData(t.Context())
	if err != nil || len(entries) != 1 || entries[0] != originBurnTestData(1) {
		t.Fatal("burn lost on reopen", entries, err)
	}
	entries[0].Origin = "77"
	again, err := reopened.EntriesData(t.Context())
	if err != nil || again[0].Origin != "1" {
		t.Fatal("caller changed retained data")
	}
	if _, err := reopened.AppendBurnData(t.Context(), originBurnTestData(2)); err != nil {
		t.Fatal(err)
	}
}
func TestOriginBurnJournalMissingDisabledAndForeignIdentity(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	if j, e := CreateFreshOriginBurnJournal(t.Context(), false, missing, "lineage"); e == nil || j != nil {
		t.Fatal("disabled created journal")
	}
	if _, e := os.Stat(missing); !os.IsNotExist(e) {
		t.Fatal("disabled touched root", e)
	}
	if j, e := OpenOriginBurnJournal(t.Context(), true, root, "lineage", strings.Repeat("a", 64)); e == nil || j != nil {
		t.Fatal("missing history guessed empty")
	}
	if _, e := os.Stat(filepath.Join(root, originBurnFilename)); !os.IsNotExist(e) {
		t.Fatal("open created missing file", e)
	}
	j, root, id := originBurnTestCreate(t)
	_ = j.Close()
	for _, identity := range [][2]string{{"foreign-lineage", id}, {"synthetic-lineage", strings.Repeat("f", 64)}} {
		if j, e := OpenOriginBurnJournal(t.Context(), true, root, identity[0], identity[1]); e == nil || j != nil {
			t.Fatal("foreign identity adopted")
		}
	}
}
func TestOriginBurnJournalCorruptOrIncompleteNeverRepairs(t *testing.T) {
	j, _, id := originBurnTestCreate(t)
	if _, err := j.AppendBurnData(t.Context(), originBurnTestData(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := j.AppendBurnData(t.Context(), originBurnTestData(2)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(j.root.Name(), originBurnFilename))
	if err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	cases := map[string][]byte{
		"missing-final-newline": raw[:len(raw)-1], "truncated-record": raw[:len(raw)-7],
		"unknown-key":      bytes.Replace(raw, []byte(`{"format":`), []byte(`{"unknown":0,"format":`), 1),
		"duplicate-key":    bytes.Replace(raw, []byte(`"origin":"1"`), []byte(`"origin":"1","origin":"1"`), 1),
		"case-key":         bytes.Replace(raw, []byte(`"origin":"1"`), []byte(`"Origin":"1"`), 1),
		"numeric-origin":   bytes.Replace(raw, []byte(`"origin":"1"`), []byte(`"origin":1`), 1),
		"changed-content":  bytes.Replace(raw, []byte(`source-1`), []byte(`source-x`), 1),
		"extra-empty-line": append(append([]byte(nil), raw...), '\n'),
		"oversized-line":   append(append([]byte(nil), raw...), bytes.Repeat([]byte("x"), maxOriginBurnLine+1)...),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, originBurnFilename)
			if e := os.WriteFile(path, data, 0o600); e != nil {
				t.Fatal(e)
			}
			if opened, e := OpenOriginBurnJournal(t.Context(), true, root, "synthetic-lineage", id); e == nil || opened != nil {
				t.Fatal("corrupt file opened")
			}
			after, e := os.ReadFile(path)
			if e != nil || !bytes.Equal(data, after) {
				t.Fatal("corrupt file repaired/truncated")
			}
		})
	}
	t.Run("oversized-file", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, originBurnFilename)
		if e := os.WriteFile(path, raw, 0o600); e != nil {
			t.Fatal(e)
		}
		if e := os.Truncate(path, maxOriginBurnBytes+1); e != nil {
			t.Fatal(e)
		}
		if opened, e := OpenOriginBurnJournal(t.Context(), true, root, "synthetic-lineage", id); e == nil || opened != nil {
			t.Fatal("oversized file opened")
		}
	})
}
func TestOriginBurnJournalIntegrityErrorPoisonsEvenAfterBytesRestored(t *testing.T) {
	j, root, _ := originBurnTestCreate(t)
	if _, e := j.AppendBurnData(t.Context(), originBurnTestData(1)); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(root, originBurnFilename)
	original, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	changed := bytes.Replace(original, []byte("source-1"), []byte("source-x"), 1)
	if e := os.WriteFile(path, changed, 0o600); e != nil {
		t.Fatal(e)
	}
	if _, e := j.EntriesData(t.Context()); e == nil {
		t.Fatal("same-length corruption undetected")
	}
	if e := os.WriteFile(path, original, 0o600); e != nil {
		t.Fatal(e)
	}
	if _, e := j.AppendBurnData(t.Context(), originBurnTestData(2)); e == nil {
		t.Fatal("failed integrity check silently reset")
	}
}
func TestOriginBurnJournalSymlinkHardlinkAndPathReplacement(t *testing.T) {
	j, root, id := originBurnTestCreate(t)
	_ = j.Close()
	path := filepath.Join(root, originBurnFilename)
	old := filepath.Join(root, "original")
	if e := os.Rename(path, old); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(old, path); e != nil {
		t.Fatal(e)
	}
	if opened, e := OpenOriginBurnJournal(t.Context(), true, root, "synthetic-lineage", id); e == nil || opened != nil {
		t.Fatal("symlink accepted")
	}
	if e := os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if e := os.Link(old, path); e != nil {
		t.Fatal(e)
	}
	if opened, e := OpenOriginBurnJournal(t.Context(), true, root, "synthetic-lineage", id); e == nil || opened != nil {
		t.Fatal("hardlink accepted")
	}
	if e := os.Remove(old); e != nil {
		t.Fatal(e)
	}
	opened, e := OpenOriginBurnJournal(t.Context(), true, root, "synthetic-lineage", id)
	if e != nil {
		t.Fatal(e)
	}
	defer opened.Close()
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if e := os.Rename(path, old); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, raw, 0o600); e != nil {
		t.Fatal(e)
	}
	if _, e := opened.AppendBurnData(t.Context(), originBurnTestData(1)); e == nil {
		t.Fatal("replacement inode accepted")
	}
}

type originBurnFailSync struct{ originBurnFile }

func (f originBurnFailSync) Sync() error { return errors.New("synthetic fsync uncertainty") }
func TestOriginBurnJournalUncertainFsyncNeverAcknowledgesOrContinues(t *testing.T) {
	j, root, id := originBurnTestCreate(t)
	j.file = originBurnFailSync{j.file} // private I/O fault fixture; no public callback.
	if digest, e := j.AppendBurnData(t.Context(), originBurnTestData(1)); e == nil || digest != "" {
		t.Fatal("fsync uncertainty acknowledged")
	}
	if _, e := j.EntriesData(t.Context()); e == nil {
		t.Fatal("uncertain instance returned claimed coverage")
	}
	if _, e := j.AppendBurnData(t.Context(), originBurnTestData(2)); e == nil {
		t.Fatal("uncertainty advanced later burn")
	}
	_ = j.Close()
	// Complete uncertain bytes are conservatively retained as DATA after an
	// explicit reopen and successful sync, never declared an unused origin.
	reopened, e := OpenOriginBurnJournal(t.Context(), true, root, "synthetic-lineage", id)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	entries, e := reopened.EntriesData(t.Context())
	if e != nil || len(entries) != 1 || entries[0] != originBurnTestData(1) {
		t.Fatal("uncertain burn recycled", entries, e)
	}
}
func TestOriginBurnJournalConcurrentExactRetryAndCapacity(t *testing.T) {
	j, root, id := originBurnTestCreate(t)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := j.AppendBurnData(t.Context(), originBurnTestData(1)); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	entries, e := j.EntriesData(t.Context())
	if e != nil || len(entries) != 1 {
		t.Fatal("concurrent retry duplicated", e)
	}
	for n := 2; n <= MaxOriginInventoryEntries; n++ {
		if _, e := j.AppendBurnData(t.Context(), originBurnTestData(n)); e != nil {
			t.Fatalf("bounded burn %d: %v", n, e)
		}
	}
	if _, e := j.AppendBurnData(t.Context(), originBurnTestData(MaxOriginInventoryEntries+1)); e == nil {
		t.Fatal("capacity silently evicted or overflowed")
	}
	if digest, e := j.AppendBurnData(t.Context(), originBurnTestData(1)); e != nil || digest == "" {
		t.Fatal("full exact retry lost original", e)
	}
	_ = j.Close()
	reopened, e := OpenOriginBurnJournal(t.Context(), true, root, "synthetic-lineage", id)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	entries, e = reopened.EntriesData(t.Context())
	if e != nil || len(entries) != MaxOriginInventoryEntries {
		t.Fatal("full history lost on reopen", len(entries), e)
	}
}
func TestOriginBurnJournalCancellationAndZeroHandle(t *testing.T) {
	j, root, _ := originBurnTestCreate(t)
	before, _ := os.ReadFile(filepath.Join(root, originBurnFilename))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e := j.AppendBurnData(ctx, originBurnTestData(1)); e == nil {
		t.Fatal("cancelled append accepted")
	}
	after, _ := os.ReadFile(filepath.Join(root, originBurnFilename))
	if !bytes.Equal(before, after) {
		t.Fatal("cancelled append wrote")
	}
	var zero OriginBurnJournal
	for _, value := range []*OriginBurnJournal{nil, &zero, {self: j}} {
		if _, e := value.AppendBurnData(t.Context(), originBurnTestData(1)); e == nil {
			t.Fatal("zero/copied handle wrote")
		}
	}
}

type originBurnPartialWrite struct{ originBurnFile }

func (f originBurnPartialWrite) Write(raw []byte) (int, error) {
	return f.originBurnFile.Write(raw[:len(raw)-1])
}
func TestOriginBurnJournalPartialAppendRemainsUnqualifiedWithoutRepair(t *testing.T) {
	j, root, id := originBurnTestCreate(t)
	j.file = originBurnPartialWrite{j.file}
	if digest, e := j.AppendBurnData(t.Context(), originBurnTestData(1)); e == nil || digest != "" {
		t.Fatal("partial write acknowledged")
	}
	if _, e := j.AppendBurnData(t.Context(), originBurnTestData(2)); e == nil {
		t.Fatal("partial write advanced prefix")
	}
	_ = j.Close()
	before, e := os.ReadFile(filepath.Join(root, originBurnFilename))
	if e != nil {
		t.Fatal(e)
	}
	if reopened, e := OpenOriginBurnJournal(t.Context(), true, root, "synthetic-lineage", id); e == nil || reopened != nil {
		t.Fatal("partial append repaired or accepted")
	}
	after, e := os.ReadFile(filepath.Join(root, originBurnFilename))
	if e != nil || !bytes.Equal(before, after) {
		t.Fatal("partial append erased")
	}
}

type originBurnCancelAfterSync struct {
	originBurnFile
	cancel context.CancelFunc
}

func (f originBurnCancelAfterSync) Sync() error {
	err := f.originBurnFile.Sync()
	f.cancel()
	return err
}
func TestOriginBurnJournalLateCancellationRetainsBurnWithoutAcknowledgment(t *testing.T) {
	j, root, id := originBurnTestCreate(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	j.file = originBurnCancelAfterSync{j.file, cancel}
	if digest, e := j.AppendBurnData(ctx, originBurnTestData(1)); e == nil || digest != "" {
		t.Fatal("late cancellation acknowledged")
	}
	if _, e := j.AppendBurnData(t.Context(), originBurnTestData(2)); e == nil {
		t.Fatal("late cancellation continued instance")
	}
	_ = j.Close()
	reopened, e := OpenOriginBurnJournal(t.Context(), true, root, "synthetic-lineage", id)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	entries, e := reopened.EntriesData(t.Context())
	if e != nil || len(entries) != 1 || entries[0] != originBurnTestData(1) {
		t.Fatal("late-cancel burn recycled", e)
	}
}
func TestOriginBurnJournalIntactOldPrefixIsDataNotRecoveryCoverage(t *testing.T) {
	j, root, id := originBurnTestCreate(t)
	if _, e := j.AppendBurnData(t.Context(), originBurnTestData(1)); e != nil {
		t.Fatal(e)
	}
	first, e := os.Stat(filepath.Join(root, originBurnFilename))
	if e != nil {
		t.Fatal(e)
	}
	if _, e := j.AppendBurnData(t.Context(), originBurnTestData(2)); e != nil {
		t.Fatal(e)
	}
	_ = j.Close()
	// Deliberately simulate lost full-record history in OWN temporary data.
	// A checksummed file alone cannot detect this rollback. No journal API is an
	// issuance/coverage proof, and this expected limitation blocks that claim.
	if e := os.Truncate(filepath.Join(root, originBurnFilename), first.Size()); e != nil {
		t.Fatal(e)
	}
	reopened, e := OpenOriginBurnJournal(t.Context(), true, root, "synthetic-lineage", id)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	entries, e := reopened.EntriesData(t.Context())
	if e != nil || len(entries) != 1 {
		t.Fatal("intact-prefix limitation changed", e)
	}
}
func TestOriginBurnJournalRejectsUnboundedIdentifiersAndRelativeRoot(t *testing.T) {
	if j, e := CreateFreshOriginBurnJournal(t.Context(), true, ".", "synthetic-lineage"); e == nil || j != nil {
		t.Fatal("implicit working-root journal created")
	}
	root := t.TempDir()
	if j, e := CreateFreshOriginBurnJournal(t.Context(), true, root, strings.Repeat("x", 8192)); e == nil || j != nil {
		t.Fatal("unbounded lineage admitted")
	}
	j, _, _ := originBurnTestCreate(t)
	data := originBurnTestData(1)
	data.SourceID = strings.Repeat("x", 8192)
	if _, e := j.AppendBurnData(t.Context(), data); e == nil {
		t.Fatal("unbounded source identifier admitted")
	}
}

type originBurnTamperBetweenReads struct {
	originBurnFile
	path    string
	changed []byte
	swapped bool
	err     error
}

func (f *originBurnTamperBetweenReads) ReadAt(p []byte, offset int64) (int, error) {
	n, e := f.originBurnFile.ReadAt(p, offset)
	if offset == 0 && !f.swapped {
		f.swapped = true
		f.err = os.WriteFile(f.path, f.changed, 0o600)
	}
	return n, e
}
func TestOriginBurnJournalTamperBetweenValidationAndAppendCannotAcknowledge(t *testing.T) {
	j, root, _ := originBurnTestCreate(t)
	if _, e := j.AppendBurnData(t.Context(), originBurnTestData(1)); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(root, originBurnFilename)
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	changed := bytes.Replace(raw, []byte("source-1"), []byte("source-x"), 1)
	fault := &originBurnTamperBetweenReads{originBurnFile: j.file, path: path, changed: changed}
	j.file = fault
	digest, e := j.AppendBurnData(t.Context(), originBurnTestData(2))
	if !fault.swapped || fault.err != nil {
		t.Fatal("actual disk corruption fixture not reached", fault.err)
	}
	if e == nil || digest != "" {
		t.Fatal("append acknowledged a corrupt prefix after its earlier integrity check")
	}
}

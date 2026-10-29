package backup

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// Store only a compressed stream, not a million files or an in-memory TAR.
func capacityArchive(t *testing.T, count int, header func(int) tar.Header) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "entries.tar.zst")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := zstd.NewWriter(f, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	w := tar.NewWriter(z)
	for i := 0; i < count; i++ {
		h := header(i)
		if err := w.WriteHeader(&h); err != nil {
			t.Fatalf("write header %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

type capacityEntryReader interface {
	Next() (*tar.Header, error)
	Close() error
}

func capacityReader(t *testing.T, path string, ctx context.Context, replay bool) capacityEntryReader {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if !replay {
		r, err := NewTarZstReader(f, ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = r.Close() })
		return r
	}
	// Inflate the stored fixture as an input stream to the raw TAR replay
	// reader. The replay reader itself accounts for the decoded work.
	z, err := zstd.NewReader(f, zstd.WithDecoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(z.Close)
	r := newRestoreTarReader(ctx, z)
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func readCapacityEntries(t *testing.T, r capacityEntryReader, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		h, err := r.Next()
		if err != nil || h == nil {
			t.Fatalf("entry %d of %d: header=%v error=%v", i+1, count, h, err)
		}
	}
}

func requireCapacityError(t *testing.T, err error, kind string, limit int64) {
	t.Helper()
	if !errors.Is(err, ErrRestoreLimit) || !strings.Contains(err.Error(), fmt.Sprintf("%s allowance %d ", kind, limit)) {
		t.Fatalf("want %s limit %d, got %v", kind, limit, err)
	}
}

func TestRestoreBudgetMillionEntries(t *testing.T) {
	const million = 1_000_000
	prefix := "workspace/crews/research/projects/pages/site/.git/objects/" + strings.Repeat("assets/subdirectory/", 6)
	header := func(i int) tar.Header {
		h := tar.Header{Name: prefix + strconv.Itoa(i), Mode: 0600, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		if i%16 == 0 {
			h.Typeflag = tar.TypeSymlink
			h.Linkname = "objects/" + strings.Repeat("a", 64)
		}
		return h
	}
	archives := []struct {
		name  string
		count int
		path  string
	}{
		{name: "million", count: million},
		{name: "million_plus_one", count: million + 1},
	}
	for i := range archives {
		archives[i].path = capacityArchive(t, archives[i].count, header)
	}
	for _, replay := range []bool{false, true} {
		mode := "compressed"
		if replay {
			mode = "replay"
		}
		for _, archive := range archives {
			t.Run(mode+"/"+archive.name, func(t *testing.T) {
				r := capacityReader(t, archive.path, budgetContext(DefaultRestoreLimits()), replay)
				readCapacityEntries(t, r, million)
				_, err := r.Next()
				if archive.count == million {
					if err != io.EOF {
						t.Fatalf("million-entry archive must end cleanly: %v", err)
					}
				} else {
					requireCapacityError(t, err, "entries", million)
				}
			})
		}
	}
	t.Run("shared_compressed_and_replay", func(t *testing.T) {
		ctx := budgetContext(DefaultRestoreLimits())
		first := capacityReader(t, archives[0].path, ctx, false)
		readCapacityEntries(t, first, million/2)
		if err := first.Close(); err != nil {
			t.Fatal(err)
		}
		replay := capacityReader(t, archives[0].path, ctx, true)
		readCapacityEntries(t, replay, million/2)
		if got := readBudget(ctx).entries; got != million {
			t.Fatalf("combined work: got %d entries, want %d", got, million)
		}
		_, err := replay.Next()
		requireCapacityError(t, err, "entries", million)
	})
}

func TestRestoreBudgetHeaderByteBoundary(t *testing.T) {
	headers := []tar.Header{
		{Name: "workspace/žluťoučký", Linkname: "odkazy/🧭", Typeflag: tar.TypeSymlink, Format: tar.FormatPAX},
		{Name: "pages/資料", Linkname: "assets/číslo", Typeflag: tar.TypeSymlink, Format: tar.FormatPAX},
		{Name: "a", Typeflag: tar.TypeReg, Format: tar.FormatUSTAR},
	}
	limit := int64(len(headers[0].Name) + len(headers[0].Linkname) + len(headers[1].Name) + len(headers[1].Linkname))
	t.Setenv("CREWSHIP_BACKUP_RESTORE_HEADER_BYTES", strconv.FormatInt(limit, 10))
	t.Setenv("CREWSHIP_BACKUP_RESTORE_ENTRIES", "10")
	limits, err := RestoreLimitsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{2, 3} {
		path := capacityArchive(t, count, func(i int) tar.Header { return headers[i] })
		for _, replay := range []bool{false, true} {
			t.Run(fmt.Sprintf("entries_%d/replay_%t", count, replay), func(t *testing.T) {
				ctx := budgetContext(limits)
				r := capacityReader(t, path, ctx, replay)
				readCapacityEntries(t, r, 2)
				if got := readBudget(ctx).headers; got != limit {
					t.Fatalf("names and link names must count UTF-8 bytes: got %d, want %d", got, limit)
				}
				_, err := r.Next()
				if count == 2 {
					if err != io.EOF {
						t.Fatalf("exact header byte allowance must pass: %v", err)
					}
				} else {
					requireCapacityError(t, err, "headers", limit)
				}
			})
		}
	}
}

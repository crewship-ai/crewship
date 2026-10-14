package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type boardUploadPadding struct{}

func (boardUploadPadding) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func boardUploadTemps(t *testing.T, root string) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".tmp-") {
			names = append(names, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestBoardUploadLargeCompressedInputDoesNotBufferBody(t *testing.T) {
	h, ws, user, _ := boardFileFixture(t)
	png := boardRaster(t, false)
	// Trailing bytes are intentionally discarded by raster re-encoding, but
	// must still count toward the input limit and exact retry identity.
	body := io.MultiReader(bytes.NewReader(png), io.LimitReader(boardUploadPadding{}, maxBoardFileInput-int64(len(png))))
	r := pagesGrantRequest(t, "POST", "/?base_seq=0&mutation_id=large-padding", ws, user, "OWNER", "")
	r.SetPathValue("slug", "report")
	r.Body = io.NopCloser(body)
	rr := httptest.NewRecorder()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	h.UploadBoardFile(rr, r)
	runtime.ReadMemStats(&after)
	artifactExpect(t, rr, 201)
	// This tiny raster with a maximum-size transport needs only codec/SQL
	// scratch. ReadAll's growth previously allocated over 11 MiB on this case.
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("5 MiB upload allocated %d heap bytes", allocated)
	if allocated > 8<<20 {
		t.Fatalf("5 MiB transport retained/grew heap buffers: allocated %d bytes", allocated)
	}
	if leftovers := boardUploadTemps(t, h.artifactStoragePath); len(leftovers) != 0 {
		t.Fatalf("successful upload left staging files: %v", leftovers)
	}
	file := boardFileID(t, rr)
	read := boardFileRead(t, h, ws, user, "report", file)
	artifactExpect(t, read, 200)
	if len(read.Body.Bytes()) >= 1<<20 {
		t.Fatal("transport padding was published rather than re-encoded")
	}
}

type boardUploadObservedBody struct {
	reader io.Reader
	read   func()
}

func (b *boardUploadObservedBody) Read(p []byte) (int, error) {
	b.read()
	return b.reader.Read(p)
}
func (*boardUploadObservedBody) Close() error { return nil }

func TestBoardUploadStagingIsPrivateAndCleanedOnAllExits(t *testing.T) {
	for _, outcome := range []string{"success", "invalid", "oversize", "cancelled", "stale"} {
		t.Run(outcome, func(t *testing.T) {
			h, ws, user, _ := boardFileFixture(t)
			var body io.Reader = bytes.NewReader(boardRaster(t, false))
			want := 201
			switch outcome {
			case "invalid":
				body, want = strings.NewReader("<svg>untrusted</svg>"), 422
			case "oversize":
				body, want = io.LimitReader(boardUploadPadding{}, maxBoardFileInput+1), 413
			case "cancelled":
				want = 408
			case "stale":
				artifactExpect(t, boardFileUpload(t, h, ws, user, "report", "first", 0, boardRaster(t, true)), 201)
				want = 409
			}
			r := pagesGrantRequest(t, "POST", "/?base_seq=0&mutation_id=staging", ws, user, "OWNER", "")
			r.SetPathValue("slug", "report")
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			r = r.WithContext(ctx)
			observed := false
			r.Body = &boardUploadObservedBody{reader: body, read: func() {
				if observed {
					return
				}
				observed = true
				names := boardUploadTemps(t, h.artifactStoragePath)
				if len(names) != 1 {
					t.Fatalf("expected one private input staging file, got %v", names)
				}
				info, err := os.Stat(names[0])
				if err != nil || info.Mode().Perm()&0o077 != 0 {
					t.Fatalf("staging readable outside uploader: %v %v", info, err)
				}
				if outcome == "cancelled" {
					cancel()
				}
			}}
			rr := httptest.NewRecorder()
			h.UploadBoardFile(rr, r)
			artifactExpect(t, rr, want)
			if !observed {
				t.Fatal("upload never consumed its bounded input")
			}
			if names := boardUploadTemps(t, h.artifactStoragePath); len(names) != 0 {
				t.Fatalf("%s left staging files: %v", outcome, names)
			}
		})
	}
}

func TestBoardUploadRetryUsesOriginalBytesDespiteCanonicalDeduplication(t *testing.T) {
	h, ws, user, _ := boardFileFixture(t)
	png := boardRaster(t, false)
	original := append(append([]byte(nil), png...), []byte("original discarded trailer")...)
	first := boardFileUpload(t, h, ws, user, "report", "original-bytes", 0, original)
	artifactExpect(t, first, 201)
	retry := boardFileUpload(t, h, ws, user, "report", "original-bytes", 0, original)
	artifactExpect(t, retry, 200)
	if boardFileID(t, first) != boardFileID(t, retry) {
		t.Fatal("exact retry returned another file")
	}
	changed := append(append([]byte(nil), png...), []byte("different discarded trailer")...)
	artifactExpect(t, boardFileUpload(t, h, ws, user, "report", "original-bytes", 0, changed), 409)
	if names := boardUploadTemps(t, h.artifactStoragePath); len(names) != 0 {
		t.Fatalf("retry left staging files: %v", names)
	}
}

func TestBoardUploadCapacityWaitDoesNotReadBody(t *testing.T) {
	h, ws, user, _ := boardFileFixture(t)
	r := pagesGrantRequest(t, "POST", "/?base_seq=0&mutation_id=waiting", ws, user, "OWNER", "")
	r.SetPathValue("slug", "report")
	first, ok := acquireBoardWrite(httptest.NewRecorder(), r)
	if !ok {
		t.Fatal("first capacity slot unavailable")
	}
	defer first()
	second, ok := acquireBoardWrite(httptest.NewRecorder(), r)
	if !ok {
		t.Fatal("second capacity slot unavailable")
	}
	defer second()
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	r = r.WithContext(ctx)
	r.Body = &boardUploadObservedBody{reader: strings.NewReader("unused"), read: func() { t.Error("waiting request read its body") }}
	rr := httptest.NewRecorder()
	h.UploadBoardFile(rr, r)
	artifactExpect(t, rr, 408)
	if names := boardUploadTemps(t, h.artifactStoragePath); len(names) != 0 {
		t.Fatalf("waiting request created staging files: %v", names)
	}
}

func TestBoardUploadStagingFailureDoesNotPublish(t *testing.T) {
	h, ws, user, page := boardFileFixture(t)
	// A regular file as the storage root deterministically refuses staging,
	// including under root, where chmod-only fixtures would not fail.
	root := filepath.Join(storageDir(t), "unavailable")
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.artifactStoragePath = root
	rr := boardFileUpload(t, h, ws, user, "report", "unavailable", 0, boardRaster(t, false))
	artifactExpect(t, rr, 500)
	var count, seq int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_board_files WHERE board_page_id=?`, page).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed storage published file: count=%d error=%v", count, err)
	}
	if err := h.db.QueryRow(`SELECT seq FROM page_boards WHERE page_id=?`, page).Scan(&seq); err != nil || seq != 0 {
		t.Fatalf("failed storage advanced sequence: seq=%d error=%v", seq, err)
	}
}

func TestBoardUploadTransportReadErrorCleansStaging(t *testing.T) {
	h, ws, user, _ := boardFileFixture(t)
	r := pagesGrantRequest(t, "POST", "/?base_seq=0&mutation_id=broken-body", ws, user, "OWNER", "")
	r.SetPathValue("slug", "report")
	r.Body = &boardUploadFailingBody{}
	rr := httptest.NewRecorder()
	h.UploadBoardFile(rr, r)
	artifactExpect(t, rr, 500)
	if names := boardUploadTemps(t, h.artifactStoragePath); len(names) != 0 {
		t.Fatalf("failed body left staging files: %v", names)
	}
}

type boardUploadFailingBody struct{}

func (*boardUploadFailingBody) Read([]byte) (int, error) { return 0, errors.New("interrupted upload") }
func (*boardUploadFailingBody) Close() error             { return nil }

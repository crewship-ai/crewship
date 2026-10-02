package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"filippo.io/age"
	"github.com/klauspost/compress/zstd"
)

func encryptedUploadFixture(t *testing.T) []byte {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	manifest := newValidManifest()
	var output bytes.Buffer
	if err = WriteBundle(&output, manifest, bytes.NewBufferString("private fixture contents"), WriteBundleOptions{Recipients: []age.Recipient{identity.Recipient()}}); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
func uploadTestOptions(t *testing.T) UploadOptions {
	t.Helper()
	return UploadOptions{Directory: t.TempDir(), MaxBytes: 1 << 20, Space: func(string) (uint64, error) { return 1 << 30, nil }}
}
func TestUploadPublishesEncryptedChecksumProofAndKeepsDuplicateEvidence(t *testing.T) {
	db := openMigratedDBCov(t)
	data := encryptedUploadFixture(t)
	opts := uploadTestOptions(t)
	result, err := ImportUploadedBundle(t.Context(), db, bytes.NewReader(data), opts)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(result.Entry.FilePath)
	if err != nil || !bytes.Equal(stored, data) {
		t.Fatalf("stored bundle differs: %v", err)
	}
	if result.Entry.ProofLevel != ProofChecksum || !result.Entry.Encrypted || result.Duplicate {
		t.Fatalf("wrong upload proof: %+v", result.Entry)
	}
	if err = SetCatalogProof(t.Context(), db, result.Entry.FilePath, ProofContents, result.Manifest.CreatedAt); err != nil {
		t.Fatal(err)
	}
	again, err := ImportUploadedBundle(t.Context(), db, bytes.NewReader(data), opts)
	if err != nil || !again.Duplicate {
		t.Fatalf("duplicate: %+v %v", again, err)
	}
	entries, err := ListCatalog(t.Context(), db, "")
	if err != nil || len(entries) != 1 || entries[0].ProofLevel != ProofContents {
		t.Fatalf("duplicate reset evidence: %+v %v", entries, err)
	}
}
func TestUploadAuditFailureRollsBackCatalogAndOwnedFiles(t *testing.T) {
	db := openMigratedDBCov(t)
	opts := uploadTestOptions(t)
	opts.Finalize = func(context.Context, *sql.Tx, *ImportedBundle) error { return errors.New("audit unavailable") }
	if _, err := ImportUploadedBundle(t.Context(), db, bytes.NewReader(encryptedUploadFixture(t)), opts); err == nil {
		t.Fatal("audit failure accepted")
	}
	entries, err := ListCatalog(t.Context(), db, "")
	if err != nil || len(entries) != 0 {
		t.Fatalf("audit rollback: %v %v", entries, err)
	}
	files, err := filepath.Glob(filepath.Join(opts.Directory, "*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("failed upload leaked files: %v %v", files, err)
	}
}
func TestUploadRejectsSizeSpaceCancellationAndTruncation(t *testing.T) {
	data := encryptedUploadFixture(t)
	for _, kind := range []string{"size", "space", "canceled", "truncated"} {
		t.Run(kind, func(t *testing.T) {
			db := openMigratedDBCov(t)
			opts := uploadTestOptions(t)
			ctx := t.Context()
			body := data
			switch kind {
			case "size":
				opts.MaxBytes = int64(len(data) - 1)
			case "space":
				opts.Space = func(string) (uint64, error) { return 1, nil }
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "truncated":
				body = data[:len(data)/2]
			}
			if _, err := ImportUploadedBundle(ctx, db, bytes.NewReader(body), opts); err == nil {
				t.Fatal("invalid upload published")
			}
			files, err := os.ReadDir(opts.Directory)
			if err != nil || len(files) != 0 {
				t.Fatalf("invalid upload staging leaked: %v %v", files, err)
			}
		})
	}
}

func mutateUploadArchive(t *testing.T, data []byte, kind string) []byte {
	t.Helper()
	decoder, err := zstd.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()
	reader := tar.NewReader(decoder)
	var output bytes.Buffer
	encoder, err := zstd.NewWriter(&output)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(encoder)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == manifestFileName && (kind == "future" || kind == "legacy") {
			var manifest map[string]any
			if err = json.Unmarshal(body, &manifest); err != nil {
				t.Fatal(err)
			}
			version := FormatVersion + 1
			if kind == "legacy" {
				version = OldestRecoverableFormatVersion
			}
			manifest["format_version"] = version
			body, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
		}
		if header.Name == payloadFileName {
			switch kind {
			case "traversal":
				header.Name = "../payload"
			case "symlink":
				header.Typeflag = tar.TypeSymlink
				header.Linkname = "/etc/passwd"
				body = nil
			case "checksum":
				body[len(body)-1] ^= 1
			case "plaintext":
				body = []byte("plain private payload")
			}
		}
		header.Size = int64(len(body))
		if err = writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err = writer.Write(body); err != nil {
			t.Fatal(err)
		}
		if kind == "duplicate" && header.Name == payloadFileName {
			if err = writer.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if _, err = writer.Write(body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = encoder.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestUploadRejectsMaliciousArchiveWithoutPublishing(t *testing.T) {
	source := encryptedUploadFixture(t)
	for _, kind := range []string{"future", "traversal", "symlink", "checksum", "plaintext", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			db := openMigratedDBCov(t)
			opts := uploadTestOptions(t)
			_, err := ImportUploadedBundle(t.Context(), db, bytes.NewReader(mutateUploadArchive(t, source, kind)), opts)
			if !errors.Is(err, ErrUploadInvalid) {
				t.Fatalf("malicious archive %s: %v", kind, err)
			}
			entries, err := ListCatalog(t.Context(), db, "")
			if err != nil || len(entries) != 0 {
				t.Fatalf("published invalid archive: %v %v", entries, err)
			}
			files, err := os.ReadDir(opts.Directory)
			if err != nil || len(files) != 0 {
				t.Fatalf("invalid archive leaked files: %v %v", files, err)
			}
		})
	}
}

func TestUploadLegacyArchiveIsMarkedForConversion(t *testing.T) {
	db := openMigratedDBCov(t)
	opts := uploadTestOptions(t)
	result, err := ImportUploadedBundle(t.Context(), db, bytes.NewReader(mutateUploadArchive(t, encryptedUploadFixture(t), "legacy")), opts)
	if err != nil || !result.ConversionRequired {
		t.Fatalf("legacy conversion marker: %+v %v", result, err)
	}
}

func TestUploadConcurrentDuplicateKeepsOneCommittedFile(t *testing.T) {
	db := openMigratedDBCov(t)
	opts := uploadTestOptions(t)
	data := encryptedUploadFixture(t)
	var workers sync.WaitGroup
	errorsFound := make(chan error, 2)
	for i := 0; i < 2; i++ {
		workers.Go(func() {
			_, err := ImportUploadedBundle(t.Context(), db, bytes.NewReader(data), opts)
			errorsFound <- err
		})
	}
	workers.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, err := ListCatalog(t.Context(), db, "")
	if err != nil || len(entries) != 1 {
		t.Fatalf("concurrent catalog: %v %v", entries, err)
	}
	files, err := os.ReadDir(opts.Directory)
	if err != nil || len(files) != 1 {
		t.Fatalf("concurrent files: %v %v", files, err)
	}
}

type uploadCancelReader struct {
	io.Reader
	cancel context.CancelFunc
}

func (r uploadCancelReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.cancel()
	return n, err
}

func TestUploadInterruptedTransferRemovesEncryptedStaging(t *testing.T) {
	for _, kind := range []string{"disk fills", "cancel during read", "length mismatch"} {
		t.Run(kind, func(t *testing.T) {
			db := openMigratedDBCov(t)
			opts := uploadTestOptions(t)
			data := encryptedUploadFixture(t)
			var reader io.Reader = bytes.NewReader(data)
			ctx := t.Context()
			switch kind {
			case "disk fills":
				calls := 0
				opts.Space = func(string) (uint64, error) {
					calls++
					if calls > 2 {
						return 0, nil
					}
					return 1 << 30, nil
				}
			case "cancel during read":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				reader = uploadCancelReader{reader, cancel}
			case "length mismatch":
				opts.ContentLength = int64(len(data) + 1)
			}
			if _, err := ImportUploadedBundle(ctx, db, reader, opts); err == nil {
				t.Fatal("interrupted transfer was accepted")
			}
			files, err := os.ReadDir(opts.Directory)
			if err != nil || len(files) != 0 {
				t.Fatalf("staging leaked: %v %v", files, err)
			}
		})
	}
}

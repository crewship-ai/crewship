package offsite

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type keyFileStore struct {
	LocalBlobs
	files map[string]string
	err   error
}

func (s keyFileStore) SealedKeyFiles([]string) (map[string]string, error) { return s.files, s.err }

type unavailableBlobStore struct{ err error }

func (s unavailableBlobStore) BlobPath(string) (string, error) { return "", s.err }

type failingDestination struct {
	Destination
	operation, key string
	err            error
}

func (d failingDestination) Get(ctx context.Context, key string) (io.ReadCloser, Object, error) {
	if d.operation == "get" && (d.key == "" || d.key == key) {
		return nil, Object{}, d.err
	}
	return d.Destination.Get(ctx, key)
}
func (d failingDestination) Head(ctx context.Context, key string) (Object, error) {
	if d.operation == "head" && (d.key == "" || d.key == key) {
		return Object{}, d.err
	}
	return d.Destination.Head(ctx, key)
}
func (d failingDestination) Put(ctx context.Context, key string, r io.Reader, n int64, sum string) (Object, error) {
	if d.operation == "put" && (d.key == "" || d.key == key) {
		return Object{}, d.err
	}
	return d.Destination.Put(ctx, key, r, n, sum)
}
func (d failingDestination) Delete(ctx context.Context, key string) error {
	if d.operation == "delete" && (d.key == "" || d.key == key) {
		return d.err
	}
	return d.Destination.Delete(ctx, key)
}
func (d failingDestination) List(ctx context.Context, prefix string) ([]Object, error) {
	if d.operation == "list" {
		return nil, d.err
	}
	return d.Destination.List(ctx, prefix)
}

func TestEnvironmentKeyFilesAreVerifiedAndFailuresPropagate(t *testing.T) {
	fs := startFake(t)
	dst := fs.newS3(t, s3Opts{pathStyle: true})
	data := []byte("age-encryption.org/v1\nsealed test fixture")
	path := writeTemp(t, data)
	store := keyFileStore{files: map[string]string{"keys/generation/recipient.age": path}}
	n, err := UploadEnvironmentKeys(t.Context(), dst, store, "archive", nil, UploadOptions{SHA256: strings.Repeat("0", 64)})
	if err != nil || n != 1 {
		t.Fatalf("keys=%d %v", n, err)
	}
	rc, obj, err := dst.Get(t.Context(), "archive/environments/keys/generation/recipient.age")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(got, data) || obj.SHA256 != hexSHA(data) {
		t.Fatalf("uploaded key changed: %q %+v %v", got, obj, err)
	}
	for _, tc := range []struct {
		name   string
		store  keyFileStore
		prefix string
		dst    Destination
	}{
		{"manifest unavailable", keyFileStore{err: errors.New("key manifest unavailable")}, "", dst},
		{"unsafe name", keyFileStore{files: map[string]string{"../escape": path}}, "", dst},
		{"unsafe prefix", store, "../escape", dst},
		{"missing file", keyFileStore{files: map[string]string{"keys/generation/missing.age": filepath.Join(t.TempDir(), "missing")}}, "", dst},
		{"destination unavailable", store, "", failingDestination{Destination: dst, operation: "put", err: errors.New("offline")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if n, err := UploadEnvironmentKeys(t.Context(), tc.dst, tc.store, tc.prefix, nil, UploadOptions{}); err == nil || n != 0 {
				t.Fatalf("failure hidden: %d %v", n, err)
			}
		})
	}
}

func TestUploadBundleDoesNotPublishRefsAfterKeyOrBundleFailure(t *testing.T) {
	for _, failure := range []string{"refs put", "refs verify", "keys", "bundle"} {
		t.Run(failure, func(t *testing.T) {
			fs := startFake(t)
			dst := fs.newS3(t, s3Opts{pathStyle: true})
			root := t.TempDir()
			digest := putBlob(t, root, []byte("encrypted layer"))
			store := keyFileStore{LocalBlobs: dirBlobs(root)}
			var target Destination = dst
			boom := errors.New("transfer unavailable")
			switch failure {
			case "refs put":
				target = failingDestination{Destination: dst, operation: "put", key: EnvironmentRefsKey("bundle"), err: boom}
			case "refs verify":
				target = failingDestination{Destination: dst, operation: "head", key: EnvironmentRefsKey("bundle"), err: boom}
			case "keys":
				store.err = boom
			case "bundle":
				target = failingDestination{Destination: dst, operation: "put", key: "bundle", err: boom}
			}
			_, err := UploadBundle(t.Context(), target, store, writeTemp(t, []byte("sealed bundle")), "bundle", []string{digest}, UploadOptions{})
			if !errors.Is(err, boom) {
				t.Fatalf("transfer failure=%v", err)
			}
			if _, _, err := dst.Get(t.Context(), "bundle"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("failed bundle published: %v", err)
			}
			// A failed initial refs verification can conservatively leave refs behind;
			// after a verified refs write, later failure must remove them.
			if failure == "keys" || failure == "bundle" {
				if _, _, err := dst.Get(t.Context(), EnvironmentRefsKey("bundle")); !errors.Is(err, ErrNotFound) {
					t.Fatalf("failed bundle refs remain: %v", err)
				}
			}
		})
	}
}

func TestEnvironmentTransferStopsOnCanceledOrUnavailableSources(t *testing.T) {
	fs := startFake(t)
	dst := fs.newS3(t, s3Opts{pathStyle: true})
	root := t.TempDir()
	digest := putBlob(t, root, []byte("sealed layer"))
	boom := errors.New("source unavailable")
	for _, direction := range []string{"upload", "download"} {
		for _, failure := range []string{"canceled", "bad digest", "local store", "remote head"} {
			t.Run(direction+"/"+failure, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var store LocalBlobs = dirBlobs(root)
				var target Destination = dst
				d := digest
				switch failure {
				case "canceled":
					cancel()
				case "bad digest":
					d = "sha256:bad"
				case "local store":
					store = unavailableBlobStore{boom}
				case "remote head":
					target = failingDestination{Destination: dst, operation: "head", err: boom}
					if direction == "download" {
						store = dirBlobs(t.TempDir())
					}
				}
				var result EnvironmentTransfer
				var err error
				if direction == "upload" {
					result, err = UploadEnvironmentBlobs(ctx, target, store, "", []string{d}, UploadOptions{})
				} else {
					result, err = DownloadEnvironmentBlobs(ctx, target, store, "", []string{d}, DownloadOptions{})
				}
				if err == nil || result.Transferred != 0 || result.Missing != 0 {
					t.Fatalf("failure became completed/missing transfer: %+v %v", result, err)
				}
			})
		}
	}
}

func TestDeleteBundlePreservesLayerRefsWhenRemoteEvidenceOrDeletionFails(t *testing.T) {
	for _, failure := range []string{"get", "delete bundle", "list", "delete layer", "delete refs"} {
		t.Run(failure, func(t *testing.T) {
			fs := startFake(t)
			dst := fs.newS3(t, s3Opts{pathStyle: true})
			root := t.TempDir()
			digest := putBlob(t, root, []byte("sealed layer"))
			_, err := UploadBundle(t.Context(), dst, dirBlobs(root), writeTemp(t, []byte("sealed bundle")), "bundle", []string{digest}, UploadOptions{})
			if err != nil {
				t.Fatal(err)
			}
			boom := errors.New("object store unavailable")
			target := failingDestination{Destination: dst, operation: failure, err: boom}
			switch failure {
			case "delete bundle":
				target.operation = "delete"
				target.key = "bundle"
			case "delete layer":
				target.operation = "delete"
				target.key, _ = EnvironmentBlobKey("", digest)
			case "delete refs":
				target.operation = "delete"
				target.key = EnvironmentRefsKey("bundle")
			}
			removed, err := DeleteBundle(t.Context(), target, "bundle")
			if !errors.Is(err, boom) {
				t.Fatalf("deletion failure=%v", err)
			}
			want := 0
			if failure == "delete refs" {
				want = 1
			}
			if removed != want {
				t.Fatalf("removed=%d want %d", removed, want)
			}
			refs, err := ReadEnvironmentRefs(t.Context(), dst, "bundle")
			if err != nil || len(refs) != 1 || refs[0] != digest {
				t.Fatalf("recovery evidence lost: %v %v", refs, err)
			}
		})
	}
}

func TestDownloadEnvironmentCannotReplaceUnusableLocalDirectory(t *testing.T) {
	fs := startFake(t)
	dst := fs.newS3(t, s3Opts{pathStyle: true})
	root := t.TempDir()
	digest := putBlob(t, root, []byte("sealed layer"))
	if _, err := UploadEnvironmentBlobs(t.Context(), dst, dirBlobs(root), "", []string{digest}, UploadOptions{}); err != nil {
		t.Fatal(err)
	}
	blocker := writeTemp(t, []byte("keep this file"))
	if _, err := DownloadEnvironmentBlobs(t.Context(), dst, dirBlobs(blocker), "", []string{digest}, DownloadOptions{}); err == nil {
		t.Fatal("unusable storage accepted")
	}
	if data, err := os.ReadFile(blocker); err != nil || string(data) != "keep this file" {
		t.Fatalf("local file damaged: %q %v", data, err)
	}
}

func TestDownloadBundleRefusesIncompleteOrUnreadableEnvironmentEvidence(t *testing.T) {
	for _, failure := range []string{"refs unavailable", "invalid refs", "invalid digest", "missing store", "layer download"} {
		t.Run(failure, func(t *testing.T) {
			fs := startFake(t)
			dst := fs.newS3(t, s3Opts{pathStyle: true})
			root := t.TempDir()
			digest := putBlob(t, root, []byte("sealed layer"))
			if _, err := UploadBundle(t.Context(), dst, dirBlobs(root), writeTemp(t, []byte("sealed bundle")), "bundle", []string{digest}, UploadOptions{}); err != nil {
				t.Fatal(err)
			}
			var store LocalBlobs = dirBlobs(t.TempDir())
			var target Destination = dst
			boom := errors.New("remote object unavailable")
			switch failure {
			case "refs unavailable":
				target = failingDestination{Destination: dst, operation: "get", key: EnvironmentRefsKey("bundle"), err: boom}
			case "invalid refs", "invalid digest":
				data := []byte("{broken")
				if failure == "invalid digest" {
					data = []byte(`{"bundle":"bundle","blobs":["sha256:bad"]}`)
				}
				if _, err := dst.Put(t.Context(), EnvironmentRefsKey("bundle"), bytes.NewReader(data), int64(len(data)), hexSHA(data)); err != nil {
					t.Fatal(err)
				}
			case "missing store":
				store = nil
			case "layer download":
				key, _ := EnvironmentBlobKey("", digest)
				target = failingDestination{Destination: dst, operation: "get", key: key, err: boom}
			}
			local := filepath.Join(t.TempDir(), "bundle")
			if _, err := DownloadBundle(t.Context(), target, store, "bundle", local, DownloadOptions{}); err == nil {
				t.Fatal("incomplete restore accepted")
			}
			if _, err := os.Stat(local); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("incomplete bundle published locally: %v", err)
			}
		})
	}
	if _, err := UploadBundle(t.Context(), nil, nil, "unused", "bundle", []string{"sha256:" + strings.Repeat("a", 64)}, UploadOptions{}); err == nil {
		t.Fatal("layer-dependent upload accepted without a store")
	}
}

func TestFailedDownloadDoesNotLeavePartialFilesOrReplaceDirectory(t *testing.T) {
	fs := startFake(t)
	dst := fs.newS3(t, s3Opts{pathStyle: true})
	if _, err := Upload(t.Context(), dst, writeTemp(t, []byte("sealed data")), "bundle", UploadOptions{}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, local := range []string{filepath.Join(root, "missing", "bundle"), filepath.Join(root, "directory")} {
		if strings.HasSuffix(local, "directory") {
			if err := os.Mkdir(local, 0700); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := Download(t.Context(), dst, "bundle", local, DownloadOptions{}); err == nil {
			t.Fatal("unusable local path accepted")
		}
		matches, err := filepath.Glob(filepath.Join(root, ".*.offsite-*"))
		if err != nil || len(matches) != 0 {
			t.Fatalf("partial files remain: %v %v", matches, err)
		}
	}
}

func TestDeleteBundleKeepsLayersWhenAnotherBundleHasOversizedRefs(t *testing.T) {
	fs := startFake(t)
	dst := fs.newS3(t, s3Opts{pathStyle: true})
	root := t.TempDir()
	digest := putBlob(t, root, []byte("sealed shared layer"))
	if _, err := UploadBundle(t.Context(), dst, dirBlobs(root), writeTemp(t, []byte("sealed bundle")), "bundle", []string{digest}, UploadOptions{}); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte(" "), maxRefsBytes+1)
	if _, err := dst.Put(t.Context(), EnvironmentRefsKey("other"), bytes.NewReader(data), int64(len(data)), hexSHA(data)); err != nil {
		t.Fatal(err)
	}
	if n, err := DeleteBundle(t.Context(), dst, "bundle"); err == nil || !strings.Contains(err.Error(), "larger than") || n != 0 {
		t.Fatalf("unbounded refs accepted: %d %v", n, err)
	}
	key, _ := EnvironmentBlobKey("", digest)
	if _, err := dst.Head(t.Context(), key); err != nil {
		t.Fatalf("potentially shared layer deleted: %v", err)
	}
	refs, err := ReadEnvironmentRefs(t.Context(), dst, "bundle")
	if err != nil || len(refs) != 1 {
		t.Fatalf("cleanup evidence discarded: %v %v", refs, err)
	}
}

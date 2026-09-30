package offsite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dirBlobs is a minimal LocalBlobs: <dir>/<hex>.
type dirBlobs string

func (d dirBlobs) BlobPath(digest string) (string, error) {
	h, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || !isHexSHA256(h) {
		return "", errors.New("bad digest")
	}
	return filepath.Join(string(d), h), nil
}

func putBlob(t *testing.T, dir string, data []byte) string {
	t.Helper()
	d := "sha256:" + hexSHA(data)
	p, _ := dirBlobs(dir).BlobPath(d)
	if err := os.WriteFile(p, data, 0o400); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestEnvironmentBlobsRoundTrip(t *testing.T) {
	ctx := context.Background()
	fs := startFake(t)
	s := fs.newS3(t, s3Opts{pathStyle: true, prefix: "crewship"})
	src := t.TempDir()
	shared := putBlob(t, src, []byte("shared base layer"))
	own := putBlob(t, src, []byte("this bundle's own layer"))
	absent := "sha256:" + hexSHA([]byte("never stored"))

	up, err := UploadEnvironmentBlobs(ctx, s, dirBlobs(src), "offsite", []string{shared, own, absent}, UploadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if up.Transferred != 2 || up.Missing != 1 || up.Present != 0 {
		t.Fatalf("first upload = %+v", up)
	}
	// A second bundle sharing the base layer uploads nothing new.
	again, err := UploadEnvironmentBlobs(ctx, s, dirBlobs(src), "offsite", []string{shared}, UploadOptions{})
	if err != nil || again.Transferred != 0 || again.Present != 1 {
		t.Fatalf("second upload = %+v, %v", again, err)
	}

	dst := t.TempDir()
	down, err := DownloadEnvironmentBlobs(ctx, s, dirBlobs(dst), "offsite", []string{shared, own, absent}, DownloadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if down.Transferred != 2 || down.Missing != 1 {
		t.Fatalf("download = %+v", down)
	}
	p, _ := dirBlobs(dst).BlobPath(own)
	if b, err := os.ReadFile(p); err != nil || string(b) != "this bundle's own layer" {
		t.Fatalf("downloaded blob = %q, %v", b, err)
	}
	// Already local: not fetched again.
	if down, err := DownloadEnvironmentBlobs(ctx, s, dirBlobs(dst), "offsite", []string{own}, DownloadOptions{}); err != nil || down.Present != 1 {
		t.Fatalf("re-download = %+v, %v", down, err)
	}
}

func TestDownloadEnvironmentBlobRefusesAMisnamedObject(t *testing.T) {
	ctx := context.Background()
	fs := startFake(t)
	s := fs.newS3(t, s3Opts{pathStyle: true})
	want := "sha256:" + hexSHA([]byte("the real layer"))
	key, _ := EnvironmentBlobKey("", want)
	// Somebody stored other bytes under that digest's name.
	src := writeTemp(t, []byte("something else"))
	if _, err := Upload(ctx, s, src, key, UploadOptions{}); err != nil {
		t.Fatal(err)
	}
	_, err := DownloadEnvironmentBlobs(ctx, s, dirBlobs(t.TempDir()), "", []string{want}, DownloadOptions{})
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("misnamed blob accepted: %v", err)
	}
}

func TestEnvironmentBlobKey(t *testing.T) {
	d := "sha256:" + strings.Repeat("ab", 32)
	if k, err := EnvironmentBlobKey("p", d); err != nil || k != "p/environments/blobs/sha256/"+strings.Repeat("ab", 32) {
		t.Errorf("key = %q, %v", k, err)
	}
	if _, err := EnvironmentBlobKey("", "sha256:../x"); err == nil {
		t.Error("bad digest accepted")
	}
}

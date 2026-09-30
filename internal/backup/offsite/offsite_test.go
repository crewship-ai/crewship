package offsite

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUploadVerifies(t *testing.T) {
	data := payload(6000)
	cases := []struct {
		name       string
		partSize   int64
		mutate     func(key string, o *fakeObject)
		deep       bool
		knownSHA   bool
		wantErr    error
		wantDelete bool // the bad remote copy is removed
	}{
		{name: "single put verified by head", wantErr: nil},
		{name: "multipart verified by head", partSize: 1024},
		{name: "known sha skips hashing", knownSHA: true},
		{name: "deep verify ok", deep: true},
		{name: "remote truncated", mutate: func(_ string, o *fakeObject) { o.data = o.data[:len(o.data)-1] }, wantErr: ErrVerifyFailed, wantDelete: true},
		{name: "remote sha metadata differs", mutate: func(_ string, o *fakeObject) { o.sha256 = hexSHA([]byte("other")) }, wantErr: ErrVerifyFailed, wantDelete: true},
		{name: "remote sha metadata missing", mutate: func(_ string, o *fakeObject) { o.sha256 = "" }, wantErr: ErrVerifyFailed, wantDelete: true},
		{name: "content flipped, metadata intact: head passes", mutate: flipByte},
		{name: "content flipped, metadata intact: deep catches it", mutate: flipByte, deep: true, wantErr: ErrVerifyFailed, wantDelete: true},
		{name: "multipart remote truncated", partSize: 1024, mutate: func(_ string, o *fakeObject) { o.data = o.data[:100] }, wantErr: ErrVerifyFailed, wantDelete: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := startFake(t)
			fs.mutate = tc.mutate
			s := fs.newS3(t, s3Opts{pathStyle: true, partSize: tc.partSize})
			path := writeTemp(t, data)
			opts := UploadOptions{VerifyDownload: tc.deep}
			if tc.knownSHA {
				opts.SHA256 = strings.ToUpper(hexSHA(data))
			}
			obj, err := Upload(context.Background(), s, path, "ws/acme/b.tar.zst", opts)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				_, stillThere := fs.object("ws/acme/b.tar.zst")
				if tc.wantDelete && stillThere {
					t.Fatal("unverified remote copy was left in place")
				}
				return
			}
			if err != nil {
				t.Fatalf("Upload: %v", err)
			}
			if obj.Size != int64(len(data)) || obj.SHA256 != hexSHA(data) || obj.Key != "ws/acme/b.tar.zst" {
				t.Fatalf("Upload object = %+v", obj)
			}
			if tc.deep && fs.count("GET") != 1 {
				t.Fatalf("deep verify made %d GETs, want 1", fs.count("GET"))
			}
			if !tc.deep && fs.count("GET") != 0 {
				t.Fatal("head-only verify downloaded the object")
			}
			if fs.count("HEAD") != 1 {
				t.Fatalf("HEADs = %d, want 1", fs.count("HEAD"))
			}
		})
	}
}

func flipByte(_ string, o *fakeObject) {
	d := append([]byte(nil), o.data...)
	d[10] ^= 0xff
	o.data = d
}

func TestUploadRefusesBadInput(t *testing.T) {
	fs := startFake(t)
	s := fs.newS3(t, s3Opts{pathStyle: true})
	ctx := context.Background()
	path := writeTemp(t, payload(10))
	if _, err := Upload(ctx, s, path, "../escape", UploadOptions{}); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("bad key: %v", err)
	}
	if _, err := Upload(ctx, s, filepath.Join(t.TempDir(), "missing"), "k", UploadOptions{}); err == nil {
		t.Fatal("missing file uploaded")
	}
	if _, err := Upload(ctx, s, t.TempDir(), "k", UploadOptions{}); err == nil {
		t.Fatal("directory uploaded")
	}
	if _, err := Upload(ctx, s, path, "k", UploadOptions{SHA256: "nothex"}); err == nil {
		t.Fatal("malformed SHA256 accepted")
	}
	// A declared SHA that does not match the file never commits anything.
	if _, err := Upload(ctx, s, path, "k", UploadOptions{SHA256: hexSHA([]byte("x"))}); !errors.Is(err, ErrContentMismatch) {
		t.Fatalf("wrong declared sha: %v", err)
	}
	if fs.count("PUT") != 0 {
		t.Fatal("a request was sent for refused input")
	}
}

func TestUploadRateLimited(t *testing.T) {
	fs := startFake(t)
	s := fs.newS3(t, s3Opts{pathStyle: true, partSize: 64 << 10})
	data := payload(512 << 10)
	clk := newFakeClock()
	_, err := Upload(context.Background(), s, writeTemp(t, data), "k", UploadOptions{BytesPerSecond: 64 << 10, Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	// 512 KiB at 64 KiB/s with a 64 KiB burst: at least 7 s of throttling.
	if got := clk.elapsed().Seconds(); got < 6.9 {
		t.Fatalf("throttled %.2fs, want >= 7s", got)
	}
}

func TestVerifyExported(t *testing.T) {
	fs := startFake(t)
	s := fs.newS3(t, s3Opts{pathStyle: true})
	ctx := context.Background()
	data := payload(100)
	if _, err := s.Put(ctx, "k", bytes.NewReader(data), 100, hexSHA(data)); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(ctx, s, "k", 100, strings.ToUpper(hexSHA(data)), true); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if _, err := Verify(ctx, s, "k", 101, hexSHA(data), false); !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("size mismatch: %v", err)
	}
	if _, err := Verify(ctx, s, "missing", 100, hexSHA(data), false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestDownload(t *testing.T) {
	data := payload(5000)
	cases := []struct {
		name    string
		mutate  func(string, *fakeObject)
		wantErr error
	}{
		{name: "ok"},
		{name: "content corrupted", mutate: flipByte, wantErr: ErrVerifyFailed},
		{name: "no sha metadata", mutate: func(_ string, o *fakeObject) { o.sha256 = "" }, wantErr: ErrVerifyFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := startFake(t)
			s := fs.newS3(t, s3Opts{prefix: "x"})
			ctx := context.Background()
			if _, err := s.Put(ctx, "b.tar.zst", bytes.NewReader(data), int64(len(data)), hexSHA(data)); err != nil {
				t.Fatal(err)
			}
			fs.mu.Lock()
			if tc.mutate != nil {
				tc.mutate("x/b.tar.zst", fs.objects["x/b.tar.zst"])
			}
			fs.mu.Unlock()

			dir := t.TempDir()
			dst := filepath.Join(dir, "restored.tar.zst")
			obj, err := Download(ctx, s, "b.tar.zst", dst, DownloadOptions{BytesPerSecond: 1 << 20, Clock: newFakeClock()})
			entries, _ := os.ReadDir(dir)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if len(entries) != 0 {
					t.Fatalf("failed download left files: %v", entries)
				}
				return
			}
			if err != nil {
				t.Fatalf("Download: %v", err)
			}
			got, _ := os.ReadFile(dst)
			if !bytes.Equal(got, data) || obj.SHA256 != hexSHA(data) {
				t.Fatal("downloaded content differs")
			}
			st, _ := os.Stat(dst)
			if st.Mode().Perm() != 0o600 {
				t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
			}
			if len(entries) != 1 {
				t.Fatalf("temp file left behind: %v", entries)
			}
		})
	}
	t.Run("missing key", func(t *testing.T) {
		fs := startFake(t)
		s := fs.newS3(t, s3Opts{pathStyle: true})
		if _, err := Download(context.Background(), s, "nope", filepath.Join(t.TempDir(), "f"), DownloadOptions{}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

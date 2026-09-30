package offsite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + i/7)
	}
	return b
}

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bundle.tar.zst")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestS3ConfigValidate(t *testing.T) {
	base := S3Config{Endpoint: "https://s3.eu-central-1.amazonaws.com", Region: "eu-central-1", Bucket: "crew-backups", AccessKeyID: "AK", SecretAccessKey: "SK"}
	cases := []struct {
		name    string
		mod     func(*S3Config)
		wantErr string
	}{
		{"aws virtual host", func(*S3Config) {}, ""},
		{"r2 auto region path style", func(c *S3Config) {
			c.Endpoint, c.Region, c.PathStyle = "https://acct.r2.cloudflarestorage.com", "auto", true
		}, ""},
		{"minio on LAN with explicit opt-in", func(c *S3Config) {
			c.Endpoint, c.PathStyle, c.AllowPrivateNetwork = "http://192.168.1.20:9000", true, true
		}, ""},
		{"loopback minio with opt-in", func(c *S3Config) {
			c.Endpoint, c.PathStyle, c.AllowPrivateNetwork = "http://127.0.0.1:9000", true, true
		}, ""},
		{"private IP without opt-in", func(c *S3Config) { c.Endpoint = "https://10.0.0.5" }, "literal private/internal IP"},
		{"plain http without opt-in", func(c *S3Config) { c.Endpoint = "http://s3.example.com" }, "scheme"},
		{"cloud metadata even with opt-in", func(c *S3Config) {
			c.Endpoint, c.AllowPrivateNetwork = "http://169.254.169.254", true
		}, "literal private/internal IP"},
		{"localhost refused", func(c *S3Config) {
			c.Endpoint, c.AllowPrivateNetwork = "http://localhost:9000", true
		}, "localhost"},
		{"userinfo refused", func(c *S3Config) { c.Endpoint = "https://ak:sk@s3.example.com" }, "userinfo"},
		{"endpoint with path", func(c *S3Config) { c.Endpoint = "https://s3.example.com/bucket" }, "no path"},
		{"missing bucket", func(c *S3Config) { c.Bucket = "" }, "bucket name"},
		{"dotted bucket needs path style", func(c *S3Config) { c.Bucket = "my.bucket" }, "path_style"},
		{"dotted bucket path style ok", func(c *S3Config) { c.Bucket, c.PathStyle = "my.bucket", true }, ""},
		{"missing secret", func(c *S3Config) { c.SecretAccessKey = "" }, "required"},
		{"bad prefix", func(c *S3Config) { c.Prefix = "a/../b" }, "prefix"},
		{"prefix slashes trimmed", func(c *S3Config) { c.Prefix = "/crewship/prod/" }, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			tc.mod(&c)
			_, err := NewS3(c)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("NewS3: %v", err)
				}
				return
			}
			if err == nil || !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want ErrInvalidConfig containing %q", err, tc.wantErr)
			}
		})
	}

	t.Run("part size floor", func(t *testing.T) {
		if _, err := NewS3(base, WithPartSize(1<<20)); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("err = %v, want ErrInvalidConfig", err)
		}
	})
}

func TestS3ConfigRedactsSecret(t *testing.T) {
	c := S3Config{Endpoint: "https://s3.example.com", Bucket: "bkt", AccessKeyID: "AKID", SecretAccessKey: "very-secret-key"}
	for _, s := range []string{fmt.Sprint(c), fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), fmt.Sprintf("%s", c)} {
		if strings.Contains(s, "very-secret-key") {
			t.Fatalf("formatted config leaks the secret: %s", s)
		}
		if !strings.Contains(s, "[redacted]") {
			t.Fatalf("formatted config does not mark the secret redacted: %s", s)
		}
	}
}

func TestValidateKey(t *testing.T) {
	cases := []struct {
		key string
		ok  bool
	}{
		{"workspace/acme/2026-09-30.tar.zst", true},
		{"reports/2026 Q3/a+b=c&d?e#f%g é.tar.zst", true},
		{".crewship-connection-test/abc", true},
		{"", false},
		{"/abs", false},
		{"a/../b", false},
		{"./a", false},
		{"a//b", false},
		{"dir/", false},
		{"a\nb", false},
		{"a\x00b", false},
		{string([]byte{0xff, 0xfe}), false},
		{strings.Repeat("k", 1025), false},
	}
	for _, tc := range cases {
		err := ValidateKey(tc.key)
		if tc.ok && err != nil {
			t.Errorf("ValidateKey(%q) = %v, want nil", tc.key, err)
		}
		if !tc.ok && !errors.Is(err, ErrInvalidKey) {
			t.Errorf("ValidateKey(%q) = %v, want ErrInvalidKey", tc.key, err)
		}
	}
}

// Every operation, in both addressing styles, with keys that need escaping.
func TestS3RoundTrip(t *testing.T) {
	keys := []string{
		"workspace/acme/bundle.tar.zst",
		"reports/2026 Q3/a+b=c&d?e#f%g é.tar.zst",
		"ünïcode/файл~(1)[2]*'!.bin",
	}
	for _, style := range []struct {
		name string
		path bool
	}{{"path-style", true}, {"virtual-host", false}} {
		for _, prefix := range []string{"", "crewship/prod"} {
			for _, key := range keys {
				t.Run(fmt.Sprintf("%s/prefix=%q/%s", style.name, prefix, key), func(t *testing.T) {
					fs := startFake(t)
					s := fs.newS3(t, s3Opts{pathStyle: style.path, prefix: prefix})
					ctx := context.Background()
					data := payload(3000)
					sum := hexSHA(data)

					put, err := s.Put(ctx, key, bytes.NewReader(data), int64(len(data)), sum)
					if err != nil {
						t.Fatalf("Put: %v", err)
					}
					if put.Key != key || put.Size != 3000 || put.SHA256 != sum || put.ETag == "" {
						t.Fatalf("Put object = %+v", put)
					}
					stored := key
					if prefix != "" {
						stored = prefix + "/" + key
					}
					if _, ok := fs.object(stored); !ok {
						t.Fatalf("fake has no object at %q", stored)
					}
					wantStyle := "virtual"
					if style.path {
						wantStyle = "path"
					}
					fs.mu.Lock()
					if fs.styles[wantStyle] == 0 || len(fs.styles) != 1 {
						t.Errorf("addressing styles seen = %v, want only %s", fs.styles, wantStyle)
					}
					fs.mu.Unlock()

					head, err := s.Head(ctx, key)
					if err != nil || head.Size != 3000 || head.SHA256 != sum || head.Modified.IsZero() {
						t.Fatalf("Head = %+v, %v", head, err)
					}
					rc, obj, err := s.Get(ctx, key)
					if err != nil {
						t.Fatalf("Get: %v", err)
					}
					got, _ := io.ReadAll(rc)
					_ = rc.Close()
					if !bytes.Equal(got, data) || obj.SHA256 != sum {
						t.Fatalf("Get returned %d bytes, sha %q", len(got), obj.SHA256)
					}
					list, err := s.List(ctx, "")
					if err != nil || len(list) != 1 || list[0].Key != key || list[0].Size != 3000 {
						t.Fatalf("List = %+v, %v", list, err)
					}
					if err := s.Delete(ctx, key); err != nil {
						t.Fatalf("Delete: %v", err)
					}
					if _, err := s.Head(ctx, key); !errors.Is(err, ErrNotFound) {
						t.Fatalf("Head after delete = %v, want ErrNotFound", err)
					}
					if _, _, err := s.Get(ctx, key); !errors.Is(err, ErrNotFound) {
						t.Fatalf("Get after delete = %v, want ErrNotFound", err)
					}
					if err := s.Delete(ctx, key); err != nil {
						t.Fatalf("Delete of a missing key = %v, want nil", err)
					}
				})
			}
		}
	}
}

func TestS3PutRejectsContentMismatch(t *testing.T) {
	data := payload(4000)
	cases := []struct {
		name     string
		partSize int64
		reader   io.Reader
		size     int64
		sum      string
	}{
		{"single: wrong sha", 0, bytes.NewReader(data), 4000, hexSHA(payload(10))},
		{"single: source short", 0, bytes.NewReader(data[:100]), 4000, hexSHA(data)},
		{"single: source long", 0, bytes.NewReader(data), 3000, hexSHA(data[:3000])},
		{"multipart: wrong sha", 1024, bytes.NewReader(data), 4000, hexSHA(payload(10))},
		{"multipart: source short", 1024, bytes.NewReader(data[:2500]), 4000, hexSHA(data)},
		{"multipart: source long", 1024, bytes.NewReader(data), 3000, hexSHA(data[:3000])},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := startFake(t)
			s := fs.newS3(t, s3Opts{pathStyle: true, partSize: tc.partSize})
			_, err := s.Put(context.Background(), "k.bin", tc.reader, tc.size, tc.sum)
			if !errors.Is(err, ErrContentMismatch) {
				t.Fatalf("Put err = %v, want ErrContentMismatch", err)
			}
			if _, ok := fs.object("k.bin"); ok {
				t.Fatal("a mismatched source was committed")
			}
			fs.mu.Lock()
			defer fs.mu.Unlock()
			if len(fs.uploads) != 0 {
				t.Fatalf("multipart upload left open: %d", len(fs.uploads))
			}
			if tc.partSize > 0 && fs.aborts != 1 {
				t.Fatalf("aborts = %d, want 1", fs.aborts)
			}
		})
	}
}

func TestS3Multipart(t *testing.T) {
	fs := startFake(t)
	s := fs.newS3(t, s3Opts{prefix: "p", partSize: 1024})
	data := payload(5000) // 4 full parts + 904
	sum := hexSHA(data)
	obj, err := s.Put(context.Background(), "big/bundle.tar.zst", bytes.NewReader(data), int64(len(data)), sum)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if obj.ETag != `"mp-5"` {
		t.Fatalf("ETag = %q, want the Complete answer", obj.ETag)
	}
	if n := fs.count("UPLOAD-PART"); n != 5 {
		t.Fatalf("parts = %d, want 5", n)
	}
	o, ok := fs.object("p/big/bundle.tar.zst")
	if !ok || !bytes.Equal(o.data, data) || o.sha256 != sum {
		t.Fatalf("stored object wrong (ok=%t)", ok)
	}
}

func TestS3MultipartPartSizeGrowsPastMaxParts(t *testing.T) {
	// 10 001 parts of MinPartSize would exceed S3's limit; the computed
	// part size must bring it back to <= 10 000 parts. Checked without
	// allocating: exercise the arithmetic Put uses.
	size := int64(maxParts+1) * MinPartSize
	part := MinPartSize
	if parts := (size + part - 1) / part; parts > maxParts {
		part = (size + maxParts - 1) / maxParts
		part = (part + (1<<20 - 1)) &^ (1<<20 - 1)
	}
	if n := (size + part - 1) / part; n > maxParts {
		t.Fatalf("%d parts of %d bytes, want <= %d", n, part, maxParts)
	}
}

func TestS3Retry(t *testing.T) {
	cases := []struct {
		name      string
		op        string
		status    int
		code      string
		delay     time.Duration
		failFirst int // attempts that fail
		wantErr   bool
		wantCount int
	}{
		{"transient 500 on PUT", "PUT", 500, "InternalError", 0, 2, false, 3},
		{"503 SlowDown on HEAD", "HEAD", 503, "SlowDown", 0, 1, false, 2},
		{"400 RequestTimeout is retried", "PUT", 400, "RequestTimeout", 0, 1, false, 2},
		{"429 is retried", "PUT", 429, "TooManyRequests", 0, 1, false, 2},
		{"timeout awaiting headers", "PUT", 0, "", 2 * time.Second, 1, false, 2},
		{"persistent 500 gives up", "PUT", 500, "InternalError", 0, 99, true, 4},
		{"403 is not retried", "PUT", 403, "AccessDenied", 0, 99, true, 1},
		{"transient 500 on a part", "UPLOAD-PART", 500, "InternalError", 0, 1, false, 6},
		{"transient 500 on complete", "COMPLETE-MULTIPART", 500, "InternalError", 0, 1, false, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := startFake(t)
			fs.inject = func(op string, attempt int, _ *http.Request) (int, string, time.Duration) {
				if op == tc.op && attempt <= tc.failFirst {
					return tc.status, tc.code, tc.delay
				}
				return 0, "", 0
			}
			s := fs.newS3(t, s3Opts{pathStyle: true, partSize: 1024, headerTimeout: 300 * time.Millisecond})
			data := payload(1000)
			if strings.Contains(tc.op, "PART") || strings.Contains(tc.op, "MULTIPART") {
				data = payload(4500) // 5 parts
			}
			ctx := context.Background()
			_, err := s.Put(ctx, "k", bytes.NewReader(data), int64(len(data)), hexSHA(data))
			if err == nil && tc.op == "HEAD" {
				_, err = s.Head(ctx, "k")
			}
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr %t", err, tc.wantErr)
			}
			if tc.wantErr {
				var se *S3Error
				if !errors.As(err, &se) || se.StatusCode != tc.status || se.Code != tc.code {
					t.Fatalf("err = %#v, want S3Error %d %s", err, tc.status, tc.code)
				}
			}
			if got := fs.count(tc.op); got != tc.wantCount {
				t.Fatalf("%s requests = %d, want %d", tc.op, got, tc.wantCount)
			}
		})
	}
}

func TestS3RetryHonoursContext(t *testing.T) {
	fs := startFake(t)
	fs.inject = func(string, int, *http.Request) (int, string, time.Duration) { return 500, "InternalError", 0 }
	s := fs.newS3(t, s3Opts{pathStyle: true})
	s.retry = RetryPolicy{MaxAttempts: 10, BaseDelay: time.Hour, MaxDelay: time.Hour}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := s.Head(ctx, "k")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("retry backoff ignored the context")
	}
}

func TestS3MultipartContextCancelAborts(t *testing.T) {
	fs := startFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs.onPart = func(n int, r *http.Request) {
		if n == 2 {
			cancel()
			<-r.Context().Done() // the client drops the in-flight part
		}
	}
	s := fs.newS3(t, s3Opts{pathStyle: true, partSize: 1024})
	data := payload(5000)
	_, err := s.Put(ctx, "big.bin", bytes.NewReader(data), int64(len(data)), hexSHA(data))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.aborts != 1 {
		t.Fatalf("aborts = %d, want 1", fs.aborts)
	}
	if len(fs.uploads) != 0 {
		t.Fatalf("open uploads = %d, want 0", len(fs.uploads))
	}
	if _, ok := fs.objects["big.bin"]; ok {
		t.Fatal("cancelled upload produced an object")
	}
	if fs.requests["COMPLETE-MULTIPART"] != 0 {
		t.Fatal("cancelled upload was completed")
	}
}

func TestS3MultipartFailureAborts(t *testing.T) {
	t.Run("part fails permanently", func(t *testing.T) {
		fs := startFake(t)
		fs.inject = func(op string, _ int, _ *http.Request) (int, string, time.Duration) {
			if op == "UPLOAD-PART" {
				return 403, "AccessDenied", 0
			}
			return 0, "", 0
		}
		s := fs.newS3(t, s3Opts{pathStyle: true, partSize: 1024})
		data := payload(3000)
		if _, err := s.Put(context.Background(), "k", bytes.NewReader(data), 3000, hexSHA(data)); err == nil {
			t.Fatal("Put succeeded")
		}
		fs.mu.Lock()
		defer fs.mu.Unlock()
		if fs.aborts != 1 || len(fs.uploads) != 0 {
			t.Fatalf("aborts = %d, open uploads = %d", fs.aborts, len(fs.uploads))
		}
	})
	t.Run("complete answers 200 with an Error body", func(t *testing.T) {
		fs := startFake(t)
		fs.completeError = true
		s := fs.newS3(t, s3Opts{pathStyle: true, partSize: 1024, attempts: 1})
		data := payload(3000)
		_, err := s.Put(context.Background(), "k", bytes.NewReader(data), 3000, hexSHA(data))
		var se *S3Error
		if !errors.As(err, &se) || se.Code != "InternalError" {
			t.Fatalf("err = %v, want S3Error InternalError", err)
		}
		fs.mu.Lock()
		defer fs.mu.Unlock()
		if fs.aborts != 1 {
			t.Fatalf("aborts = %d, want 1", fs.aborts)
		}
	})
}

func TestS3ListPagination(t *testing.T) {
	fs := startFake(t)
	fs.listPageSize = 2
	s := fs.newS3(t, s3Opts{prefix: "crewship"})
	ctx := context.Background()
	for _, k := range []string{"ws/a/1", "ws/a/2", "ws/a/3 x", "ws/b/1", "ws/a/4", "other/1"} {
		d := []byte(k)
		if _, err := s.Put(ctx, k, bytes.NewReader(d), int64(len(d)), hexSHA(d)); err != nil {
			t.Fatal(err)
		}
	}
	// An object outside the destination prefix must never be listed.
	fs.mu.Lock()
	fs.store("crewshipX/ws/a/9", []byte("x"), "", false, nil)
	fs.mu.Unlock()

	got, err := s.List(ctx, "ws/a/")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, o := range got {
		keys = append(keys, o.Key)
	}
	want := "ws/a/1,ws/a/2,ws/a/3 x,ws/a/4"
	if strings.Join(keys, ",") != want {
		t.Fatalf("keys = %v, want %s", keys, want)
	}
	if n := fs.count("LIST"); n != 2 {
		t.Fatalf("LIST requests = %d, want 2 (continuation)", n)
	}
	all, err := s.List(ctx, "")
	if err != nil || len(all) != 6 {
		t.Fatalf("List(\"\") = %d objects, %v; want 6", len(all), err)
	}
}

func TestS3Test(t *testing.T) {
	t.Run("ok leaves nothing behind", func(t *testing.T) {
		fs := startFake(t)
		s := fs.newS3(t, s3Opts{pathStyle: true, prefix: "pre"})
		if err := s.Test(context.Background()); err != nil {
			t.Fatalf("Test: %v", err)
		}
		fs.mu.Lock()
		defer fs.mu.Unlock()
		if len(fs.objects) != 0 {
			t.Fatalf("probe object left behind: %d", len(fs.objects))
		}
	})
	steps := []struct{ op, step string }{{"PUT", "upload"}, {"HEAD", "check"}, {"DELETE", "delete"}}
	for _, st := range steps {
		t.Run("denied "+st.op, func(t *testing.T) {
			fs := startFake(t)
			fs.inject = func(op string, _ int, _ *http.Request) (int, string, time.Duration) {
				if op == st.op {
					return 403, "AccessDenied", 0
				}
				return 0, "", 0
			}
			s := fs.newS3(t, s3Opts{pathStyle: true})
			err := s.Test(context.Background())
			if err == nil || !strings.Contains(err.Error(), "connection test: "+st.step) || !strings.Contains(err.Error(), "AccessDenied") {
				t.Fatalf("err = %v, want the %s step named", err, st.step)
			}
		})
	}
}

func TestS3WrongSecretIsRejected(t *testing.T) {
	fs := startFake(t)
	s := fs.newS3(t, s3Opts{pathStyle: true})
	s.creds.secretAccessKey = "wrong"
	_, _, err := s.Get(context.Background(), "k")
	var se *S3Error
	if !errors.As(err, &se) || se.Code != "SignatureDoesNotMatch" {
		t.Fatalf("err = %v, want SignatureDoesNotMatch", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatal("error carries the secret")
	}
	fs.mu.Lock()
	fs.authFail = nil // expected here; do not fail the cleanup check
	fs.mu.Unlock()
}

// The production transport: an explicit private-network opt-in reaches a
// loopback MinIO; without it the endpoint is refused before any dial.
func TestS3PrivateNetworkOptIn(t *testing.T) {
	f := newFakeS3(t)
	srv := httptest.NewServer(f)
	defer srv.Close()
	cfg := S3Config{Endpoint: srv.URL, Region: f.region, Bucket: f.bucket, AccessKeyID: f.accessKey, SecretAccessKey: f.secret, PathStyle: true}

	if _, err := NewS3(cfg); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("loopback endpoint without opt-in: err = %v, want ErrInvalidConfig", err)
	}
	cfg.AllowPrivateNetwork = true
	s, err := NewS3(cfg, WithRetryPolicy(RetryPolicy{MaxAttempts: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Test(context.Background()); err != nil {
		t.Fatalf("Test against loopback with opt-in: %v", err)
	}

}

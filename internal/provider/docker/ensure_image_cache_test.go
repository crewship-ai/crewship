package docker

// Tests for ensureImage's handling of local-only devcontainer cache images
// (crewship-cache:*). Such tags exist in NO registry, so a missing one must
// NOT be ImagePull'd (the pull can only ever fail with "pull access denied for
// crewship-cache"). Instead ensureImage returns the typed ErrCachedImageMissing
// so callers can route to reprovisioning.
//
// Like the rest of the package, these wire the Provider to an httptest server
// that mimics the slice of the Docker REST API ensureImage touches:
// ImageInspect (GET .../images/<ref>/json) and ImagePull (POST
// .../images/create). We assert both the returned error AND that ImagePull is
// never attempted for a missing cache image.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/dockerutil"
	"github.com/docker/docker/client"
)

// newEnsureImageProvider returns a Provider whose fake daemon records the
// number of ImageInspect and ImagePull calls. imagePresent controls whether
// the inspect reports the image as present locally (200) or absent (404).
func newEnsureImageProvider(t *testing.T, imagePresent bool) (p *Provider, pulls *int32) {
	t.Helper()

	var pullCount int32
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch {
		// GET /v*/images/<ref>/json — ImageInspect.
		case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
			if !imagePresent {
				http.Error(w, `{"message":"No such image"}`, http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"sha256:fake","RepoDigests":[],"Config":{"Env":[]}}`))

		// POST /v*/images/create — ImagePull. Should never fire for a missing
		// cache image; for a registry ref it returns a one-line pull stream.
		case strings.HasSuffix(r.URL.Path, "/images/create"):
			atomic.AddInt32(&pullCount, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"Pulling"}` + "\n"))

		default:
			w.WriteHeader(http.StatusOK)
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(handler))
	cli, err := client.NewClientWithOpts(
		client.WithHost(srv.URL),
		client.WithVersion("1.43"),
	)
	if err != nil {
		srv.Close()
		t.Fatalf("docker client: %v", err)
	}

	prov := &Provider{
		client: cli,
		cfg:    Config{},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		// 1ms HEAD timeout: the registry digest probe for a normal ref fails
		// fast and returns "" without depending on real network latency.
		digestResolver: dockerutil.NewDigestResolver(0, time.Millisecond),
	}
	t.Cleanup(func() {
		_ = cli.Close()
		srv.Close()
	})
	return prov, &pullCount
}

func TestEnsureImage_LocalCacheTag(t *testing.T) {
	tests := []struct {
		name      string
		ref       string
		present   bool
		wantErrIs error
		wantPulls int32
	}{
		{
			name:      "cache image missing -> sentinel, no pull",
			ref:       "crewship-cache:deadbeef",
			present:   false,
			wantErrIs: ErrCachedImageMissing,
			wantPulls: 0,
		},
		{
			name:      "cache image present -> no pull, no error",
			ref:       "crewship-cache:0d08da4b8ac3",
			present:   true,
			wantErrIs: nil,
			wantPulls: 0,
		},
		{
			name:      "registry image missing -> still pulls",
			ref:       "example.invalid/foo:bar",
			present:   false,
			wantErrIs: nil,
			wantPulls: 1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p, pulls := newEnsureImageProvider(t, tt.present)

			err := p.ensureImage(context.Background(), tt.ref)

			if tt.wantErrIs != nil {
				if !errors.Is(err, tt.wantErrIs) {
					t.Fatalf("err = %v, want errors.Is(%v)", err, tt.wantErrIs)
				}
				// The dead ref must be embedded so operators can see which
				// image needs rebuilding.
				if !strings.Contains(err.Error(), tt.ref) {
					t.Errorf("error %q should mention ref %q", err, tt.ref)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got := atomic.LoadInt32(pulls); got != tt.wantPulls {
				t.Errorf("ImagePull calls = %d, want %d", got, tt.wantPulls)
			}
		})
	}
}

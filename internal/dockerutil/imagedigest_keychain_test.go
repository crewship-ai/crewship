package dockerutil

import (
	"context"
	"io"
	"log"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// hostileDockerConfig points DOCKER_CONFIG at a temp dir whose config.json
// names a credential helper binary that does not exist. This is the shape of
// the developer-machine state that used to leak into these tests: the default
// keychain shells out to `docker-credential-<credsStore>` for *every* registry,
// including a loopback httptest one that wants no credentials at all.
func hostileDockerConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte(`{"credsStore":"crewship-nonexistent-helper"}`), 0o600); err != nil {
		t.Fatalf("write hostile docker config: %v", err)
	}
	t.Setenv("DOCKER_CONFIG", dir)
}

// startLocalRegistry stands up an in-process OCI registry and pushes a random
// image to it. Returns the full reference and the expected manifest digest.
func startLocalRegistry(t *testing.T) (ref string, digest v1.Hash) {
	t.Helper()
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse registry url: %v", err)
	}
	ref = u.Host + "/crewship/hermetic:latest"

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random image: %v", err)
	}
	tag, err := name.NewTag(ref)
	if err != nil {
		t.Fatalf("parse tag: %v", err)
	}
	// Push explicitly anonymously — the push side must not be affected by the
	// hostile keychain either, otherwise the test would fail for the wrong
	// reason.
	if err := remote.Write(tag, img, remote.WithAuth(authn.Anonymous)); err != nil {
		t.Fatalf("push image to local registry: %v", err)
	}
	digest, err = img.Digest()
	if err != nil {
		t.Fatalf("image digest: %v", err)
	}
	return ref, digest
}

// TestDigestResolver_HostileCredsStore is the regression test for #1251/#1256.
// A broken (or merely slow) host credential helper must not be able to turn a
// perfectly reachable registry into "digest unknown" — which silently degrades
// every ensureImage caller into "trust the stale local copy, skip the pull".
func TestDigestResolver_HostileCredsStore(t *testing.T) {
	hostileDockerConfig(t)
	ref, want := startLocalRegistry(t)

	r := NewDigestResolver(0, 0)
	got := r.Remote(context.Background(), ref)
	if got != want.String() {
		t.Fatalf("Remote(%q) = %q; want %q — a failing host credential helper must fall back to anonymous auth, not swallow the digest", ref, got, want.String())
	}
}

// TestDigestResolver_InjectedFailingKeychain covers the non-loopback path: even
// for a real-looking registry host, a keychain that errors must degrade to
// anonymous rather than abort the HEAD.
func TestDigestResolver_InjectedFailingKeychain(t *testing.T) {
	ref, want := startLocalRegistry(t)

	r := NewDigestResolver(0, 0, WithKeychain(errKeychain{}))
	got := r.Remote(context.Background(), ref)
	if got != want.String() {
		t.Fatalf("Remote(%q) = %q; want %q — an erroring keychain must fall back to anonymous", ref, got, want.String())
	}
}

type errKeychain struct{}

func (errKeychain) Resolve(authn.Resource) (authn.Authenticator, error) {
	return nil, errTestKeychain
}

var errTestKeychain = errKeychainType{}

type errKeychainType struct{}

func (errKeychainType) Error() string { return "keychain unavailable" }

func TestIsLoopbackRegistry(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:5000":            true,
		"localhost:5000":            true,
		"localhost":                 true,
		"[::1]:5000":                true,
		"::1":                       true,
		"127.9.9.9:5000":            true,
		"ghcr.io":                   false,
		"index.docker.io":           false,
		"registry.example.com:5000": false,
	}
	for host, want := range cases {
		if got := isLoopbackRegistry(host); got != want {
			t.Errorf("isLoopbackRegistry(%q) = %v; want %v", host, got, want)
		}
	}
}

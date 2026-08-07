package docker

// Supply-chain tests for ensureImage (#1825).
//
// Two properties, both previously unheld:
//
//  1. When the registry HEAD resolves a manifest digest, the PULL must name
//     that digest — not the mutable tag we started from. Asserted on the wire:
//     the moby client sends a digest reference as ?fromImage=<repo>&tag=<digest>
//     (see getAPITagFromNamedRef in moby/client), so the `tag` query parameter
//     is exactly the "did we pin?" oracle.
//
//  2. ensureImage must RETURN the digest the run will execute under, so the
//     caller can put it in the journal. Before this change it returned only an
//     error and the digest was computed, compared, and discarded.
//
// Plus the fail-closed decision for a provably-stale local copy, and the
// air-gap invariant that decision must not break.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

const (
	pinRemoteDigest = "sha256:aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111"
	pinLocalDigest  = "sha256:bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222"
)

// pullRecorder captures what each ImagePull actually asked the daemon for,
// and every ImageTag that followed.
type pullRecorder struct {
	mu    sync.Mutex
	calls []string // the ?tag= value of each /images/create
	tags  []string // "<source> -> <repo>:<tag>" for each /images/{src}/tag
}

func (r *pullRecorder) record(tag string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, tag)
}

func (r *pullRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// recordTag captures a POST /images/{source}/tag?repo=&tag=.
func (r *pullRecorder) recordTag(path string, q url.Values) {
	// path is ".../images/<source>/tag"; take the segment before "/tag".
	trimmed := strings.TrimSuffix(path, "/tag")
	source := trimmed[strings.Index(trimmed, "/images/")+len("/images/"):]
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tags = append(r.tags, source+" -> "+q.Get("repo")+":"+q.Get("tag"))
}

func (r *pullRecorder) tagSnapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.tags...)
}

// TestEnsureImage_PullsByDigestNotTag is the headline of #1825: with a
// resolvable remote digest and no local copy, the pull the daemon receives
// must be pinned to that digest.
func TestEnsureImage_PullsByDigestNotTag(t *testing.T) {
	t.Parallel()

	var rec pullRecorder
	// Local copy absent (inspect 404s) so ensureImage must pull.
	p, ref := newCovImageProvider(t, pinRemoteDigest, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			http.Error(w, `{"message":"No such image"}`, http.StatusNotFound)
		case strings.HasSuffix(path, "/images/create"):
			rec.record(r.URL.Query().Get("tag"))
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	prov, err := p.ensureImage(context.Background(), ref)
	if err != nil {
		t.Fatalf("ensureImage: %v", err)
	}

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("ImagePull calls = %d (%v), want 1", len(calls), calls)
	}
	if calls[0] != pinRemoteDigest {
		t.Errorf("pulled ?tag=%q, want the resolved digest %q — the pull is still by mutable tag",
			calls[0], pinRemoteDigest)
	}
	if prov.Digest != pinRemoteDigest {
		t.Errorf("ensureImage returned digest %q, want %q — the journal has nothing to record",
			prov.Digest, pinRemoteDigest)
	}
	if !prov.Verified {
		t.Error("a digest-addressed pull must report Verified — otherwise the audit row understates what we actually proved")
	}
}

// TestEnsureImage_PinnedPullRestoresTheLocalTag is a regression test for a
// defect this PR's first draft shipped to CI and only a REAL daemon caught
// (TestResilienceNetworkRecreate, "No such image: alpine:latest").
//
// `docker pull repo@sha256:…` fetches the manifest but does NOT create the
// `repo:tag` entry in the local image store. Everything downstream of
// ensureImage still addresses the image by tag — fixBindMountOwnership's init
// container, buildCrewContainerConfig, ContainerCreate, and the drift check in
// reconcileExistingContainer that compares inspect.Config.Image against the
// desired tag. Pinning the pull without re-tagging therefore left every one of
// them looking at an image that was on disk but unnamed.
//
// A fake daemon answers any inspect, so no unit test in this package could see
// it. This one asserts the fix at the protocol level: a pinned pull must be
// followed by POST /images/<pinned>/tag mapping it back to the original ref.
func TestEnsureImage_PinnedPullRestoresTheLocalTag(t *testing.T) {
	t.Parallel()

	var rec pullRecorder
	p, ref := newCovImageProvider(t, pinRemoteDigest, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/tag"):
			rec.recordTag(path, r.URL.Query())
			w.WriteHeader(http.StatusCreated)
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			http.Error(w, `{"message":"No such image"}`, http.StatusNotFound)
		case strings.HasSuffix(path, "/images/create"):
			rec.record(r.URL.Query().Get("tag"))
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	if _, err := p.ensureImage(context.Background(), ref); err != nil {
		t.Fatalf("ensureImage: %v", err)
	}

	tags := rec.tagSnapshot()
	if len(tags) != 1 {
		t.Fatalf("ImageTag calls = %d (%v), want exactly 1 — a digest-pinned pull leaves no local tag, so every downstream reference to %q breaks",
			len(tags), tags, ref)
	}
	// Source must be the digest reference we pulled; target the ref we started
	// from, so `ref` resolves again afterwards.
	wantSource := strings.TrimSuffix(ref, ":tag") + "@" + pinRemoteDigest
	if !strings.HasPrefix(tags[0], wantSource+" -> ") {
		t.Errorf("ImageTag source = %q, want it to start from the pinned ref %q", tags[0], wantSource)
	}
	if !strings.HasSuffix(tags[0], ":tag") {
		t.Errorf("ImageTag target = %q, want the ORIGINAL tag restored", tags[0])
	}
}

// TestEnsureImage_UnpinnedPullDoesNotRetag guards the other side: when the
// pull was already tag-addressed the daemon created the tag itself, and an
// extra ImageTag would be a pointless round-trip on every cold start.
func TestEnsureImage_UnpinnedPullDoesNotRetag(t *testing.T) {
	t.Parallel()

	var rec pullRecorder
	// registryDigest "" → manifest HEAD 404s → nothing to pin to.
	p, ref := newCovImageProvider(t, "", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/tag"):
			rec.recordTag(path, r.URL.Query())
			w.WriteHeader(http.StatusCreated)
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			http.Error(w, `{"message":"No such image"}`, http.StatusNotFound)
		case strings.HasSuffix(path, "/images/create"):
			rec.record(r.URL.Query().Get("tag"))
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	if _, err := p.ensureImage(context.Background(), ref); err != nil {
		t.Fatalf("ensureImage: %v", err)
	}
	if tags := rec.tagSnapshot(); len(tags) != 0 {
		t.Errorf("unpinned pull must not re-tag, got %v", tags)
	}
}

// TestEnsureImage_LocalMatch_ReturnsDigest covers the warm path: no pull is
// needed, but the digest the container will run under is still known and must
// still be reported, or every warm run would be unattributed in the audit
// trail.
func TestEnsureImage_LocalMatch_ReturnsDigest(t *testing.T) {
	t.Parallel()

	var rec pullRecorder
	var ref string
	p, ref := newCovImageProvider(t, pinRemoteDigest, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":          "sha256:local",
				"RepoDigests": []string{strings.TrimSuffix(ref, ":tag") + "@" + pinRemoteDigest},
			})
		case strings.HasSuffix(path, "/images/create"):
			rec.record(r.URL.Query().Get("tag"))
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	prov, err := p.ensureImage(context.Background(), ref)
	if err != nil {
		t.Fatalf("ensureImage: %v", err)
	}
	if got := rec.snapshot(); len(got) != 0 {
		t.Errorf("digest match must not pull, got %v", got)
	}
	if prov.Digest != pinRemoteDigest {
		t.Errorf("digest = %q, want %q", prov.Digest, pinRemoteDigest)
	}
	if !prov.Verified {
		t.Error("a local copy confirmed against a live registry answer is verified, pull or no pull")
	}
}

// TestEnsureImage_AirGapped_ReportsLocalDigest is the invariant the
// fail-closed decision below must not break. With NO reachable registry
// (manifest HEAD 404s → remote digest unknown) and a local copy present,
// ensureImage keeps running — and reports the digest that is actually on
// disk, so even an offline install has an attributable audit record.
func TestEnsureImage_AirGapped_ReportsLocalDigest(t *testing.T) {
	t.Parallel()

	var rec pullRecorder
	var ref string
	p, ref := newCovImageProvider(t, "" /* registry unreachable */, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":          "sha256:local",
				"RepoDigests": []string{strings.TrimSuffix(ref, ":tag") + "@" + pinLocalDigest},
			})
		case strings.HasSuffix(path, "/images/create"):
			rec.record(r.URL.Query().Get("tag"))
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	prov, err := p.ensureImage(context.Background(), ref)
	if err != nil {
		t.Fatalf("air-gapped install must not be broken by pinning: %v", err)
	}
	if got := rec.snapshot(); len(got) != 0 {
		t.Errorf("unreachable registry must not pull, got %v", got)
	}
	if prov.Digest != pinLocalDigest {
		t.Errorf("digest = %q, want the on-disk digest %q", prov.Digest, pinLocalDigest)
	}
	if prov.Verified {
		t.Error("nothing was confirmed against a registry here; reporting Verified would overstate an offline read-back")
	}
}

// TestEnsureImage_ProvablyStaleLocalCopy_FailsClosed is the fallback
// decision. Reaching the pull with a local copy present means the HEAD
// SUCCEEDED and the local RepoDigests did not contain the answer — i.e. we
// have proof the local image is not the one the tag names. Running it anyway
// (the pre-#1825 behaviour) executes a known-wrong image and writes a journal
// row claiming a digest that never ran.
func TestEnsureImage_ProvablyStaleLocalCopy_FailsClosed(t *testing.T) {
	t.Parallel()

	var ref string
	p, ref := newCovImageProvider(t, pinRemoteDigest, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			// Local copy exists but is a DIFFERENT manifest than the tag now
			// names — provable drift.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":          "sha256:local",
				"RepoDigests": []string{strings.TrimSuffix(ref, ":tag") + "@" + pinLocalDigest},
			})
		case strings.HasSuffix(path, "/images/create"):
			http.Error(w, `{"message":"toomanyrequests: rate limit exceeded"}`, http.StatusTooManyRequests)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	prov, err := p.ensureImage(context.Background(), ref)
	if err == nil {
		t.Fatalf("a provably-stale local copy must not be run silently; got %+v, nil error", prov)
	}
	if !strings.Contains(err.Error(), pinLocalDigest) || !strings.Contains(err.Error(), pinRemoteDigest) {
		t.Errorf("error must name both digests so an operator can see the drift: %v", err)
	}
	if !strings.Contains(err.Error(), staleImageEscapeHatchEnv) {
		t.Errorf("error must name the escape hatch %s: %v", staleImageEscapeHatchEnv, err)
	}
}

// TestEnsureImage_StaleEscapeHatch_ProceedsAndReportsWhatRan covers the
// deliberate opt-out. An operator who sets the env var accepts a stale image
// rather than a stopped fleet — but the digest we report is then the LOCAL
// one, because that is what actually executes. Reporting the resolved remote
// digest here would put a lie in the tamper-evident journal.
func TestEnsureImage_StaleEscapeHatch_ProceedsAndReportsWhatRan(t *testing.T) {
	t.Setenv(staleImageEscapeHatchEnv, "1")

	var ref string
	p, ref := newCovImageProvider(t, pinRemoteDigest, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":          "sha256:local",
				"RepoDigests": []string{strings.TrimSuffix(ref, ":tag") + "@" + pinLocalDigest},
			})
		case strings.HasSuffix(path, "/images/create"):
			http.Error(w, `{"message":"toomanyrequests: rate limit exceeded"}`, http.StatusTooManyRequests)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	prov, err := p.ensureImage(context.Background(), ref)
	if err != nil {
		t.Fatalf("escape hatch set: ensureImage must proceed, got %v", err)
	}
	if prov.Digest != pinLocalDigest {
		t.Errorf("digest = %q, want the LOCAL digest %q — the journal must record what ran, not what we wanted to run",
			prov.Digest, pinLocalDigest)
	}
	if prov.Verified {
		t.Error("an operator-accepted stale copy CONTRADICTS the registry; it is the least verified state there is")
	}
}

// TestEnsureImage_NoLocalCopy_PullFailure_StillFails guards the case the
// escape hatch must NOT cover: there is nothing on disk to fall back to, so a
// failed pull is fatal whether or not the operator opted out.
func TestEnsureImage_NoLocalCopy_PullFailure_StillFails(t *testing.T) {
	t.Setenv(staleImageEscapeHatchEnv, "1")

	p, ref := newCovImageProvider(t, pinRemoteDigest, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			http.Error(w, `{"message":"No such image"}`, http.StatusNotFound)
		case strings.HasSuffix(path, "/images/create"):
			http.Error(w, `{"message":"toomanyrequests: rate limit exceeded"}`, http.StatusTooManyRequests)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	if _, err := p.ensureImage(context.Background(), ref); err == nil {
		t.Fatal("no local copy + failed pull must be fatal even with the escape hatch set")
	}
}

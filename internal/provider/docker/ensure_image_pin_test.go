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
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/tag"):
			// A real daemon accepts the re-tag that follows a pinned pull, and
			// since #2006 a REFUSED one is no longer ignored — it downgrades the
			// provenance. This case is what keeps the fake honest about the
			// scenario the test is actually describing.
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

// TestEnsureImage_FailedRetag_DoesNotJournalTheRemoteDigest is #2006.
//
// The re-tag above is best-effort, and that is fine — what was NOT fine is what
// followed it: ensureImage returned {remoteDigest, Verified:true} without ever
// re-reading what `ref` points at. When a stale local copy of the tag exists,
// the pull lands the new manifest but the tag still names the OLD one, and
// everything downstream (ContainerCreate, the drift check) addresses the image
// by tag. The container ran the old image while the tamper-evident journal
// attested the new digest as verified — the one failure mode the whole pinning
// change exists to prevent.
//
// The rule is the one the escape-hatch branch already follows: report what will
// actually run. So after a failed re-tag we read the tag back and either
// confirm it carries the digest we pulled, or report the digest it does carry
// with Verified false — and record nothing at all when the tag is absent
// entirely, letting the real daemon error surface at ContainerCreate as before.
//
// The stale case additionally REFUSES to start (see
// TestEnsureImage_FailedRetag_StaleTagFailsClosed), so what it asserts here is
// the property that survives the escape hatch: an operator who opts out of the
// refusal still gets an honest audit row. The hatch relaxes execution, never
// the journal.
func TestEnsureImage_FailedRetag_DoesNotJournalTheRemoteDigest(t *testing.T) {
	// Not parallel: the stale case sets the escape-hatch env var, which is
	// process-global.
	tests := []struct {
		name       string
		localStale bool // does `ref` still resolve locally, to the OLD manifest?
		allowStale bool // operator set CREWSHIP_ALLOW_STALE_RUNTIME_IMAGE
		wantDigest string
		why        string
	}{
		{
			name:       "stale local tag under the escape hatch reports the digest that will run",
			localStale: true,
			allowStale: true,
			wantDigest: pinLocalDigest,
			why:        "the tag still names the old manifest, so that is what the container executes",
		},
		{
			name:       "absent local tag records nothing",
			localStale: false,
			wantDigest: "",
			why:        "nothing on disk answers to the tag; the honest audit row is no digest at all, and ContainerCreate surfaces the real daemon error",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.allowStale {
				t.Setenv(staleImageEscapeHatchEnv, "1")
			}

			var rec pullRecorder
			var ref string
			p, ref := newCovImageProvider(t, pinRemoteDigest, func(w http.ResponseWriter, r *http.Request) {
				path := r.URL.Path
				switch {
				case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/tag"):
					// The re-tag fails — a name conflict, a read-only image
					// store, a daemon that lost the layer mid-pull.
					rec.recordTag(path, r.URL.Query())
					http.Error(w, `{"message":"conflict: cannot restore tag"}`, http.StatusInternalServerError)
				case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
					if !tc.localStale {
						http.Error(w, `{"message":"No such image"}`, http.StatusNotFound)
						return
					}
					// Both the pre-pull inspect and the post-tag read-back see
					// the same stale local copy: the pull never renamed it.
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
				// Neither case may fail here: the absent-tag one because the
				// image is merely unnamed and the real daemon error belongs to
				// ContainerCreate, the stale one because the operator has
				// explicitly accepted the stale copy.
				t.Fatalf("ensureImage must not fail in this case: %v", err)
			}
			if tags := rec.tagSnapshot(); len(tags) != 1 {
				t.Fatalf("ImageTag calls = %d (%v), want exactly 1 — the test is not exercising the re-tag failure", len(tags), tags)
			}
			if prov.Digest == pinRemoteDigest {
				t.Errorf("ensureImage journalled the remote digest %q after the re-tag failed — %s", pinRemoteDigest, tc.why)
			}
			if prov.Digest != tc.wantDigest {
				t.Errorf("digest = %q, want %q — %s", prov.Digest, tc.wantDigest, tc.why)
			}
			if prov.Verified {
				t.Error("nothing confirmed that `ref` resolves to the pulled manifest, so Verified would attest a claim we did not check")
			}
		})
	}
}

// TestEnsureImage_FailedRetag_StaleTagFailsClosed is the second half of #2019:
// the two routes to a known-stale runtime image now answer the same way.
//
// A failed re-tag over a surviving stale copy of the tag lands in EXACTLY the
// state the #1825 pull-failure branch refuses to run: the registry answered, the
// manifest it names is not what `ref` resolves to, and ContainerCreate would
// start the old image. Journalling that honestly (#2006) fixed the audit record
// but left execution fail-open — one route stopped the fleet, the sibling route
// shrugged and started the wrong image with an Error log. An operator cannot be
// asked to reason about which of the two internal paths their host took.
//
// So it refuses, through the same env-var opt-out, and the error has to be
// actionable on its own: both digests, the escape-hatch name, and the fact that
// the correct manifest is already on disk and merely needs a name.
func TestEnsureImage_FailedRetag_StaleTagFailsClosed(t *testing.T) {
	t.Parallel()

	var ref string
	p, ref := newCovImageProvider(t, pinRemoteDigest, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/tag"):
			// The re-tag fails — a read-only image store, a daemon that lost
			// the layer mid-pull, a name conflict.
			http.Error(w, `{"message":"conflict: cannot restore tag"}`, http.StatusInternalServerError)
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			// Both the pre-pull inspect and the post-tag read-back see the same
			// stale local copy: the pull never renamed it.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":          "sha256:local",
				"RepoDigests": []string{strings.TrimSuffix(ref, ":tag") + "@" + pinLocalDigest},
			})
		case strings.HasSuffix(path, "/images/create"):
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	prov, err := p.ensureImage(context.Background(), ref)
	if err == nil {
		t.Fatalf("a stale tag that survived a failed re-tag must not be started silently; got %+v, nil error", prov)
	}
	if prov.Digest != "" || prov.Verified {
		t.Errorf("a refusal must report no provenance at all, got %+v — nothing ran, so nothing may be journalled", prov)
	}
	if !strings.Contains(err.Error(), pinLocalDigest) || !strings.Contains(err.Error(), pinRemoteDigest) {
		t.Errorf("error must name both digests so an operator can see the drift: %v", err)
	}
	if !strings.Contains(err.Error(), staleImageEscapeHatchEnv) {
		t.Errorf("error must name the escape hatch %s, or the operator has no way to act on it: %v", staleImageEscapeHatchEnv, err)
	}
	// The distinguishing fact of THIS route: the manifest we wanted is already
	// on disk, unnamed. The error must hand over the command that names it,
	// otherwise the only remedy an operator can infer is the escape hatch —
	// i.e. the insecure one.
	if !strings.Contains(err.Error(), "docker tag ") {
		t.Errorf("error must offer the re-tag that fixes this properly, not just the opt-out: %v", err)
	}
}

// TestEnsureImage_FailedRetag_StillVerifiesWhenTheTagCarriesTheDigest is the
// other half of #2006, and the only branch of the read-back that hands out
// Verified:true.
//
// A failed re-tag is not proof that the tag is wrong — only that WE did not put
// the right manifest there. The daemon may have named it for us, or a concurrent
// start of another crew on the same host may have won the race and created the
// tag a microsecond before ours, which is exactly what makes the re-tag fail
// with a conflict. In that state the tag already resolves to the manifest we
// pulled, so the guarantee the re-tag existed to create is present anyway and
// downgrading the audit row would understate what we actually proved.
//
// Left untested, the failure mode is silent and permanent: every host that
// loses that race writes `pinned: false` for a pull that was in fact pinned and
// confirmed, and the one field an auditor uses to separate "the daemon fetched
// the manifest we verified" from "we read a digest off local disk" stops
// meaning anything. It cannot be folded into the table above, whose whole
// thesis is that the remote digest must NOT be journalled.
func TestEnsureImage_FailedRetag_StillVerifiesWhenTheTagCarriesTheDigest(t *testing.T) {
	t.Parallel()

	var rec pullRecorder
	var mu sync.Mutex
	inspects := 0
	var ref string
	p, ref := newCovImageProvider(t, pinRemoteDigest, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/tag"):
			// "conflict: the tag is already in use" — the race, not a fault.
			rec.recordTag(path, r.URL.Query())
			http.Error(w, `{"message":"conflict: tag already exists"}`, http.StatusConflict)
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			mu.Lock()
			inspects++
			first := inspects == 1
			mu.Unlock()
			if first {
				// Pre-pull: nothing on disk, so ensureImage must pull.
				http.Error(w, `{"message":"No such image"}`, http.StatusNotFound)
				return
			}
			// Post-tag read-back: the tag exists and carries the manifest we
			// just pulled.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":          "sha256:pulled",
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
		t.Fatalf("a best-effort re-tag failure must not be fatal: %v", err)
	}
	if tags := rec.tagSnapshot(); len(tags) != 1 {
		t.Fatalf("ImageTag calls = %d (%v), want exactly 1 — the test is not exercising the re-tag failure", len(tags), tags)
	}
	if prov.Digest != pinRemoteDigest {
		t.Errorf("digest = %q, want %q — the tag resolves to the manifest we pulled, so that is what runs",
			prov.Digest, pinRemoteDigest)
	}
	if !prov.Verified {
		t.Error("the tag carries the digest the registry reported and the pull was digest-addressed; reporting Verified false here writes `pinned: false` for a pull that WAS pinned and confirmed")
	}
}

// TestEnsureImage_AlreadyDigestRefIsNotRetagged covers the operator who wrote
// `image: repo@sha256:…` straight into a crew manifest. There is no tag to
// restore, and the Docker API refuses to create one from a digest reference
// ("refusing to create a tag with a digest reference") — so attempting it would
// log a warning on every single container start for the users who are doing the
// MOST correct thing. Skip it.
func TestEnsureImage_AlreadyDigestRefIsNotRetagged(t *testing.T) {
	t.Parallel()

	var rec pullRecorder
	p, tagRef := newCovImageProvider(t, pinRemoteDigest, func(w http.ResponseWriter, r *http.Request) {
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

	digestRef := strings.TrimSuffix(tagRef, ":tag") + "@" + pinRemoteDigest

	prov, err := p.ensureImage(context.Background(), digestRef)
	if err != nil {
		t.Fatalf("ensureImage: %v", err)
	}
	if tags := rec.tagSnapshot(); len(tags) != 0 {
		t.Errorf("a digest-addressed ref has no tag to restore; got ImageTag %v", tags)
	}
	if prov.Digest != pinRemoteDigest {
		t.Errorf("digest = %q, want %q", prov.Digest, pinRemoteDigest)
	}
	if !prov.Verified {
		t.Error("an operator-pinned digest ref is the strongest case there is; it must report Verified")
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

// Package dockerutil contains small shared helpers for code that drives the
// Docker API. Kept intentionally narrow — only utilities proven useful by
// more than one caller land here, to avoid a kitchen-sink "common" package.
package dockerutil

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// DefaultDigestTTL is the default lifetime of a cached HEAD-manifest result.
// Tagged images are essentially immutable inside their manifest window, so
// 24h is plenty. Callers can override via NewDigestResolver.
const DefaultDigestTTL = 24 * time.Hour

// DefaultHeadTimeout bounds a single registry HEAD request. Keeps Ensure*Image
// code paths responsive when the registry is slow or the network is degraded
// (callers fall back to the local image copy on timeout).
const DefaultHeadTimeout = 5 * time.Second

// DigestResolver caches remote manifest digests and answers "does the local
// RepoDigests list already contain the remote digest?" queries. One instance
// per long-lived component (provisioner, runtime provider). Safe for
// concurrent use.
//
// Why shared: both the devcontainer Provisioner and the Docker runtime
// Provider need identical "is my local :latest still fresh?" semantics. Before
// extraction each carried a bit-identical copy of the cache + HEAD helpers.
type DigestResolver struct {
	ttl         time.Duration
	headTimeout time.Duration

	mu    sync.RWMutex
	cache map[string]digestEntry
}

type digestEntry struct {
	digest    string
	fetchedAt time.Time
}

// NewDigestResolver returns a DigestResolver using the default TTL + HEAD
// timeout. Pass 0 for either to use the package defaults.
func NewDigestResolver(ttl, headTimeout time.Duration) *DigestResolver {
	if ttl <= 0 {
		ttl = DefaultDigestTTL
	}
	if headTimeout <= 0 {
		headTimeout = DefaultHeadTimeout
	}
	return &DigestResolver{
		ttl:         ttl,
		headTimeout: headTimeout,
		cache:       make(map[string]digestEntry),
	}
}

// Remote returns the manifest digest of ref from its registry. Uses the cache
// when a previous lookup for the same ref is still within TTL. Empty results
// (auth failure, parse failure, timeout) are NOT cached — callers may retry
// on the next call. Best-effort: returns "" on any failure so callers treat
// it as "unknown, fall back to local copy".
func (r *DigestResolver) Remote(ctx context.Context, ref string) string {
	r.mu.RLock()
	entry, ok := r.cache[ref]
	r.mu.RUnlock()
	if ok && time.Since(entry.fetchedAt) < r.ttl {
		return entry.digest
	}
	fresh := r.fetchRemote(ctx, ref)
	if fresh != "" {
		r.mu.Lock()
		r.cache[ref] = digestEntry{digest: fresh, fetchedAt: time.Now()}
		r.mu.Unlock()
	}
	return fresh
}

// fetchRemote performs the HEAD request directly (no cache). Isolated for
// testability of the caching layer.
func (r *DigestResolver) fetchRemote(ctx context.Context, ref string) string {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return ""
	}
	headCtx, cancel := context.WithTimeout(ctx, r.headTimeout)
	defer cancel()
	desc, err := remote.Head(parsed,
		remote.WithContext(headCtx),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
	)
	if err != nil {
		return ""
	}
	return desc.Digest.String()
}

// RepoDigestsContain reports whether any entry in repoDigests refers to the
// given manifest digest. Each entry is formatted "<repo>@sha256:<hex>".
// Empty digest argument never matches — callers use "" to represent "unknown"
// and an unknown digest must not produce a spurious local-is-fresh signal.
func RepoDigestsContain(repoDigests []string, digest string) bool {
	if digest == "" {
		return false
	}
	for _, rd := range repoDigests {
		at := strings.LastIndex(rd, "@")
		if at > 0 && rd[at+1:] == digest {
			return true
		}
	}
	return false
}

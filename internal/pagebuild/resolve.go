package pagebuild

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	pageprofile "github.com/crewship-ai/crewship/tools/pages-build"
)

// Machine-readable refusal codes for clients (seed, UI) that must branch on
// "not enabled" rather than match the sentence (#3108).
const (
	CodeNotEnabled           = "page_apps_not_enabled"
	CodeRuntimeOriginMissing = "page_runtime_origin_missing"
)

// EnableCommand is the one remedy every refusal names.
const EnableCommand = "crewship page enable"

var ErrNotEnabled = errors.New("Pages Apps are not enabled: run `" + EnableCommand + "` on the server host")

// ImageResolver picks the tools image for a build. An explicitly configured
// image always wins. Otherwise the image `crewship page enable` recorded in
// RecordDir for this server's build context fingerprint is used, after
// verifying its ID, exact tag and label (RecordedCompilerImage): the record is
// the operator's opt-in, so a server without a verified record never starts a
// Pages container. The chosen value is always an immutable ID; builds still
// never pull.
type ImageResolver struct {
	Explicit string
	// RecordDir is page_projects_path, where `crewship page enable` writes
	// BuildImageRecordFile.
	RecordDir string
	// Lookup returns the verified image (first element); nil uses
	// RecordedCompilerImage for RecordDir and this binary's fingerprint.
	// Tests replace it.
	Lookup func(context.Context) ([]string, error)
	// TTL bounds how long a found image is reused; zero means 10s.
	// NegativeTTL bounds how long "not enabled" is reused; zero means 2s, so
	// `crewship page enable` is seen within seconds without a restart while
	// a server that is not enabled does not run `docker image ls` per request.
	TTL         time.Duration
	NegativeTTL time.Duration
	// LookupTimeout bounds one lookup; zero means 5s. A hung Docker daemon
	// then fails requests quickly instead of queueing them behind the lock.
	LookupTimeout time.Duration
	Now           func() time.Time

	mu      sync.Mutex
	image   string
	err     error
	checked time.Time
}

func (r *ImageResolver) Image(ctx context.Context) (string, error) {
	if r.Explicit != "" {
		return r.Explicit, ValidateImage(r.Explicit)
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	ttl := durationOr(r.TTL, 10*time.Second)
	negativeTTL := durationOr(r.NegativeTTL, 2*time.Second)
	// The lock makes concurrent requests share one lookup instead of each
	// starting a docker process.
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.checked.IsZero() {
		age := now().Sub(r.checked)
		if (r.err == nil && age < ttl) || (r.err != nil && age < negativeTTL) {
			return r.image, r.err
		}
	}
	lookup := r.Lookup
	if lookup == nil {
		lookup = func(ctx context.Context) ([]string, error) {
			id, err := RecordedCompilerImage(ctx, r.RecordDir, pageprofile.ImageFingerprint())
			if err != nil {
				return nil, err
			}
			return []string{id}, nil
		}
	}
	// Detached from the request: one caller's disconnect must not be cached
	// as "not enabled" for everyone else.
	lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), durationOr(r.LookupTimeout, 5*time.Second))
	ids, err := lookup(lookupCtx)
	cancel()
	r.checked = now()
	switch {
	case errors.Is(err, ErrNotEnabled):
		r.image, r.err = "", err
	case err != nil:
		r.image, r.err = "", fmt.Errorf("%w (%v)", ErrNotEnabled, err)
	case len(ids) == 0:
		r.image, r.err = "", ErrNotEnabled
	default:
		r.image, r.err = ids[0], nil
	}
	return r.image, r.err
}

func durationOr(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

// Invalidate forgets the cached lookup, e.g. after a build found the image gone.
func (r *ImageResolver) Invalidate() {
	r.mu.Lock()
	r.checked = time.Time{}
	r.mu.Unlock()
}

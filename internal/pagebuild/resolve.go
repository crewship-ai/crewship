package pagebuild

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
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

// LabeledImages lists local tools images built by `crewship page enable`,
// newest first, as immutable sha256 IDs. An empty fingerprint lists images of
// every profile (used to remove them all).
func LabeledImages(ctx context.Context, fingerprint string) ([]string, error) {
	filter := "label=" + pageprofile.ImageLabel
	if fingerprint != "" {
		filter += "=" + fingerprint
	}
	// --all: with the containerd image store an untagged image (a plain
	// `docker build --label …`) is otherwise invisible to `image ls`.
	out, err := exec.CommandContext(ctx, "docker", "image", "ls", "--all", "--quiet", "--no-trunc", "--filter", filter).Output()
	if err != nil {
		return nil, fmt.Errorf("list Pages tools images: %w", err)
	}
	var ids []string
	seen := map[string]bool{}
	for _, id := range strings.Fields(string(out)) {
		// One ID repeats once per tag; keep Docker's newest-first order.
		if seen[id] || ValidateImage(id) != nil {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}

// ImageResolver picks the tools image for a build. An explicitly configured
// image always wins. Otherwise the image labelled with this server's profile
// fingerprint is used: its presence is the operator's opt-in, so a server
// without it never starts a Pages container. The chosen value is always an
// immutable ID; builds still never pull.
type ImageResolver struct {
	Explicit string
	// Lookup lists candidate IDs newest first; nil uses LabeledImages for
	// this binary's fingerprint. Tests replace it.
	Lookup func(context.Context) ([]string, error)
	// TTL bounds how long a lookup result is reused; zero means 10s. A
	// `crewship page enable` is therefore seen without a restart.
	TTL time.Duration
	Now func() time.Time

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
	ttl := r.TTL
	if ttl == 0 {
		ttl = 10 * time.Second
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.checked.IsZero() && now().Sub(r.checked) < ttl {
		return r.image, r.err
	}
	lookup := r.Lookup
	if lookup == nil {
		lookup = func(ctx context.Context) ([]string, error) {
			return LabeledImages(ctx, pageprofile.Fingerprint())
		}
	}
	ids, err := lookup(ctx)
	r.checked = now()
	switch {
	case err != nil:
		r.image, r.err = "", fmt.Errorf("%w (%v)", ErrNotEnabled, err)
	case len(ids) == 0:
		r.image, r.err = "", ErrNotEnabled
	default:
		r.image, r.err = ids[0], nil
	}
	return r.image, r.err
}

// Invalidate forgets the cached lookup, e.g. after a build found the image gone.
func (r *ImageResolver) Invalidate() {
	r.mu.Lock()
	r.checked = time.Time{}
	r.mu.Unlock()
}

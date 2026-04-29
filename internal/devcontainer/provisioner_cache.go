package devcontainer

// Image-cache memoization + content-addressable hashing for cached
// images. Extracted from provisioner.go for readability — pure data
// flow, no install logic.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/docker/docker/api/types/image"
)

type imageListCacheEntry struct {
	images    []image.Summary
	fetchedAt time.Time
}

const imageListTTL = 60 * time.Second

const provisionerSchemaVersion = "v1"

// cacheImageTag returns the Docker image tag for a given config hash.

func cacheImageTag(configHash string) string {
	short := configHash
	if len(short) > 12 {
		short = short[:12]
	}
	return "crewship-cache:" + short
}

// configHash computes a deterministic SHA-256 hash from the base image,
// devcontainer config, and mise config.
//
// Canonical JSON representation: Config.MarshalJSON emits a map with sorted
// keys (Go's json package sorts map[string]X keys). miseConfig is re-parsed
// and re-marshaled so that whitespace / key-order differences in the stored
// JSON produce the same hash. Unparseable mise config falls back to raw.
//
// Note: changing the canonicalization changes existing hashes once; users

// will re-provision on next run. Document in CHANGELOG when bumped.
func configHash(baseImage string, cfg *Config, miseConfig string) string {
	h := sha256.New()
	h.Write([]byte(provisionerSchemaVersion))
	h.Write([]byte("|"))
	h.Write([]byte(baseImage))
	h.Write([]byte("|"))

	// Canonicalize cfg via hashRelevantMap, which omits runtime-only fields
	// like postStartCommand. Tweaking a start hook must not invalidate the
	// cached image — only image content should.
	cfgCanon, _ := json.Marshal(cfg.hashRelevantMap())
	h.Write(cfgCanon)
	h.Write([]byte("|"))

	// Canonicalize miseConfig by parsing + re-marshaling. Falls back to raw
	// bytes if the config is not valid JSON.
	if miseConfig != "" {
		var miseData any
		if err := json.Unmarshal([]byte(miseConfig), &miseData); err == nil {
			sortedMise, _ := json.Marshal(miseData)
			h.Write(sortedMise)
		} else {
			h.Write([]byte(miseConfig))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// IsCached reports whether a cached image for the given config hash exists.

func (p *Provisioner) IsCached(ctx context.Context, hash string) (bool, error) {
	tag := cacheImageTag(hash)
	return p.imageExists(ctx, tag)
}

// imageExists checks whether a locally available image matches the given
// reference (e.g. "crewship-cache:a1b2c3d4e5f6"). Uses the cached image list
// when fresh.

func (p *Provisioner) imageExists(ctx context.Context, ref string) (bool, error) {
	imgs, err := p.listImages(ctx)
	if err != nil {
		return false, fmt.Errorf("listing images: %w", err)
	}
	for _, img := range imgs {
		for _, tag := range img.RepoTags {
			if tag == ref {
				return true, nil
			}
		}
	}
	return false, nil
}

// listImages returns the local image summaries, using a short-lived cache to
// avoid hammering the Docker daemon. Cache is invalidated on our own
// ImagePull/ImageCommit calls and on TTL expiry. External `docker rmi` is
// picked up after the TTL window (default 60s).

func (p *Provisioner) listImages(ctx context.Context) ([]image.Summary, error) {
	p.imageListMu.Lock()
	defer p.imageListMu.Unlock()

	if p.imageListCache.images != nil && time.Since(p.imageListCache.fetchedAt) < imageListTTL {
		return p.imageListCache.images, nil
	}
	imgs, err := p.docker.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return nil, err
	}
	p.imageListCache = imageListCacheEntry{images: imgs, fetchedAt: time.Now()}
	return imgs, nil
}

// invalidateImageListCache forces the next listImages call to hit the Docker
// daemon. Call after any operation that mutates the local image set
// (ImagePull, ImageCommit, ImageRemove).

func (p *Provisioner) invalidateImageListCache() {
	p.imageListMu.Lock()
	p.imageListCache = imageListCacheEntry{}
	p.imageListMu.Unlock()
}

// Provision builds a cached image by installing devcontainer features and
// running post-create commands in a temporary container. If a cached image
// already exists, it returns immediately.

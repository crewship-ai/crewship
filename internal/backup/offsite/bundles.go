package offsite

// A bundle whose environment layers live in the shared store (not inline)
// is copied off-site with them, and deleted without taking a layer another
// remote bundle still needs:
//
//	<bundle key>                        the bundle, as Upload writes it
//	<bundle key>.environments.json      the layer digests it needs (EnvironmentRefs)
//	environments/blobs/sha256/<hex>     each layer once, shared by every bundle
//
// The refs object is the remote reference count: DeleteBundle keeps every
// blob some other refs object names, so the destination alone — no local
// database — decides what may go. It is written BEFORE the layers, so a
// concurrent DeleteBundle that lists refs after it sees them; refMu makes
// sure no DeleteBundle that listed earlier is still deleting while an upload
// decides a layer is already present.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// EnvironmentRefsSuffix names the refs object beside a bundle.
const EnvironmentRefsSuffix = ".environments.json"

// maxRefsBytes bounds a refs object read back from a destination.
const maxRefsBytes = 4 << 20

// EnvironmentRefs is the refs object's content.
type EnvironmentRefs struct {
	Bundle string   `json:"bundle"`
	Blobs  []string `json:"blobs"`
}

// EnvironmentRefsKey is the refs object's key for a bundle key.
func EnvironmentRefsKey(bundleKey string) string { return bundleKey + EnvironmentRefsSuffix }

// refMu serialises writing refs against DeleteBundle's list-and-delete.
var refMu sync.Mutex

// BundleTransfer reports one UploadBundle / DownloadBundle.
type BundleTransfer struct {
	Bundle       Object              `json:"bundle"`
	Environments EnvironmentTransfer `json:"environments"`
	// Blobs are the digests the bundle needs from the environment store.
	Blobs []string `json:"blobs"`
}

// UploadBundle copies a bundle and, when digests is not empty, the layers it
// needs from the local environment store: the refs object first, then every
// layer the destination does not already hold (Head with the same size and
// checksum), then the bundle — each verified like Upload. A layer missing
// locally fails the copy (the bundle off-site could not be restored). On
// failure the refs object is removed again, so it protects nothing.
func UploadBundle(ctx context.Context, dst Destination, store LocalBlobs, localPath, key string, digests []string, opts UploadOptions) (BundleTransfer, error) {
	out := BundleTransfer{Blobs: uniqueSorted(digests)}
	if len(out.Blobs) > 0 {
		if store == nil {
			return out, errors.New("offsite: the bundle needs environment layers and no local store was given")
		}
		refsKey := EnvironmentRefsKey(key)
		if err := putRefs(ctx, dst, refsKey, EnvironmentRefs{Bundle: key, Blobs: out.Blobs}); err != nil {
			return out, err
		}
		fail := func(err error) (BundleTransfer, error) {
			delCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			_ = dst.Delete(delCtx, refsKey)
			cancel()
			return out, err
		}
		o := opts
		o.SHA256 = ""
		env, err := UploadEnvironmentBlobs(ctx, dst, store, "", out.Blobs, o)
		out.Environments = env
		if err != nil {
			return fail(fmt.Errorf("offsite: environment layers: %w", err))
		}
		if env.Missing > 0 {
			return fail(fmt.Errorf("offsite: %d environment layer(s) the bundle needs are not in the local store", env.Missing))
		}
		obj, err := Upload(ctx, dst, localPath, key, opts)
		if err != nil {
			return fail(err)
		}
		out.Bundle = obj
		return out, nil
	}
	obj, err := Upload(ctx, dst, localPath, key, opts)
	out.Bundle = obj
	return out, err
}

func putRefs(ctx context.Context, dst Destination, key string, refs EnvironmentRefs) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	b, err := json.Marshal(refs)
	if err != nil {
		return err
	}
	h := sha256.Sum256(b)
	sum := hex.EncodeToString(h[:])
	refMu.Lock()
	defer refMu.Unlock()
	put, err := dst.Put(ctx, key, bytes.NewReader(b), int64(len(b)), sum)
	if err != nil {
		return fmt.Errorf("offsite: write environment refs: %w", err)
	}
	if _, err := verify(ctx, dst, key, int64(len(b)), sum, put.Checksum, false, nil); err != nil {
		return err
	}
	return nil
}

// ReadEnvironmentRefs reads a bundle's refs object; a bundle without one
// (self-contained, or written before layers were shipped) reads as none.
func ReadEnvironmentRefs(ctx context.Context, dst Destination, bundleKey string) ([]string, error) {
	rc, _, err := dst.Get(ctx, EnvironmentRefsKey(bundleKey))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, maxRefsBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxRefsBytes {
		return nil, fmt.Errorf("offsite: environment refs of %s are larger than %d bytes", bundleKey, maxRefsBytes)
	}
	var refs EnvironmentRefs
	if err := json.Unmarshal(b, &refs); err != nil {
		return nil, fmt.Errorf("offsite: environment refs of %s: %w", bundleKey, err)
	}
	for _, d := range refs.Blobs {
		if _, err := EnvironmentBlobKey("", d); err != nil {
			return nil, err
		}
	}
	return refs.Blobs, nil
}

// DownloadBundle fetches a bundle into localPath and the environment layers
// its refs object names into store (those it lacks), each verified. The
// caller records the refs locally (backup.AddEnvironmentRefs). A layer the
// destination does not hold fails the fetch: the bundle could not restore
// its environments.
func DownloadBundle(ctx context.Context, dst Destination, store LocalBlobs, key, localPath string, opts DownloadOptions) (BundleTransfer, error) {
	var out BundleTransfer
	blobs, err := ReadEnvironmentRefs(ctx, dst, key)
	if err != nil {
		return out, err
	}
	out.Blobs = blobs
	if len(blobs) > 0 {
		if store == nil {
			return out, errors.New("offsite: the bundle needs environment layers and no local store was given")
		}
		env, err := DownloadEnvironmentBlobs(ctx, dst, store, "", blobs, opts)
		out.Environments = env
		if err != nil {
			return out, fmt.Errorf("offsite: environment layers: %w", err)
		}
		if env.Missing > 0 {
			return out, fmt.Errorf("offsite: %d environment layer(s) the bundle needs are missing at the destination", env.Missing)
		}
	}
	obj, err := Download(ctx, dst, key, localPath, opts)
	out.Bundle = obj
	return out, err
}

// DeleteBundle removes a bundle, its refs object, and every layer it needed
// that no other bundle's refs object at the destination still names.
// Returns how many layers went. A missing bundle is not an error.
func DeleteBundle(ctx context.Context, dst Destination, key string) (int, error) {
	refMu.Lock()
	defer refMu.Unlock()
	mine, err := ReadEnvironmentRefs(ctx, dst, key)
	if err != nil {
		return 0, err
	}
	if err := dst.Delete(ctx, key); err != nil && !errors.Is(err, ErrNotFound) {
		return 0, err
	}
	if len(mine) == 0 {
		return 0, nil
	}
	needed, err := remoteRefs(ctx, dst, EnvironmentRefsKey(key))
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, d := range mine {
		if needed[d] {
			continue
		}
		bk, err := EnvironmentBlobKey("", d)
		if err != nil {
			return removed, err
		}
		if err := dst.Delete(ctx, bk); err != nil && !errors.Is(err, ErrNotFound) {
			return removed, err
		}
		removed++
	}
	// The refs go last: until every layer it alone needed is gone, a crash
	// leaves the refs object to say which layers are the bundle's.
	if err := dst.Delete(ctx, EnvironmentRefsKey(key)); err != nil && !errors.Is(err, ErrNotFound) {
		return removed, err
	}
	return removed, nil
}

// remoteRefs is the union of every refs object at the destination but skip.
func remoteRefs(ctx context.Context, dst Destination, skip string) (map[string]bool, error) {
	objs, err := dst.List(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("offsite: list refs: %w", err)
	}
	out := map[string]bool{}
	for _, o := range objs {
		if o.Key == skip || !strings.HasSuffix(o.Key, EnvironmentRefsSuffix) {
			continue
		}
		blobs, err := ReadEnvironmentRefs(ctx, dst, strings.TrimSuffix(o.Key, EnvironmentRefsSuffix))
		if err != nil {
			return nil, err
		}
		for _, d := range blobs {
			out[d] = true
		}
	}
	return out, nil
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

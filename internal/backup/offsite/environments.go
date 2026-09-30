package offsite

// Complete container environments (Track E) keep their image layers in a
// content-addressed store beside the backups directory, shared by every
// bundle that needs them, instead of inside each bundle. A bundle copied
// off-site on its own would then be missing its layers. These two calls ship
// and fetch them: every blob under <prefix>environments/blobs/sha256/<hex>,
// uploaded once however many bundles reference it, verified like a bundle.
//
// The list of digests a bundle needs is backup.BundleEnvironmentBlobs(m) (it
// is in the unencrypted manifest). A bundle written with inline layers
// (CREWSHIP_BACKUP_ENV_INLINE=1) is self-contained and needs neither call.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LocalBlobs is the local environment store as this package needs it:
// where a digest's file is (backup.EnvironmentStore satisfies it).
type LocalBlobs interface {
	BlobPath(digest string) (string, error)
}

// EnvironmentTransfer reports one Upload/DownloadEnvironmentBlobs call.
type EnvironmentTransfer struct {
	Transferred int   `json:"transferred"`
	Present     int   `json:"present"` // already at the destination (or locally)
	Missing     int   `json:"missing"` // not in the source at all
	Bytes       int64 `json:"bytes"`
}

// EnvironmentBlobKey is the off-site key of one layer blob.
func EnvironmentBlobKey(prefix, digest string) (string, error) {
	h, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || !isHexSHA256(h) {
		return "", fmt.Errorf("offsite: %q is not a sha256 digest", digest)
	}
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return prefix + "environments/blobs/sha256/" + h, nil
}

// UploadEnvironmentBlobs uploads every digest the destination does not
// already hold with the same size and checksum. Each upload is verified
// (Upload); a blob the local store lacks is counted as missing, and the
// call fails on the first transfer error.
func UploadEnvironmentBlobs(ctx context.Context, dst Destination, store LocalBlobs, prefix string, digests []string, opts UploadOptions) (EnvironmentTransfer, error) {
	var out EnvironmentTransfer
	for _, d := range digests {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		key, err := EnvironmentBlobKey(prefix, d)
		if err != nil {
			return out, err
		}
		path, err := store.BlobPath(d)
		if err != nil {
			return out, err
		}
		st, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			out.Missing++
			continue
		}
		if err != nil {
			return out, err
		}
		sum := strings.TrimPrefix(d, "sha256:")
		if obj, err := dst.Head(ctx, key); err == nil && obj.Size == st.Size() && strings.EqualFold(obj.SHA256, sum) {
			out.Present++
			continue
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			return out, err
		}
		o := opts
		o.SHA256 = sum
		if _, err := Upload(ctx, dst, path, key, o); err != nil {
			return out, err
		}
		out.Transferred++
		out.Bytes += st.Size()
	}
	return out, nil
}

// DownloadEnvironmentBlobs fetches every digest the local store lacks. The
// object's stored checksum must BE the digest (not merely match its own
// content), so a blob under the wrong name is refused. The caller records
// refs for the bundle that needed them (backup.AddEnvironmentRefs).
func DownloadEnvironmentBlobs(ctx context.Context, dst Destination, store LocalBlobs, prefix string, digests []string, opts DownloadOptions) (EnvironmentTransfer, error) {
	var out EnvironmentTransfer
	for _, d := range digests {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		key, err := EnvironmentBlobKey(prefix, d)
		if err != nil {
			return out, err
		}
		path, err := store.BlobPath(d)
		if err != nil {
			return out, err
		}
		if _, err := os.Stat(path); err == nil {
			out.Present++
			continue
		}
		obj, err := dst.Head(ctx, key)
		if errors.Is(err, ErrNotFound) {
			out.Missing++
			continue
		}
		if err != nil {
			return out, err
		}
		if !strings.EqualFold(obj.SHA256, strings.TrimPrefix(d, "sha256:")) {
			return out, fmt.Errorf("%w: %s: stored sha256 %s is not the digest it is named by", ErrVerifyFailed, key, obj.SHA256)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return out, err
		}
		got, err := Download(ctx, dst, key, path, opts)
		if err != nil {
			return out, err
		}
		out.Transferred++
		out.Bytes += got.Size
	}
	return out, nil
}

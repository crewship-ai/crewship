//go:build linux

package quota

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// offline verifies the exact backing image and every visible mount namespace.
// Merely stopping an application is insufficient: Docker bind aliases must go.
func (b *Backend) offline(ctx context.Context, d Descriptor) error {
	refs, err := b.references(d.Key)
	if err != nil {
		return err
	}
	if len(refs) != 0 {
		return ErrDenied
	}
	dev, err := mountedDevice(d.Mount)
	if err != nil {
		return err
	}
	if dev == "" {
		return ErrDenied
	}
	if err = b.verify(d); err != nil {
		return err
	}
	if err = b.checkAttachedElsewhere(dev, d.Mount); err != nil {
		return err
	}
	if err = b.command(ctx, "/usr/bin/umount", d.Mount); err != nil {
		return err
	}
	// Shared/slave canonical mounts may disappear through unmount propagation;
	// a private same-path alias must be absent before a single byte is copied.
	if err = b.checkAttachedElsewhere(dev, ""); err != nil {
		return errors.Join(err, b.attachDescriptor(context.WithoutCancel(ctx), d))
	}
	return b.command(ctx, "/usr/sbin/losetup", "-d", dev)
}

type snapshotReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r snapshotReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (b *Backend) Export(ctx context.Context, k Key, size int64, dst io.Writer) (result error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lock == nil || Validate(k, size) != nil || dst == nil {
		return ErrDenied
	}
	d, err := b.read(k)
	if err != nil {
		return err
	}
	if d.Bytes != size {
		return ErrDenied
	}
	if err = b.offline(ctx, d); err != nil {
		return err
	}
	// Restore the helper mount even after a disconnect. Runtime remains fenced by
	// the caller until capture succeeds; no application alias is resurrected here.
	defer func() { result = errors.Join(result, b.attachDescriptor(context.WithoutCancel(ctx), d)) }()
	_, image, _ := b.paths(k)
	if err = b.command(ctx, "/usr/sbin/e2fsck", "-f", "-n", image); err != nil {
		return err
	}
	f, err := b.safeFile(image)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.CopyN(dst, snapshotReader{ctx, f}, size)
	return err
}

// Import publishes metadata only after a complete preallocated image passes
// offline fsck. An interrupted or invalid import cannot be mounted by Recover.
func (b *Backend) Import(ctx context.Context, k Key, size int64, src io.Reader) (Descriptor, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lock == nil || Validate(k, size) != nil || src == nil {
		return Descriptor{}, ErrDenied
	}
	meta, image, mount := b.paths(k)
	if _, err := os.Lstat(meta); !os.IsNotExist(err) {
		return Descriptor{}, ErrDenied
	}
	if _, err := os.Lstat(image); !os.IsNotExist(err) {
		return Descriptor{}, ErrDenied
	}
	reservation, err := hostReservationLock(b.reservePath, b.owner)
	if err != nil {
		return Descriptor{}, err
	}
	defer reservation.Close()
	used, err := b.reservedBytes()
	if err != nil {
		return Descriptor{}, err
	}
	var space unix.Statfs_t
	if err = unix.Statfs(b.root, &space); err != nil {
		return Descriptor{}, err
	}
	if used > b.capacity-size || int64(space.Bavail)*int64(space.Bsize) < size+b.headroom {
		return Descriptor{}, ErrDenied
	}
	f, err := os.OpenFile(image, os.O_CREATE|os.O_EXCL|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return Descriptor{}, err
	}
	published := false
	defer func() {
		f.Close()
		if !published {
			_ = os.Remove(image)
		}
	}()
	if err = unix.Fallocate(int(f.Fd()), 0, 0, size); err != nil {
		return Descriptor{}, err
	}
	if _, err = io.CopyN(f, snapshotReader{ctx, src}, size); err != nil {
		return Descriptor{}, err
	}
	if err = f.Sync(); err != nil {
		return Descriptor{}, err
	}
	if err = b.command(ctx, "/usr/sbin/e2fsck", "-f", "-n", image); err != nil {
		return Descriptor{}, ErrDenied
	}
	d := Descriptor{ID: k.id(), Key: k, Bytes: size, Mount: mount}
	// Check the filesystem size before publishing; mount uses the same restricted
	// ext4 options as ordinary Ensure. Failure leaves no recoverable metadata.
	if err = b.attachDescriptor(context.WithoutCancel(ctx), d); err != nil {
		// A structurally invalid filesystem can mount but fail quota verification.
		// Remove only the loop attached to this exact private unpublished image.
		dev, inspectErr := mountedDevice(mount)
		if inspectErr != nil {
			published = true
			return Descriptor{}, errors.Join(err, inspectErr)
		}
		if dev != "" {
			backing, readErr := os.ReadFile(filepath.Join("/sys/block", filepath.Base(dev), "loop/backing_file"))
			if readErr != nil || strings.TrimSpace(string(backing)) != image {
				published = true
				return Descriptor{}, ErrDenied
			}
			if detachErr := b.checkAttachedElsewhere(dev, mount); detachErr != nil {
				published = true
				return Descriptor{}, detachErr
			}
			if unmountErr := b.command(ctx, "/usr/bin/umount", mount); unmountErr != nil {
				published = true
				return Descriptor{}, unmountErr
			}
			if aliasErr := b.checkAttachedElsewhere(dev, ""); aliasErr != nil {
				published = true
				return Descriptor{}, aliasErr
			}
			if detachErr := b.command(ctx, "/usr/sbin/losetup", "-d", dev); detachErr != nil {
				published = true
				return Descriptor{}, detachErr
			}
		}
		return Descriptor{}, err
	}
	raw, _ := json.Marshal(d)
	if err = atomicFile(meta, raw); err != nil {
		if cleanupErr := b.offline(ctx, d); cleanupErr != nil {
			// Never unlink an image still referenced by a live mount. Keep its
			// allocation charged even though no metadata can recover it.
			published = true
			return Descriptor{}, errors.Join(err, cleanupErr)
		}
		return Descriptor{}, err
	}
	published = true
	return d, nil
}

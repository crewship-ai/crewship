//go:build linux

package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Backend must run in a root helper in the host mount namespace. Root is trusted
// installation configuration, never a request argument. Aggregate capacity and
// free-space headroom bound host allocation even with many service generations.
type Backend struct {
	root               string
	capacity, headroom int64
	mu                 sync.Mutex
	lock               *os.File

	// owner is the UID every catalog file and directory must belong to.
	// NewBackend pins it to root; unit tests use their own UID.
	owner uint32
	// reservePath is the cross-helper host allocation lock.
	reservePath string
	// run executes a fixed system tool with the restricted environment.
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// attach mounts and verifies a descriptor; nil means the loop mount.
	attach func(ctx context.Context, d Descriptor) error
	// logf reports recovery decisions; nil discards them.
	logf func(format string, args ...any)
}

const defaultReservePath = "/run/crewship-quota-host-reserve.lock"

func NewBackend(root string, capacity, headroom int64) (*Backend, error) {
	if os.Geteuid() != 0 || !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.ContainsAny(root, " \t\n") || capacity < MinBytes || capacity > 1<<40 || headroom < 0 || headroom > 1<<40 {
		return nil, ErrDenied
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err = unix.Lstat(root, &stat); err != nil {
		return nil, err
	}
	if err = trustedParent(root); err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || stat.Uid != 0 || info.Mode().Perm() != 0700 {
		return nil, ErrDenied
	}
	for _, name := range []string{"images", "mounts"} {
		if err = os.Mkdir(filepath.Join(root, name), 0700); err != nil && !os.IsExist(err) {
			return nil, err
		}
		var child unix.Stat_t
		if err = unix.Lstat(filepath.Join(root, name), &child); err != nil || child.Uid != 0 || child.Mode&unix.S_IFMT != unix.S_IFDIR || child.Mode&0777 != 0700 {
			return nil, ErrDenied
		}
	}
	f, err := os.OpenFile(filepath.Join(root, "catalog.lock"), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, ErrUnavailable
	}
	return &Backend{root: root, capacity: capacity, headroom: headroom, lock: f, reservePath: defaultReservePath, run: runTool}, nil
}

// SetLogger routes recovery decisions (quarantine, cleanup) to the helper log.
func (b *Backend) SetLogger(logf func(format string, args ...any)) { b.logf = logf }

func (b *Backend) tool(ctx context.Context, name string, args ...string) ([]byte, error) {
	if b.run == nil {
		return runTool(ctx, name, args...)
	}
	return b.run(ctx, name, args...)
}

func (b *Backend) attachDescriptor(ctx context.Context, d Descriptor) error {
	if b.attach != nil {
		return b.attach(ctx, d)
	}
	return b.mount(ctx, d)
}
func (b *Backend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lock == nil {
		return nil
	}
	err := b.lock.Close()
	b.lock = nil
	return err
}

// absent reports that no image or mount directory is left for k.
func (b *Backend) absent(k Key) bool {
	_, image, mount := b.paths(k)
	_, ierr := os.Lstat(image)
	_, merr := os.Lstat(mount)
	return os.IsNotExist(ierr) && os.IsNotExist(merr)
}
func (b *Backend) paths(k Key) (string, string, string) {
	id := k.id()
	return filepath.Join(b.root, "images", id+".json"), filepath.Join(b.root, "images", id+".ext4"), filepath.Join(b.root, "mounts", id)
}
func (b *Backend) safeFile(path string) (*os.File, error) {
	return safeFileOwned(path, b.owner)
}
func safeFileOwned(path string, owner uint32) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	i, err := f.Stat()
	var stat unix.Stat_t
	if unix.Fstat(int(f.Fd()), &stat) != nil || stat.Uid != owner || err != nil || !i.Mode().IsRegular() || i.Mode().Perm() != 0600 {
		f.Close()
		return nil, ErrDenied
	}
	return f, nil
}
func (b *Backend) read(k Key) (Descriptor, error) {
	meta, _, mount := b.paths(k)
	f, err := b.safeFile(meta)
	if err != nil {
		return Descriptor{}, err
	}
	defer f.Close()
	var d Descriptor
	if err = json.NewDecoder(f).Decode(&d); err != nil || d.ID != k.id() || d.Key != k || d.Mount != mount || !validBytes(d.Bytes) {
		return Descriptor{}, ErrDenied
	}
	return d, nil
}

// runTool runs one fixed system tool with a minimal environment and a
// two-minute cap (the caller's ctx may cut it shorter). Stdout is returned;
// on failure the combined output is folded into the error.
func runTool(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("quota operation failed: %w (%s)", err, strings.TrimSpace(stdout.String()+stderr.String()))
	}
	return []byte(stdout.String()), nil
}
func (b *Backend) command(ctx context.Context, name string, args ...string) error {
	_, err := b.tool(ctx, name, args...)
	return err
}
func (b *Backend) Ensure(ctx context.Context, k Key, size int64, owner Owner) (Descriptor, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lock == nil || !k.valid() || !validBytes(size) {
		return Descriptor{}, ErrDenied
	}
	d, err := b.read(k)
	if err == nil {
		if d.Bytes != size {
			return Descriptor{}, ErrDenied
		}
		return d, b.attachDescriptor(ctx, d)
	}
	if !os.IsNotExist(err) {
		return Descriptor{}, err
	}
	// No metadata: whatever an earlier process left for this key was never
	// handed out. Clean an interrupted allocation, set aside anything else.
	if err = b.adoptUnpublished(k); err != nil {
		return Descriptor{}, err
	}
	// Separate per-database helpers share one host allocation lock. Physical
	// preallocation survives a crash; the next process observes reduced free space.
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
	free := int64(space.Bavail) * int64(space.Bsize)
	if used > b.capacity-size || free < size+b.headroom {
		return Descriptor{}, ErrDenied
	}
	meta, image, mount := b.paths(k)
	// The allocating record exists before the image does and is removed
	// only after the metadata is durable, so a crash at any point leaves
	// proof that the image was never handed out (Recover/Ensure/Remove
	// then delete it instead of wedging the key).
	record := b.allocatingPath(k)
	pending, _ := json.Marshal(Descriptor{ID: k.id(), Key: k, Bytes: size})
	if err = atomicFile(record, pending); err != nil {
		return Descriptor{}, err
	}
	f, err := os.OpenFile(image, os.O_CREATE|os.O_EXCL|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		_ = os.Remove(record)
		return Descriptor{}, err
	}
	allocated := false
	defer func() {
		f.Close()
		if !allocated {
			os.Remove(image)
			os.Remove(record)
		}
	}()
	if err = unix.Fallocate(int(f.Fd()), 0, 0, size); err != nil {
		return Descriptor{}, fmt.Errorf("preallocate quota image: %w", err)
	}
	if err = f.Sync(); err != nil {
		return Descriptor{}, err
	}
	if err = b.command(ctx, "/usr/sbin/mkfs.ext4", mkfsArgs(image, owner)...); err != nil {
		return Descriptor{}, err
	}
	if err = f.Sync(); err != nil {
		return Descriptor{}, err
	}
	if err = os.Mkdir(mount, 0700); err != nil && !os.IsExist(err) {
		return Descriptor{}, err
	}
	d = Descriptor{ID: k.id(), Key: k, Bytes: size, Mount: mount}
	raw, _ := json.Marshal(d)
	if err = atomicFile(meta, raw); err != nil {
		return Descriptor{}, err
	}
	allocated = true
	if err = os.Remove(record); err != nil && !os.IsNotExist(err) {
		return Descriptor{}, err
	}
	if err = b.attachDescriptor(ctx, d); err != nil {
		return Descriptor{}, err
	}
	return d, b.prepareRoot(d)
}

// mkfsArgs formats a fully allocated image. root_owner hands the volume
// root to the service image's numeric user, so a non-root image can write
// to it without an entrypoint chown.
func mkfsArgs(image string, owner Owner) []string {
	extended := "nodiscard,lazy_itable_init=0,lazy_journal_init=0"
	if owner.Set {
		extended += fmt.Sprintf(",root_owner=%d:%d", owner.UID, owner.GID)
	}
	return []string{"-q", "-F", "-m", "0", "-E", extended, image}
}

// prepareRoot runs once on a freshly formatted, mounted volume: it removes
// the empty lost+found mkfs creates, because data-directory initialisers
// such as postgres initdb refuse a non-empty directory. Existing
// generations are never touched (their lost+found may hold fsck output).
func (b *Backend) prepareRoot(d Descriptor) error {
	if err := os.Remove(filepath.Join(d.Mount, "lost+found")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("prepare quota volume root: %w", err)
	}
	return nil
}

func (b *Backend) allocatingPath(k Key) string {
	return filepath.Join(b.root, "images", k.id()+".allocating")
}

// reservedBytes is the aggregate preallocation: live images plus images set
// aside in quarantine, which still occupy disk until an administrator
// deletes them.
func (b *Backend) reservedBytes() (int64, error) {
	var used int64
	for _, dir := range []string{"images", "quarantine"} {
		entries, err := os.ReadDir(filepath.Join(b.root, dir))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return 0, err
		}
		for _, e := range entries {
			if !strings.Contains(e.Name(), ".ext4") {
				continue
			}
			i, err := e.Info()
			if err != nil || !i.Mode().IsRegular() {
				return 0, ErrDenied
			}
			used += i.Size()
		}
	}
	return used, nil
}

// adoptUnpublished resolves leftovers for a key that has no metadata. An
// allocating record proves an interrupted allocation: its image is deleted.
// An image without one was not produced by this catalog and is quarantined.
func (b *Backend) adoptUnpublished(k Key) error {
	_, image, mount := b.paths(k)
	record := b.allocatingPath(k)
	_, recErr := os.Lstat(record)
	_, imgErr := os.Lstat(image)
	switch {
	case recErr == nil:
		if err := os.Remove(image); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Remove(record); err != nil && !os.IsNotExist(err) {
			return err
		}
		b.log("quota catalog: removed interrupted allocation %s", k.id())
	case imgErr == nil:
		if err := b.quarantine(k.id() + ".ext4"); err != nil {
			return err
		}
		b.log("quota catalog: quarantined image %s without metadata", k.id())
	}
	// An empty mount directory from the interrupted attempt is harmless and
	// reused; a mounted one would have metadata.
	if dev, err := mountedDevice(mount); err == nil && dev == "" {
		_ = os.Remove(mount)
	}
	return nil
}

// quarantine moves one catalog file aside. Nothing is deleted: the
// administrator inspects quarantine/ and removes what is truly garbage.
func (b *Backend) quarantine(name string) error {
	dir := filepath.Join(b.root, "quarantine")
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	src := filepath.Join(b.root, "images", name)
	if _, err := os.Lstat(src); os.IsNotExist(err) {
		return nil
	}
	return os.Rename(src, filepath.Join(dir, fmt.Sprintf("%s.%d", name, time.Now().UnixNano())))
}

func (b *Backend) log(format string, args ...any) {
	if b.logf != nil {
		b.logf(format, args...)
	}
}

func atomicFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".catalog-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func mountedDevice(path string) (string, error) {
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		parts := strings.Split(line, " - ")
		if len(parts) != 2 {
			continue
		}
		left, right := strings.Fields(parts[0]), strings.Fields(parts[1])
		if len(left) > 5 && left[4] == path {
			if !strings.Contains(","+left[5]+",", ",nosuid,") || !strings.Contains(","+left[5]+",", ",nodev,") || !strings.Contains(","+left[5]+",", ",noexec,") || len(right) < 2 || right[0] != "ext4" || !strings.HasPrefix(right[1], "/dev/loop") {
				return "", ErrDenied
			}
			return right[1], nil
		}
	}
	return "", nil
}
func (b *Backend) mount(ctx context.Context, d Descriptor) error {
	dev, err := mountedDevice(d.Mount)
	if err != nil {
		return err
	}
	if dev != "" {
		return b.verify(d)
	}
	_, image, _ := b.paths(d.Key)
	if err = os.Mkdir(d.Mount, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	var stat unix.Stat_t
	if err = unix.Lstat(d.Mount, &stat); err != nil || stat.Uid != b.owner || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0777 != 0700 {
		return ErrDenied
	}
	f, err := b.safeFile(image)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	f.Close()
	if err != nil || info.Size() != d.Bytes {
		return ErrDenied
	}
	out, err := b.tool(ctx, "/usr/sbin/losetup", "--find", "--show", "--nooverlap", image)
	if err != nil {
		return fmt.Errorf("loopback unavailable: %w", err)
	}
	dev = strings.TrimSpace(string(out))
	if !strings.HasPrefix(dev, "/dev/loop") {
		return ErrDenied
	}
	if err = b.command(ctx, "/usr/bin/mount", "-t", "ext4", "-o", "nosuid,nodev,noexec", dev, d.Mount); err != nil {
		_ = b.command(ctx, "/usr/sbin/losetup", "-d", dev)
		return err
	}
	return b.verify(d)
}
func (b *Backend) verify(d Descriptor) error {
	_, image, mount := b.paths(d.Key)
	if d.Mount != mount {
		return ErrDenied
	}
	dev, err := mountedDevice(mount)
	if err != nil || dev == "" {
		return ErrDenied
	}
	backing, err := os.ReadFile(filepath.Join("/sys/block", filepath.Base(dev), "loop/backing_file"))
	if err != nil || strings.TrimSpace(string(backing)) != image {
		return ErrDenied
	}
	f, err := b.safeFile(image)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	f.Close()
	if err != nil || info.Size() != d.Bytes {
		return ErrDenied
	}
	var allocated unix.Stat_t
	if unix.Stat(image, &allocated) != nil || allocated.Blocks*512 < d.Bytes {
		return ErrDenied
	}
	var fs unix.Statfs_t
	if err = unix.Statfs(mount, &fs); err != nil {
		return err
	}
	if fs.Type != unix.EXT4_SUPER_MAGIC || int64(fs.Blocks)*int64(fs.Bsize) > d.Bytes {
		return ErrDenied
	}
	return nil
}
func (b *Backend) Verify(_ context.Context, k Key, size int64) (Descriptor, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lock == nil || !k.valid() || !validBytes(size) {
		return Descriptor{}, ErrDenied
	}
	d, err := b.read(k)
	if err != nil || d.Bytes != size {
		return Descriptor{}, ErrDenied
	}
	return d, b.verify(d)
}
func (b *Backend) Remove(ctx context.Context, k Key) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lock == nil || !k.valid() {
		return ErrDenied
	}
	d, err := b.read(k)
	if os.IsNotExist(err) {
		if err = b.adoptUnpublished(k); err != nil {
			return err
		}
		if b.absent(k) {
			return nil
		}
		return ErrDenied
	}
	if err != nil {
		return err
	}
	refs, err := b.references(k)
	if err != nil {
		return err
	}
	if len(refs) > 0 {
		return ErrDenied
	}
	dev, err := mountedDevice(d.Mount)
	if err != nil {
		return err
	}
	if dev != "" {
		if err = b.verify(d); err != nil {
			return err
		}
		if err = detachedFromOtherMounts(dev, d.Mount); err != nil {
			return err
		}
		if err = b.command(ctx, "/usr/bin/umount", d.Mount); err != nil {
			return err
		}
		if err = b.command(ctx, "/usr/sbin/losetup", "-d", dev); err != nil {
			return err
		}
	}
	meta, image, mount := b.paths(k)
	if err = os.Remove(image); err != nil {
		return err
	}
	if err = os.Remove(meta); err != nil {
		return err
	}
	if err = os.Remove(filepath.Join(b.root, "images", k.id()+".references")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Remove(mount)
}
func (b *Backend) Recover(ctx context.Context) error {
	_, err := b.RecoverReport(ctx)
	return err
}

// RecoveryReport is what Recover did with each catalog entry (by id).
type RecoveryReport struct {
	Mounted      []string // valid entries attached and verified
	Quarantined  []string // malformed entries moved to quarantine/
	Cleaned      []string // interrupted allocations deleted
	Failed       []string // valid entries whose attach failed; left in place
	OverCapacity bool     // reserved bytes exceed the configured capacity
}

// Summary is a one-line status for the systemd STATUS= field and the log.
func (r RecoveryReport) Summary() string {
	s := fmt.Sprintf("recovered %d quota volume(s); %d quarantined, %d interrupted allocation(s) cleaned, %d failed to attach",
		len(r.Mounted), len(r.Quarantined), len(r.Cleaned), len(r.Failed))
	if r.OverCapacity {
		s += "; reserved images exceed the configured capacity"
	}
	return s
}

// RecoverReport remounts every valid catalog entry after a helper restart.
// One bad entry must not keep the helper (and the server that orders after
// it) down: malformed entries are moved to quarantine/ and reported,
// interrupted allocations are deleted, and an attach failure is reported
// without touching the entry. Only an unusable catalog is an error.
func (b *Backend) RecoverReport(ctx context.Context) (RecoveryReport, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var report RecoveryReport
	if b.lock == nil {
		return report, ErrDenied
	}
	dir := filepath.Join(b.root, "images")
	list := func(suffix string) ([]string, error) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		var ids []string
		for _, e := range entries {
			if id, ok := strings.CutSuffix(e.Name(), suffix); ok && !strings.HasPrefix(id, ".") {
				ids = append(ids, id)
			}
		}
		return ids, nil
	}
	set := func(r *[]string, id, format string, args ...any) {
		if !slices.Contains(*r, id) {
			*r = append(*r, id)
		}
		b.log(format, args...)
	}
	quarantine := func(id, reason string) error {
		for _, suffix := range []string{".json", ".ext4", ".references"} {
			if err := b.quarantine(id + suffix); err != nil {
				return err
			}
		}
		set(&report.Quarantined, id, "quota catalog: quarantined %s: %s", id, reason)
		return nil
	}

	// 1. Interrupted allocations: the record precedes the image and is
	// removed only after the metadata is durable.
	pending, err := list(".allocating")
	if err != nil {
		return report, err
	}
	for _, id := range pending {
		if _, err := os.Lstat(filepath.Join(dir, id+".json")); err == nil {
			_ = os.Remove(filepath.Join(dir, id+".allocating")) // finished; record outlived a crash
			continue
		}
		for _, suffix := range []string{".ext4", ".allocating"} {
			if err := os.Remove(filepath.Join(dir, id+suffix)); err != nil && !os.IsNotExist(err) {
				return report, err
			}
		}
		_ = os.Remove(filepath.Join(b.root, "mounts", id))
		set(&report.Cleaned, id, "quota catalog: removed interrupted allocation %s", id)
	}

	// 2. Published entries.
	published, err := list(".json")
	if err != nil {
		return report, err
	}
	for _, id := range published {
		d, reason := b.inspectEntry(id)
		if reason != "" {
			if err := quarantine(id, reason); err != nil {
				return report, err
			}
			continue
		}
		if err := b.attachDescriptor(ctx, d); err != nil {
			set(&report.Failed, id, "quota catalog: attach %s failed: %v", id, err)
			continue
		}
		report.Mounted = append(report.Mounted, id)
	}

	// 3. Images nobody published.
	images, err := list(".ext4")
	if err != nil {
		return report, err
	}
	for _, id := range images {
		if _, err := os.Lstat(filepath.Join(dir, id+".json")); os.IsNotExist(err) {
			if err := quarantine(id, "image without metadata"); err != nil {
				return report, err
			}
		}
	}

	used, err := b.reservedBytes()
	if err != nil {
		return report, err
	}
	if used > b.capacity {
		report.OverCapacity = true
		b.log("quota catalog: reserved %d bytes exceed capacity %d; new allocations are refused", used, b.capacity)
	}
	return report, nil
}

// inspectEntry validates one published entry and returns why it is
// unusable ("" when it is fine).
func (b *Backend) inspectEntry(id string) (Descriptor, string) {
	f, err := b.safeFile(filepath.Join(b.root, "images", id+".json"))
	if err != nil {
		return Descriptor{}, fmt.Sprintf("unreadable metadata: %v", err)
	}
	var d Descriptor
	err = json.NewDecoder(io.LimitReader(f, 8192)).Decode(&d)
	f.Close()
	if err != nil || !d.Key.valid() || d.ID != d.Key.id() || d.ID != id {
		return Descriptor{}, "malformed metadata"
	}
	known, err := b.read(d.Key)
	if err != nil {
		return Descriptor{}, fmt.Sprintf("inconsistent metadata: %v", err)
	}
	_, image, _ := b.paths(known.Key)
	img, err := b.safeFile(image)
	if err != nil {
		return Descriptor{}, fmt.Sprintf("missing or unsafe image: %v", err)
	}
	info, err := img.Stat()
	img.Close()
	if err != nil || info.Size() != known.Bytes {
		return Descriptor{}, "image size disagrees with metadata"
	}
	return known, ""
}

func trustedParent(path string) error {
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		var st unix.Stat_t
		if err := unix.Lstat(parent, &st); err != nil {
			return err
		}
		if st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&0022 != 0 && st.Mode&unix.S_ISVTX == 0 {
			return ErrDenied
		}
		if parent == "/" {
			break
		}
	}
	return nil
}
func detachedFromOtherMounts(dev, mount string) error {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == "" || e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "mountinfo"))
		if os.IsNotExist(err) || errors.Is(err, unix.EINVAL) && inactiveProcess(e.Name()) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect mount namespace %s: %w", e.Name(), err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			parts := strings.Split(line, " - ")
			if len(parts) != 2 {
				continue
			}
			left, right := strings.Fields(parts[0]), strings.Fields(parts[1])
			if len(left) > 4 && len(right) > 1 && right[1] == dev && left[4] != mount {
				return fmt.Errorf("quota still attached in process %s at %s: %w", e.Name(), left[4], ErrDenied)
			}
		}
	}
	return nil
}

func inactiveProcess(pid string) bool {
	raw, err := os.ReadFile(filepath.Join("/proc", pid, "stat"))
	if os.IsNotExist(err) {
		return true
	}
	if err != nil {
		return false
	}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return false
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 7 {
		return false
	}
	if fields[0] == "Z" || fields[0] == "X" {
		return true
	}
	flags, err := strconv.ParseUint(fields[6], 10, 64)
	return err == nil && flags&0x00200000 != 0 // PF_KTHREAD owns no userspace mounts
}

func (b *Backend) references(k Key) ([]string, error) {
	p := filepath.Join(b.root, "images", k.id()+".references")
	f, err := b.safeFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var refs []string
	if json.NewDecoder(io.LimitReader(f, 8192)).Decode(&refs) != nil || len(refs) > 16 {
		return nil, ErrDenied
	}
	return refs, nil
}

var referenceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,255}$`)

func (b *Backend) Protect(_ context.Context, k Key, reference string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lock == nil || !k.valid() || !referenceName.MatchString(reference) {
		return ErrDenied
	}
	d, err := b.read(k)
	if err != nil {
		return err
	}
	if err = b.verify(d); err != nil {
		return err
	}
	refs, err := b.references(k)
	if err != nil {
		return err
	}
	if slices.Contains(refs, reference) {
		return nil
	}
	if len(refs) >= 16 {
		return ErrDenied
	}
	refs = append(refs, reference)
	raw, _ := json.Marshal(refs)
	return atomicFile(filepath.Join(b.root, "images", k.id()+".references"), raw)
}
func (b *Backend) Release(_ context.Context, k Key, reference string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lock == nil || !k.valid() || !referenceName.MatchString(reference) {
		return ErrDenied
	}
	if _, err := b.read(k); err != nil {
		if os.IsNotExist(err) && b.absent(k) {
			return nil // already removed: a retried teardown must not wedge
		}
		return err
	}
	refs, err := b.references(k)
	if err != nil {
		return err
	}
	refs = slices.DeleteFunc(refs, func(r string) bool { return r == reference })
	raw, _ := json.Marshal(refs)
	return atomicFile(filepath.Join(b.root, "images", k.id()+".references"), raw)
}

// BindNamespace permanently binds a catalog to one operator-selected database identity.
func (b *Backend) BindNamespace(namespace string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lock == nil || !component.MatchString(namespace) {
		return ErrDenied
	}
	target := filepath.Join(b.root, "namespace")
	f, err := b.safeFile(target)
	if err == nil {
		raw, e := io.ReadAll(io.LimitReader(f, 129))
		f.Close()
		if e != nil || string(raw) != namespace {
			return ErrDenied
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(b.root, "images"))
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return ErrDenied
	}
	return atomicFile(target, []byte(namespace))
}

func hostReservationLock(path string, owner uint32) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err = unix.Fstat(int(f.Fd()), &st); err != nil || st.Uid != owner || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0777 != 0600 {
		f.Close()
		return nil, ErrDenied
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) || time.Now().After(deadline) {
			f.Close()
			return nil, ErrUnavailable
		}
		time.Sleep(25 * time.Millisecond)
	}
}

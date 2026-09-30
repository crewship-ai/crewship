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
}

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
	return &Backend{root: root, capacity: capacity, headroom: headroom, lock: f}, nil
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
func (b *Backend) paths(k Key) (string, string, string) {
	id := k.id()
	return filepath.Join(b.root, "images", id+".json"), filepath.Join(b.root, "images", id+".ext4"), filepath.Join(b.root, "mounts", id)
}
func safeFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	i, err := f.Stat()
	var stat unix.Stat_t
	if unix.Fstat(int(f.Fd()), &stat) != nil || stat.Uid != 0 || err != nil || !i.Mode().IsRegular() || i.Mode().Perm() != 0600 {
		f.Close()
		return nil, ErrDenied
	}
	return f, nil
}
func (b *Backend) read(k Key) (Descriptor, error) {
	meta, _, mount := b.paths(k)
	f, err := safeFile(meta)
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
func command(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("quota operation failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
func (b *Backend) Ensure(k Key, size int64) (Descriptor, error) {
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
		if err = b.mount(d); err != nil {
			return Descriptor{}, err
		}
		return d, b.verify(d)
	}
	if !os.IsNotExist(err) {
		return Descriptor{}, err
	}
	// Separate per-database helpers share one host allocation lock. Physical
	// preallocation survives a crash; the next process observes reduced free space.
	reservation, err := hostReservationLock()
	if err != nil {
		return Descriptor{}, err
	}
	defer reservation.Close()
	// A complete preallocation is the reservation. Count even orphan images left
	// by a crash, so cleanup cannot accidentally overcommit aggregate capacity.
	entries, err := os.ReadDir(filepath.Join(b.root, "images"))
	if err != nil {
		return Descriptor{}, err
	}
	used := int64(0)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".ext4") {
			i, err := e.Info()
			if err != nil || !i.Mode().IsRegular() {
				return Descriptor{}, ErrDenied
			}
			used += i.Size()
			if used > b.capacity {
				return Descriptor{}, ErrDenied
			}
		}
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
	f, err := os.OpenFile(image, os.O_CREATE|os.O_EXCL|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return Descriptor{}, err
	}
	allocated := false
	defer func() {
		f.Close()
		if !allocated {
			os.Remove(image)
		}
	}()
	if err = unix.Fallocate(int(f.Fd()), 0, 0, size); err != nil {
		return Descriptor{}, fmt.Errorf("preallocate quota image: %w", err)
	}
	if err = f.Sync(); err != nil {
		return Descriptor{}, err
	}
	if err = command("/usr/sbin/mkfs.ext4", "-q", "-F", "-m", "0", "-E", "nodiscard,lazy_itable_init=0,lazy_journal_init=0", image); err != nil {
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
	if err = b.mount(d); err != nil {
		return Descriptor{}, err
	}
	return d, b.verify(d)
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
func (b *Backend) mount(d Descriptor) error {
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
	if err = unix.Lstat(d.Mount, &stat); err != nil || stat.Uid != 0 || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0777 != 0700 {
		return ErrDenied
	}
	f, err := safeFile(image)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	f.Close()
	if err != nil || info.Size() != d.Bytes {
		return ErrDenied
	}
	out, err := exec.Command("/usr/sbin/losetup", "--find", "--show", "--nooverlap", image).Output()
	if err != nil {
		return fmt.Errorf("loopback unavailable: %w", err)
	}
	dev = strings.TrimSpace(string(out))
	if !strings.HasPrefix(dev, "/dev/loop") {
		return ErrDenied
	}
	if err = command("/usr/bin/mount", "-t", "ext4", "-o", "nosuid,nodev,noexec", dev, d.Mount); err != nil {
		_ = command("/usr/sbin/losetup", "-d", dev)
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
	f, err := safeFile(image)
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
func (b *Backend) Verify(k Key, size int64) (Descriptor, error) {
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
func (b *Backend) Remove(k Key) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lock == nil || !k.valid() {
		return ErrDenied
	}
	d, err := b.read(k)
	if os.IsNotExist(err) {
		_, image, mount := b.paths(k)
		if _, ierr := os.Lstat(image); os.IsNotExist(ierr) {
			if _, merr := os.Lstat(mount); os.IsNotExist(merr) {
				return nil
			}
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
		if err = command("/usr/bin/umount", d.Mount); err != nil {
			return err
		}
		// Unmount propagation must have removed canonical-path copies in all
		// other namespaces too. A private same-path alias remains a live writer.
		if err = detachedFromOtherMounts(dev, ""); err != nil {
			return err
		}
		if err = command("/usr/sbin/losetup", "-d", dev); err != nil {
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
func (b *Backend) Recover() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lock == nil {
		return ErrDenied
	}
	entries, err := os.ReadDir(filepath.Join(b.root, "images"))
	if err != nil {
		return err
	}
	var used int64
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".ext4") {
			continue
		}
		f, err := safeFile(filepath.Join(b.root, "images", e.Name()))
		if err != nil {
			return err
		}
		info, err := f.Stat()
		f.Close()
		if err != nil || info.Size() > b.capacity-used {
			return ErrDenied
		}
		used += info.Size()
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		f, err := safeFile(filepath.Join(b.root, "images", e.Name()))
		if err != nil {
			return err
		}
		var d Descriptor
		err = json.NewDecoder(f).Decode(&d)
		f.Close()
		if err != nil || !d.Key.valid() || d.ID != d.Key.id() {
			return ErrDenied
		}
		known, err := b.read(d.Key)
		if err != nil {
			return err
		}
		if err = b.mount(known); err != nil {
			return err
		}
	}
	return nil
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
	f, err := safeFile(p)
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

func (b *Backend) Protect(k Key, reference string) error {
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
func (b *Backend) Release(k Key, reference string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lock == nil || !k.valid() || !referenceName.MatchString(reference) {
		return ErrDenied
	}
	if _, err := b.read(k); err != nil {
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
	f, err := safeFile(target)
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

func hostReservationLock() (*os.File, error) {
	f, err := os.OpenFile("/run/crewship-quota-host-reserve.lock", os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err = unix.Fstat(int(f.Fd()), &st); err != nil || st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0777 != 0600 {
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

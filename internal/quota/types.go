// Package quota provides a fail-closed catalog of physically bounded durable
// filesystems. Calls are host-only; paths never come from service/task input.
package quota

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"
)

var ErrDenied = errors.New("quota catalog denied")
var ErrUnavailable = errors.New("quota backend unavailable")

const MinBytes = int64(32 << 20)
const MaxBytes = int64(64 << 30)

type Key struct {
	Crew, Service, Volume string
	Generation            int64
}
type Descriptor struct {
	ID    string
	Key   Key
	Bytes int64
	Mount string
}
type Catalog interface {
	Ensure(Key, int64) (Descriptor, error)
	Verify(Key, int64) (Descriptor, error)
	Remove(Key) error
	Recover() error
}

var component = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

func (k Key) valid() bool {
	return component.MatchString(k.Crew) && component.MatchString(k.Service) && component.MatchString(k.Volume) && k.Generation > 0
}
func (k Key) id() string {
	b, _ := json.Marshal(k)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func validBytes(n int64) bool { return n >= MinBytes && n <= MaxBytes && n%(1<<20) == 0 }

func Validate(k Key, n int64) error {
	if !k.valid() || !validBytes(n) {
		return ErrDenied
	}
	return nil
}

// ValidateVolume enforces the portable opt-in wire contract. Generation zero
// means the first immutable generation; trusted legacy configs carry no quota.
func ValidateVolume(enabled bool, service, volume, mount string, generation, n int64) error {
	if !enabled {
		if n != 0 || generation != 0 {
			return ErrDenied
		}
		return nil
	}
	if generation == 0 {
		generation = 1
	}
	if Validate(Key{"valid-crew", service, volume, generation}, n) != nil {
		return ErrDenied
	}
	if !path.IsAbs(mount) || path.Clean(mount) != mount || mount == "/" {
		return ErrDenied
	}
	for _, reserved := range []string{"/tmp", "/run", "/var/run", "/dev", "/proc", "/sys"} {
		if mount == reserved || strings.HasPrefix(mount, reserved+"/") {
			return ErrDenied
		}
	}
	return nil
}

// ReferenceCatalog protects filesystem identity while Docker volume metadata
// exists, including the interval before its first container mount.
type ReferenceCatalog interface {
	Catalog
	Protect(Key, string) error
	Release(Key, string) error
}

// SnapshotCatalog transfers offline fixed-size ext4 images. Import never replaces
// an existing generation. Callers must detach all runtime aliases before Export.
type SnapshotCatalog interface {
	ReferenceCatalog
	Export(context.Context, Key, int64, io.Writer) error
	Import(context.Context, Key, int64, io.Reader) (Descriptor, error)
}

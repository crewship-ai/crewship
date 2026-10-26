//go:build linux

package mooring

import (
	"math"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type bootOwnerClock struct{ bootID string }

// NewBootOwnerClock supplies the trusted Linux boot identity and a clock which
// advances while the system is suspended. It does not read document content.
func NewBootOwnerClock() (OwnerClock, error) {
	data, e := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if e != nil {
		return nil, e
	}
	boot := strings.TrimSpace(string(data))
	if !ownerIDValid(boot) {
		return nil, ErrInvalid
	}
	c := &bootOwnerClock{bootID: boot}
	if _, _, e = c.Now(); e != nil {
		return nil, e
	}
	return c, nil
}
func (c *bootOwnerClock) Now() (string, time.Duration, error) {
	var ts unix.Timespec
	if e := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); e != nil {
		return "", 0, e
	}
	sec, nsec := int64(ts.Sec), int64(ts.Nsec)
	if sec < 0 || sec > math.MaxInt64/int64(time.Second) || nsec < 0 || nsec >= int64(time.Second) {
		return "", 0, ErrInvalid
	}
	seconds := sec * int64(time.Second)
	if nsec > math.MaxInt64-seconds {
		return "", 0, ErrInvalid
	}
	return c.bootID, time.Duration(seconds + nsec), nil
}

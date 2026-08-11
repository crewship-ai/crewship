package server

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"syscall"
	"testing"
)

// TestIsConnRefused_MatchesBothSpellings covers the classification helper that
// decides "stale corpse" vs "cannot tell".
//
// It has to exist as a unit test because the bug it guards is invisible on
// this host: syscall.ECONNREFUSED is a real errno on unix, but on windows Go
// defines it as an *invented* APPLICATION_ERROR+n value, while a refused
// AF_UNIX connect() there comes back as Winsock's WSAECONNREFUSED (10061).
// syscall.Errno.Is only bridges the four oserror sentinels (ErrPermission,
// ErrExist, ErrNotExist, ErrUnsupported), never one Errno to another, so
// errors.Is(err, syscall.ECONNREFUSED) was dead code on windows. The
// consequence was the inverse of the guard's purpose: a crewshipd killed by
// power loss left a socket file the next start could not classify as stale, so
// the daemon could never start again without a manual `rm` — a permanent wedge
// where the old unconditional unlink had a transient one.
//
// Both errno values are checked on every GOOS, which is the point: whichever
// platform runs the test, the branch for the *other* platform's spelling is
// still exercised.
func TestIsConnRefused_MatchesBothSpellings(t *testing.T) {
	// wrap reproduces the shape net.DialTimeout actually returns:
	// *net.OpError -> *os.SyscallError -> syscall.Errno.
	wrap := func(errno syscall.Errno) error {
		return &net.OpError{
			Op:   "dial",
			Net:  "unix",
			Addr: &net.UnixAddr{Name: "/tmp/i.sock", Net: "unix"},
			Err:  os.NewSyscallError("connect", errno),
		}
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"bare posix ECONNREFUSED", syscall.ECONNREFUSED, true},
		{"wrapped posix ECONNREFUSED", wrap(syscall.ECONNREFUSED), true},
		{"bare winsock WSAECONNREFUSED", wsaeconnrefused, true},
		{"wrapped winsock WSAECONNREFUSED", wrap(wsaeconnrefused), true},
		// Everything below is a "cannot tell" and must stay a refusal: a
		// permission-denied probe of another user's live socket is exactly the
		// case that must not be downgraded to "stale".
		{"EACCES", wrap(syscall.EACCES), false},
		{"ETIMEDOUT", wrap(syscall.ETIMEDOUT), false},
		{"ENOENT", wrap(syscall.ENOENT), false},
		{"not-exist sentinel", fs.ErrNotExist, false},
		{"plain error", errors.New("connection refused"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isConnRefused(tt.err); got != tt.want {
				t.Errorf("isConnRefused(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

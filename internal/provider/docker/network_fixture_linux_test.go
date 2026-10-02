//go:build linux

package docker

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// A test installation must not adopt or tear down another process's crews.
func networkFixtureIdentity(t *testing.T) string {
	t.Helper()
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("crewship-nettest-%x", token)
}

// Lease a private /24 across test processes and avoid existing host routes.
// Keep the lock inode after closing: unlinking it could let a third process
// acquire a new inode while a second process still owns the original lock.
func networkFixturePool(t *testing.T) string {
	t.Helper()
	routes, err := hostIPv4Routes()
	if err != nil {
		t.Fatal(err)
	}
	var seed [2]byte
	if _, err := rand.Read(seed[:]); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(os.TempDir(), "crewship-network-test-pools")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for offset := 0; offset < 32768; offset++ {
		slot := (int(binary.BigEndian.Uint16(seed[:])) + offset) % 32768
		pool := netip.PrefixFrom(netip.AddrFrom4([4]byte{10, byte(128 + slot/256), byte(slot % 256), 0}), 24)
		if _, conflict := foreignRouteInPool(pool, routes, nil); conflict {
			continue
		}
		lock, err := os.OpenFile(filepath.Join(root, fmt.Sprintf("%d.lock", slot)), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			lock.Close()
			continue
		}
		t.Cleanup(func() { _ = lock.Close() })
		return pool.String()
	}
	t.Fatal("no isolated private network pool available")
	return ""
}

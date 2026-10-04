//go:build linux

package docker

import (
	"os"
	"syscall"
	"testing"
)

type stagedOwnerFixture struct {
	os.FileInfo
	uid uint32
}

func (f stagedOwnerFixture) Sys() any { return &syscall.Stat_t{Uid: f.uid} }
func TestStagedOwnerRejectsRuntimePrincipals(t *testing.T) {
	info, e := os.Stat(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	for _, uid := range []uint32{0, 1001, 1002, uint32(os.Geteuid()), 9999} {
		expected := (uid == 0 || uid == uint32(os.Geteuid())) && uid != 1001 && uid != 1002
		if stagedOwned(stagedOwnerFixture{FileInfo: info, uid: uid}) != expected {
			t.Fatalf("owner uid=%d principal boundary changed", uid)
		}
	}
}

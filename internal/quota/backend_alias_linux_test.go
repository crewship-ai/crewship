//go:build linux

package quota

import (
	"errors"
	"os"
	"slices"
	"testing"
)

const aliasMount = "/var/lib/crewship-quota/ns/mounts/v1"

// mountinfo lines as /proc/<pid>/mountinfo prints them.
const (
	ownSharedLine  = "500 29 7:9 / " + aliasMount + " rw,nosuid,nodev,noexec,relatime shared:812 - ext4 /dev/loop9 rw\n"
	ownPrivateLine = "500 29 7:9 / " + aliasMount + " rw,nosuid,nodev,noexec,relatime - ext4 /dev/loop9 rw\n"
)

// Remove unmounts one path in its own namespace. Any other attachment of
// the same loop device survives that umount and keeps the filesystem alive
// after the image is deleted, so it must block removal. Only copies that
// the umount itself takes down (propagation peers and slaves) are safe.
func TestMountAlias(t *testing.T) {
	tests := []struct {
		name    string
		own     string
		other   string
		sameNS  bool
		mount   string
		wantErr bool
	}{
		{name: "own mount only", own: ownSharedLine, other: ownSharedLine, sameNS: true, mount: aliasMount},
		{name: "second path in own namespace", own: ownSharedLine, other: ownSharedLine + "501 29 7:9 / /elsewhere rw - ext4 /dev/loop9 rw\n", sameNS: true, mount: aliasMount, wantErr: true},
		{name: "container bind at another path", own: ownSharedLine, other: "900 800 7:9 / /data rw - ext4 /dev/loop9 rw\n", mount: aliasMount, wantErr: true},
		{name: "same path, private copy in another namespace", own: ownSharedLine, other: "700 600 7:9 / " + aliasMount + " rw,nosuid,nodev,noexec - ext4 /dev/loop9 rw\n", mount: aliasMount, wantErr: true},
		{name: "same path, private own mount, any other namespace", own: ownPrivateLine, other: ownPrivateLine, mount: aliasMount, wantErr: true},
		{name: "same path, slave of our peer group", own: ownSharedLine, other: "700 600 7:9 / " + aliasMount + " rw,nosuid,nodev,noexec master:812 - ext4 /dev/loop9 rw\n", mount: aliasMount},
		{name: "same path, shared peer", own: ownSharedLine, other: "700 600 7:9 / " + aliasMount + " rw,nosuid,nodev,noexec shared:812 - ext4 /dev/loop9 rw\n", mount: aliasMount},
		{name: "same path, unrelated peer group", own: ownSharedLine, other: "700 600 7:9 / " + aliasMount + " rw,nosuid,nodev,noexec master:99 - ext4 /dev/loop9 rw\n", mount: aliasMount, wantErr: true},
		{name: "other device ignored", own: ownSharedLine, other: "700 600 7:8 / /data rw - ext4 /dev/loop8 rw\n", mount: aliasMount},
		{name: "unmounted image: any attachment blocks", own: "", other: "700 600 7:9 / " + aliasMount + " rw master:812 - ext4 /dev/loop9 rw\n", mount: "", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, alias := mountAlias("/dev/loop9", tc.mount, parseMountinfo(tc.own), parseMountinfo(tc.other), tc.sameNS)
			if alias != tc.wantErr {
				t.Fatalf("alias = %v, want %v", alias, tc.wantErr)
			}
		})
	}
}

// After the helper's umount, or when the helper never mounted the volume,
// a loop device backing the image can still be mounted in another
// namespace: a copy the umount did not propagate to. Remove must find the
// device from the image, refuse while anything mounts it, and detach it
// before deleting the image otherwise. Both Remove branches end here.
func TestRemoveChecksLoopOfUnmountedImage(t *testing.T) {
	for _, tc := range []struct {
		name       string
		elsewhere  error
		wantErr    bool
		wantDetach bool
	}{
		{name: "detached loop is released before the image is deleted", wantDetach: true},
		{name: "loop mounted in another namespace blocks removal", elsewhere: ErrDenied, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newUnitBackend(t)
			k := Key{"crew", "database", "data", 1}
			u.writeEntry(t, k, unitSize, true, true)
			_, image, mount := u.paths(k)
			if err := os.MkdirAll(mount, 0700); err != nil {
				t.Fatal(err)
			}
			u.backingLoops = func(path string) ([]string, error) {
				if path != image {
					t.Errorf("loop lookup for %s, want %s", path, image)
				}
				return []string{"/dev/loop9"}, nil
			}
			var scanned []string
			u.attachedElsewhere = func(dev, mount string) error {
				scanned = append(scanned, dev+"|"+mount)
				return tc.elsewhere
			}
			err := u.Remove(t.Context(), k)
			if (err != nil) != tc.wantErr || tc.wantErr && !errors.Is(err, ErrDenied) {
				t.Fatalf("Remove error = %v, wantErr %v", err, tc.wantErr)
			}
			if !slices.Equal(scanned, []string{"/dev/loop9|"}) {
				t.Fatalf("alias scans %v, want one unconditional scan of /dev/loop9", scanned)
			}
			detached := slices.ContainsFunc(u.commands, func(c []string) bool { return slices.Equal(c, []string{"/usr/sbin/losetup", "-d", "/dev/loop9"}) })
			if detached != tc.wantDetach {
				t.Fatalf("losetup -d ran=%v, want %v (commands %v)", detached, tc.wantDetach, u.commands)
			}
			_, statErr := os.Lstat(image)
			if tc.wantErr == os.IsNotExist(statErr) {
				t.Fatalf("image present=%v after Remove err=%v", statErr == nil, err)
			}
		})
	}
}

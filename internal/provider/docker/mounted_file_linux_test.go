package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPodmanMountedExecutableIdentity(t *testing.T) {
	base := t.TempDir()
	bin := filepath.Join(base, "usr", "local", "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(base, "staged")
	if err := os.WriteFile(staged, []byte("sdk"), 0555); err != nil {
		t.Fatal(err)
	}
	host, err := os.Stat(staged)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	target := filepath.Join(bin, "crewship")
	if err := os.Link(staged, target); err != nil {
		t.Fatal(err)
	}
	if err := verifyMountedFile(root, nil, agentCLIExecutable, host); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(bin, bin+"-real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("bin-real", bin); err != nil {
		t.Fatal(err)
	}
	if err := verifyMountedFile(root, nil, agentCLIExecutable, host); err == nil {
		t.Fatal("accepted a symlink parent pointing to the expected inode")
	}
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(bin+"-real", bin); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(staged, target); err != nil {
		t.Fatal(err)
	}
	if err := verifyMountedFile(root, nil, agentCLIExecutable, host); err == nil {
		t.Fatal("accepted a symlink executable")
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("sdk"), 0555); err != nil {
		t.Fatal(err)
	}
	if err := verifyMountedFile(root, nil, agentCLIExecutable, host); err == nil {
		t.Fatal("accepted different inode with identical mode and content")
	}
}

func TestPodmanInitCgroup(t *testing.T) {
	id := strings.Repeat("a", 64)
	scope := "/user.slice/libpod-" + id + ".scope"
	for _, suffix := range []string{"", "/container"} {
		if !podmanContainerCgroup("0::"+scope+suffix+"\n", id) {
			t.Fatal("rejected container scope", suffix)
		}
	}
	for _, group := range []string{"0::/other/" + id, "0::" + scope + "/nested", "0::" + scope + "2", "0::" + strings.Replace(scope, "aaa", "bbb", 1)} {
		if podmanContainerCgroup(group, id) {
			t.Fatal("accepted unrelated cgroup", group)
		}
	}
}

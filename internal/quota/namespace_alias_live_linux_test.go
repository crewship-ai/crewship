//go:build linux && quota_live

package quota

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestLiveForeignMountNamespacePreventsDestructiveSnapshotOperations(t *testing.T) {
	root := liveAdmissionRoot(t)
	b, err := NewBackend(root, 128<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	key := Key{"crew", "namespace-probe", "data", 1}
	d, err := b.Ensure(ctx, key, MinBytes, Owner{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.Mount, "canary"), []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(liveAdmissionRoot(t), "alias")
	if err := os.Mkdir(alias, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/unshare", "--mount", "/bin/sh", "-c", `set -eu
/usr/bin/mount --bind "$1" "$2"
test -f "$2/canary"
printf 'ready\n'
read ignored || true
`, "quota-alias", d.Mount, alias)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	finished := false
	defer func() {
		input.Close()
		if !finished {
			_ = cmd.Wait()
		}
	}()
	if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("foreign namespace fixture not ready: %q %v", line, err)
	}
	if err := b.Remove(ctx, key); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign namespace did not block removal: %v", err)
	}
	if err := b.Export(ctx, key, MinBytes, io.Discard); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign namespace did not block offline export: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(d.Mount, "canary")); err != nil || string(got) != "preserved" {
		t.Fatalf("refused operations changed live data: %q %v", got, err)
	}
	input.Close()
	err = cmd.Wait()
	finished = true
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Remove(ctx, key); err != nil {
		t.Fatalf("departed namespace permanently blocked removal: %v", err)
	}
}

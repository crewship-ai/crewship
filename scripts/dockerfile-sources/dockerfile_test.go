package dockerfilesources

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// rootGoPackages lists module package directories imported transitively by
// shipped binaries. Keep nested paths: a selective COPY mooring/protocol/ is
// sufficient without copying an unrelated Rust toolchain/vendor source tree.
func rootGoPackages(t *testing.T, root string) []string {
	t.Helper()
	const module = "github.com/crewship-ai/crewship/"
	cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "./cmd/...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, module) {
			continue
		}
		seen[strings.TrimPrefix(line, module)] = true
	}
	dirs := make([]string, 0, len(seen))
	for d := range seen {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	return dirs
}

// backendStageCopies returns the top-level directories the backend stage
// copies from the build context (COPY <dir>/ ./<dir>/), ignoring --from.
func backendStageCopies(t *testing.T, dockerfile string) map[string]bool {
	t.Helper()
	f, err := os.Open(dockerfile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	copies := map[string]bool{}
	inBackend := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "FROM ") {
			inBackend = strings.HasSuffix(line, " AS backend") || strings.HasSuffix(line, " as backend")
			continue
		}
		if !inBackend || !strings.HasPrefix(line, "COPY ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || strings.HasPrefix(fields[1], "--from") {
			continue
		}
		for _, src := range fields[1 : len(fields)-1] {
			copies[strings.TrimSuffix(src, "/")] = true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if !inBackend && len(copies) == 0 {
		t.Fatal("no backend stage found in Dockerfile")
	}
	return copies
}

func TestBackendStageCopiesEveryRootGoPackage(t *testing.T) {
	root := filepath.Join("..", "..")
	copies := backendStageCopies(t, filepath.Join(root, "Dockerfile"))
	for _, dir := range rootGoPackages(t, root) {
		if backendPackageCopied(copies, dir) {
			continue
		}
		t.Errorf("Dockerfile backend stage does not COPY %s/ — the in-image go build cannot resolve github.com/crewship-ai/crewship/%s", dir, dir)
	}
}

func backendPackageCopied(copies map[string]bool, dir string) bool {
	for {
		if copies[dir] {
			return true
		}
		i := strings.LastIndexByte(dir, '/')
		if i < 0 {
			return false
		}
		dir = dir[:i]
	}
}

func TestBackendPackageCopyCoverageUsesDirectoryBoundaries(t *testing.T) {
	for _, test := range []struct {
		source, pkg string
		want        bool
	}{
		{"internal", "internal/api", true},
		{"mooring/protocol", "mooring/protocol", true},
		{"mooring/protocol", "mooring/protocol/generated/go", true},
		{"mooring/protocol", "mooring/protocol-other", false},
		{"mooring/protocol/generated/go", "mooring/protocol", false},
		{"mooring/protocol/generated/go", "mooring/client", false},
	} {
		if got := backendPackageCopied(map[string]bool{test.source: true}, test.pkg); got != test.want {
			t.Errorf("COPY %s for %s: got %v want %v", test.source, test.pkg, got, test.want)
		}
	}
}

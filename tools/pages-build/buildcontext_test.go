package pageprofile

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every COPY source in the Dockerfile must be in the embedded build context,
// or `crewship page enable` builds a different image than the repository does.
func TestWriteBuildContextCoversDockerfile(t *testing.T) {
	dir := t.TempDir()
	if err := WriteBuildContext(dir); err != nil {
		t.Fatal(err)
	}
	dockerfile, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	copies := 0
	scanner := bufio.NewScanner(bytes.NewReader(dockerfile))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 || fields[0] != "COPY" {
			continue
		}
		for _, source := range fields[1 : len(fields)-1] {
			if strings.HasPrefix(source, "--") {
				continue
			}
			copies++
			if _, err := os.Stat(filepath.Join(dir, source)); err != nil {
				t.Errorf("Dockerfile COPYs %s, which the embedded build context lacks", source)
			}
		}
	}
	if copies == 0 {
		t.Fatal("no COPY sources found in the Dockerfile")
	}
	for _, name := range buildContextFiles {
		want, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: written build context differs from the repository file", name)
		}
	}
}

//go:build unix

package pagebuild

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pageprofile "github.com/crewship-ai/crewship/tools/pages-build"
)

// TestLabeledImagesDockerCLI drives the real `docker image ls` invocation
// against a fake docker on PATH (the shared CI and dev hosts must not depend
// on, or be changed by, a live daemon): the label filter names this release's
// fingerprint, the listing includes untagged images, and only immutable IDs
// come back, de-duplicated in Docker's newest-first order (#3108).
func TestLabeledImagesDockerCLI(t *testing.T) {
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	newer := "sha256:" + strings.Repeat("b", 64)
	older := "sha256:" + strings.Repeat("a", 64)
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + args + "'\n" +
		"printf '%s\\n' " + newer + " " + newer + " not-an-id " + older + "\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	fp := pageprofile.ImageFingerprint()
	ids, err := LabeledImages(context.Background(), fp)
	if err != nil {
		t.Fatalf("LabeledImages: %v", err)
	}
	if strings.Join(ids, " ") != newer+" "+older {
		t.Fatalf("ids = %v, want [%s %s]", ids, newer, older)
	}
	raw, err := os.ReadFile(args)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"image", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label=" + pageprofile.ImageLabel + "=" + fp}
	if got := strings.Split(strings.TrimSpace(string(raw)), "\n"); strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("docker args = %q, want %q", got, want)
	}

	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LabeledImages(context.Background(), fp); err == nil {
		t.Fatal("a failing docker CLI must be an error, not an empty listing")
	}
}

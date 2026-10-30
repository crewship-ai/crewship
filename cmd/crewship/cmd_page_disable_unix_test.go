//go:build unix && !clionly

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pageprofile "github.com/crewship-ai/crewship/tools/pages-build"
)

// pageDisableFakeDocker puts a docker script on PATH that lists every image
// for any `image ls` filter (crew images inherit the Pages label from a base
// image, so a label filter matches them), answers `image inspect` by ID and
// records `image rm`. The real CLI hooks run against it.
func pageDisableFakeDocker(t *testing.T, images map[string][]string, labels map[string]string) (removed func() []string) {
	t.Helper()
	dir := t.TempDir()
	var ls strings.Builder
	for id, tags := range images {
		ls.WriteString(id + "\n")
		body, _ := json.Marshal([]map[string]any{{"Id": id, "RepoTags": tags, "Config": map[string]any{"Labels": map[string]string{pageprofile.ImageLabel: labels[id]}}}})
		if err := os.WriteFile(filepath.Join(dir, "inspect-"+strings.TrimPrefix(id, "sha256:")), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "ls"), []byte(ls.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
d='` + dir + `'
case "$1 $2" in
"version "*) echo 27.0.0 ;;
"image ls") cat "$d/ls" ;;
"image inspect") f="$d/inspect-${3#sha256:}"; if [ -f "$f" ]; then cat "$f"; else echo "Error: No such image: $3" >&2; exit 1; fi ;;
"image rm") printf '%s\n' "$3" >> "$d/removed"; echo "Deleted: $3" ;;
*) echo "unexpected docker call: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	// The fake comes first; the shell tools it uses stay reachable.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		raw, _ := os.ReadFile(filepath.Join(dir, "removed"))
		return strings.Fields(string(raw))
	}
}

// `crewship page disable --all` must remove only compiler images that
// `crewship page enable` recorded and tagged, never an image that merely
// carries the Pages label: crew cache images built FROM a labelled base
// inherit it (#3108 security review).
func TestPageDisableAllSparesLabelInheritingImages(t *testing.T) {
	fp := pageprofile.ImageFingerprint()
	genuine := "sha256:" + strings.Repeat("a", 64)
	crew := "sha256:" + strings.Repeat("c", 64)
	tag := "crewship-pages-build:" + fp[:12]
	removed := pageDisableFakeDocker(t,
		map[string][]string{genuine: {tag}, crew: {"crewship-cache:crew-1"}},
		map[string]string{genuine: fp, crew: fp})
	projects := t.TempDir()
	t.Setenv("CREWSHIP_PAGE_PROJECTS_PATH", projects)
	record, _ := json.Marshal(map[string]any{"version": 1, "images": map[string]any{fp: map[string]string{"id": genuine, "tag": tag}}})
	if err := os.WriteFile(filepath.Join(projects, "pages-build-image.json"), record, 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runDisableForTest(t, "--yes", "--all")
	if err != nil {
		t.Fatalf("disable --all: %v\n%s", err, stderr)
	}
	got := removed()
	for _, id := range got {
		if id == crew {
			t.Fatalf("disable --all removed the crew image %s that only inherited the label (removed %v)", crew, got)
		}
	}
	if len(got) != 1 || got[0] != genuine || strings.TrimSpace(stdout) != genuine {
		t.Fatalf("removed %v, stdout %q; want only the recorded compiler image %s", got, stdout, genuine)
	}
}

//go:build unix

package pagebuild

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDockerImage is one image the fake daemon knows.
type fakeDockerImage struct {
	ID       string
	RepoTags []string
	Labels   map[string]string
	Created  string
}

// installFakeDocker puts a docker script first on PATH that answers
// `image ls` (every image ID, in the given order, whatever the filter: the
// worst case for a caller that trusts the listing), `image inspect <ref>` by
// ID or tag, and records `image rm`. It returns the file `image rm` and every
// other call append to.
func installFakeDocker(t *testing.T, images ...fakeDockerImage) (calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	var ls strings.Builder
	for _, img := range images {
		ls.WriteString(img.ID + "\n")
		body, err := json.Marshal([]map[string]any{{"Id": img.ID, "RepoTags": img.RepoTags, "Created": img.Created, "Config": map[string]any{"Labels": img.Labels}}})
		if err != nil {
			t.Fatal(err)
		}
		refs := append([]string{img.ID}, img.RepoTags...)
		for _, ref := range refs {
			name := filepath.Join(dir, "inspect-"+strings.NewReplacer("/", "_", ":", "_").Replace(ref))
			if err := os.WriteFile(name, body, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "ls"), []byte(ls.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
d='` + dir + `'
printf '%s\n' "$*" >> "$d/calls"
case "$1 $2" in
"version "*) echo 27.0.0 ;;
"image ls") cat "$d/ls" ;;
"image inspect")
  ref=$(printf '%s' "$3" | tr '/:' '__')
  if [ -f "$d/inspect-$ref" ]; then cat "$d/inspect-$ref"; else echo "Error: No such image: $3" >&2; exit 1; fi ;;
"image rm") echo "Deleted: $3" ;;
*) echo "unexpected docker call: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	// The fake comes first; the shell tools it uses stay reachable.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

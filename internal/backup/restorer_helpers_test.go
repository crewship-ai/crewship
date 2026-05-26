package backup

import "testing"

func TestSymlinkSectionIsStrict_DefaultsToStrict(t *testing.T) {
	cases := []struct {
		name   string
		strict bool
	}{
		// Permissive sections — user content / dotfiles
		{"workspace/my-crew/file.txt", false},
		{"workspace/my-crew/node_modules/.bin/foo", false},
		{"volumes/my-crew/home/dotfile", false},
		{"volumes/my-crew/tools/bin/x", false},
		// Strict sections — agent memory + system service data
		{"memory/my-crew/MEMORY.md", true},
		{"system/my-crew/var-lib/redis/dump.rdb", true},
		// Unknown / future sections — fail closed
		{"foo/bar", true},
		{"random.txt", true},
		{"", true},
		{"db/dump.json", true},
		{"devcontainer/my-crew/devcontainer.json", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := symlinkSectionIsStrict(c.name); got != c.strict {
				t.Errorf("symlinkSectionIsStrict(%q) = %v, want %v", c.name, got, c.strict)
			}
		})
	}
}

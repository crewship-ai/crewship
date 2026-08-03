package memport

import (
	"errors"
	"io/fs"
	"path"
	"strings"
)

// ErrUnknownFormat is returned when a source tree matches no known
// layout. Callers surface it as "tell me the format" rather than
// falling back to a default: the cost of guessing wrong is foreign
// content written into somebody's agent memory.
var ErrUnknownFormat = errors.New("memport: unrecognised memory layout")

// crewshipTierFiles are the filenames only our own layout uses at the
// root of a .memory directory.
var crewshipTierFiles = []string{"AGENT.md", "CREW.md", "PERSONA.md", "pins.md", "lessons.md", "learned.md"}

// Detect identifies which layout fsys holds.
//
// The checks run most-specific first. Crewship is tested before OKF
// because our own export carries frontmatter too — a bundle we wrote is
// still ours, and round-tripping it through the OKF reader would lose
// the tier mapping the filenames already encode.
func Detect(fsys fs.FS) (Format, error) {
	names, err := walkFiles(fsys)
	if err != nil {
		return "", err
	}

	for _, n := range names {
		if path.Dir(n) == "." && containsFold(crewshipTierFiles, path.Base(n)) {
			return FormatCrewship, nil
		}
	}

	// NanoClaw: a groups/ directory whose children hold CLAUDE.md.
	// "some CLAUDE.md exists" is far too weak a signal on its own —
	// most repos have one.
	for _, n := range names {
		if strings.HasPrefix(n, "groups/") && path.Base(n) == "CLAUDE.md" {
			return FormatNanoClaw, nil
		}
	}

	// OpenClaw: at least one of its distinctive root files.
	for _, n := range names {
		if path.Dir(n) != "." {
			continue
		}
		switch path.Base(n) {
		case "SOUL.md", "IDENTITY.md", "MEMORY.md", "USER.md":
			return FormatOpenClaw, nil
		}
	}

	// OKF last: any markdown carrying YAML frontmatter.
	for _, n := range names {
		if !strings.EqualFold(path.Ext(n), ".md") {
			continue
		}
		b, err := fs.ReadFile(fsys, n)
		if err != nil {
			continue
		}
		if _, _, ok := splitFrontmatter(b); ok {
			return FormatOKF, nil
		}
	}

	return "", ErrUnknownFormat
}

// walkFiles returns every regular file path in fsys, sorted, so every
// caller sees the same order on every run.
func walkFiles(fsys fs.FS) ([]string, error) {
	var out []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		out = append(out, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// fs.WalkDir already yields lexical order; sorting again would be
	// redundant. The contract is documented here so a future reader
	// does not add a map in the middle and lose it.
	return out, nil
}

func containsFold(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.EqualFold(h, needle) {
			return true
		}
	}
	return false
}

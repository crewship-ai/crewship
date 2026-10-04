package devcontainer

import (
	"errors"
	"regexp"
	"strings"
)

var lockedToolTable = regexp.MustCompile(`^\[\[tools\.([A-Za-z0-9_-]+)\]\]$`)
var lockedVersion = regexp.MustCompile(`^version\s*=\s*"([0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?)"$`)
var nativeLockSchema = regexp.MustCompile(`^lockfile_version\s*=\s*3$`)

// LockedToolVersion reads the canonical v3 shape emitted by native mise lock.
// It is deliberately not a general TOML parser: multiline strings, ambiguous
// tool entries, missing schema and noncanonical versions are unsupported.
// Native mise validates the complete lock during the image's locked build.
func LockedToolVersion(bundle *MiseLockBundle, tool string) (string, error) {
	denied := errors.New("managed launch: unambiguous native v3 locked version required")
	if bundle == nil || bundle.Validate() != nil || !toolNameRe.MatchString(tool) {
		return "", denied
	}
	raw := bundle.Files["mise.lock"]
	if strings.Contains(raw, `"""`) || strings.Contains(raw, "'''") {
		return "", denied
	}
	selected, schema, entries := false, false, 0
	version := ""
	for _, line := range strings.Split(raw, "\n") {
		if i := miseFindCommentIndex(line); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "lockfile_version") {
			if schema || !nativeLockSchema.MatchString(line) {
				return "", denied
			}
			schema = true
			continue
		}
		if strings.HasPrefix(line, "[") {
			match := lockedToolTable.FindStringSubmatch(line)
			selected = len(match) == 2 && match[1] == tool
			if selected {
				entries++
				if entries > 1 {
					return "", denied
				}
			}
			continue
		}
		if selected && strings.HasPrefix(line, "version") {
			match := lockedVersion.FindStringSubmatch(line)
			if len(match) != 2 || version != "" {
				return "", denied
			}
			version = match[1]
		}
	}
	if !schema || entries != 1 || version == "" {
		return "", denied
	}
	return version, nil
}

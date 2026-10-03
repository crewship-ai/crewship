package devcontainer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
)

// MiseResolvedTool is mise's proposed version selection, not an observation
// of installed binaries. VersionChanged compares version sets only; source,
// checksum and auxiliary dependency changes are captured by the bundle digest.
type MiseResolvedTool struct {
	Name             string   `json:"name"`
	Backend          string   `json:"backend"`
	PreviousVersions []string `json:"previous_versions"`
	ResolvedVersions []string `json:"resolved_versions"`
	VersionChanged   bool     `json:"version_changed"`
}

var misePlanBackend = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}:[A-Za-z0-9_@/.-]{1,95}$`)
var misePlanVersion = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,128}$`)

func parseMiseResolutionReport(raw string) ([]MiseResolvedTool, error) {
	if len(raw) > 64<<10 {
		return nil, errors.New("mise resolve: version report exceeds limit")
	}
	var native []struct {
		Name    string   `json:"name"`
		Backend string   `json:"backend"`
		Old     []string `json:"old_versions"`
		New     []string `json:"new_versions"`
	}
	if err := json.Unmarshal([]byte(raw), &native); err != nil || native == nil || len(native) > 128 {
		return nil, errors.New("mise resolve: invalid native version report")
	}
	result := make([]MiseResolvedTool, 0, len(native))
	seen := map[string]bool{}
	normalize := func(values []string) ([]string, error) {
		if len(values) > 32 {
			return nil, errors.New("mise resolve: too many versions in native report")
		}
		out := append([]string{}, values...)
		for _, v := range out {
			if !misePlanVersion.MatchString(v) {
				return nil, errors.New("mise resolve: invalid version in native report")
			}
		}
		slices.Sort(out)
		return slices.Compact(out), nil
	}
	for _, entry := range native {
		if !toolNameRe.MatchString(entry.Name) || !misePlanBackend.MatchString(entry.Backend) || seen[entry.Name] || entry.Old == nil || entry.New == nil {
			return nil, errors.New("mise resolve: invalid or duplicate tool in native report")
		}
		seen[entry.Name] = true
		old, err := normalize(entry.Old)
		if err != nil {
			return nil, err
		}
		next, err := normalize(entry.New)
		if err != nil {
			return nil, err
		}
		result = append(result, MiseResolvedTool{Name: entry.Name, Backend: entry.Backend, PreviousVersions: old, ResolvedVersions: next, VersionChanged: !slices.Equal(old, next)})
	}
	slices.SortFunc(result, func(a, b MiseResolvedTool) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return result, nil
}

func miseSelectorsDigest(tools map[string]string) string {
	// Only map[string]string reaches this helper; encoding cannot fail. Go's
	// JSON encoder sorts map keys, so equivalent selector maps have one digest.
	raw, _ := json.Marshal(tools)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func miseLockDigest(bundle *MiseLockBundle) string {
	if bundle == nil {
		return ""
	}
	raw, _ := json.Marshal(bundle)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

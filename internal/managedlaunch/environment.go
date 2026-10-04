package managedlaunch

import (
	"errors"
	"regexp"
	"sort"
	"strings"
)

const SafePath = "/usr/local/bin:/usr/bin:/bin"

var envKeyPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)

func AllowedEnvKey(key string) bool {
	if key == "http_proxy" || key == "https_proxy" || key == "no_proxy" {
		return true
	}
	if !envKeyPattern.MatchString(key) || strings.HasPrefix(key, "LD_") || strings.HasPrefix(key, "DYLD_") {
		return false
	}
	switch key {
	case "PATH", "NODE_OPTIONS", "NODE_PATH", "BUN_OPTIONS", "PYTHONPATH", "PYTHONHOME", "GLIBC_TUNABLES", "GCONV_PATH", "ENV", "BASH_ENV", "SHELLOPTS", "BASHOPTS", "TMUX", "TMUX_PANE":
		return false
	}
	return true
}

// Environment keeps only keys supplied by the host's admission/preflight.
// The launcher uses these names to discard ALL other image-inherited keys.
func Environment(input []string) (values, keys []string, err error) {
	known := map[string]string{}
	for _, item := range input {
		key, value, found := strings.Cut(item, "=")
		if !found || strings.ContainsRune(value, 0) {
			return nil, nil, errors.New("managed launch: malformed environment")
		}
		if AllowedEnvKey(key) {
			known[key] = value
		}
	}
	for key := range known {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values = []string{"PATH=" + SafePath}
	for _, key := range keys {
		values = append(values, key+"="+known[key])
	}
	return
}

func SelectedEnvironment(d Descriptor, inherited []string) ([]string, error) {
	wanted := map[string]bool{}
	for _, key := range d.EnvKeys {
		if !AllowedEnvKey(key) || wanted[key] || len(wanted) >= 256 {
			return nil, errors.New("managed launch: unsafe environment selection")
		}
		wanted[key] = true
	}
	var selected []string
	for _, item := range inherited {
		key, _, _ := strings.Cut(item, "=")
		if wanted[key] {
			selected = append(selected, item)
			delete(wanted, key)
		}
	}
	if len(wanted) != 0 {
		return nil, errors.New("managed launch: missing authoritative environment")
	}
	values, _, err := Environment(selected)
	return values, err
}

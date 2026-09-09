// Package serviceconfig defines the public boundary for service configuration.
package serviceconfig

import (
	"encoding/json"
	"io"
	"strings"
)

// Redacted is deliberately not a services array: it must never be accepted as
// deployable configuration, or mistaken for an empty service list.
const Redacted = "[service configuration withheld: contains private runtime settings]"

// Public withholds literal-bearing configurations, independently of user role.
// Crew read permission is not permission to reveal a credential. Inspecting
// secret-looking variable names is insufficient (Redis uses command arguments,
// and a password can appear under any environment key).
// Unknown fields and malformed stored data fail closed.
func Public(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return raw
	}
	var services []struct {
		Name    string   `json:"name"`
		Image   string   `json:"image"`
		Ports   []string `json:"ports"`
		EnvRefs []string `json:"env_refs"`
		Volumes []struct {
			Name  string `json:"name"`
			Mount string `json:"mount"`
		} `json:"volumes"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&services) != nil {
		return Redacted
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return Redacted
	}
	return raw
}

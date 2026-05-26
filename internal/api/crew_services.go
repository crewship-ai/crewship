package api

// Crew services helpers. The wire format for services is an opaque
// JSON document stored in crews.services_json; this file validates
// that document so a malformed payload never reaches the docker
// provider. The resolve-to-provider.CrewService translation lives in
// internal/chatbridge/services.go (kept duplicated to avoid a
// chatbridge → api dependency cycle).
//
// The on-disk JSON mirrors internal/manifest.Service: it's the same
// shape the manifest emits via apply, so the round-trip is
// byte-exact (modulo whitespace) and the server stores what the
// user authored.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// serviceWire is the JSON shape we accept on the wire and emit back
// to GET. Field tags match the manifest's Service struct.
type serviceWire struct {
	Name        string                  `json:"name"`
	Image       string                  `json:"image"`
	Command     []string                `json:"command,omitempty"`
	Env         map[string]string       `json:"env,omitempty"`
	EnvRefs     []string                `json:"env_refs,omitempty"`
	Ports       []string                `json:"ports,omitempty"`
	Volumes     []serviceVolumeWire     `json:"volumes,omitempty"`
	Healthcheck *serviceHealthcheckWire `json:"healthcheck,omitempty"`
}

type serviceVolumeWire struct {
	Name  string `json:"name"`
	Mount string `json:"mount"`
}

type serviceHealthcheckWire struct {
	Test        []string `json:"test"`
	Interval    string   `json:"interval,omitempty"`
	Timeout     string   `json:"timeout,omitempty"`
	Retries     int      `json:"retries,omitempty"`
	StartPeriod string   `json:"start_period,omitempty"`
}

// serviceNameRe mirrors internal/manifest.serviceNameRe — kept
// duplicated rather than imported so package api stays free of a
// dependency on package manifest (which imports cli, which would
// otherwise introduce a cycle with the future webhook signing path).
// RFC 1035 DNS label: 1–63 chars, lowercase letters/digits/'-',
// must start with letter and end with letter or digit.
var serviceNameRe = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// validateServicesJSON enforces the per-service shape so a
// malformed document never reaches the docker provider. Reads the
// JSON twice (Unmarshal + each-service inspection) to keep error
// messages keyed to the failing service name rather than a generic
// "invalid JSON" verdict.
func validateServicesJSON(body string) error {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	var services []serviceWire
	if err := json.Unmarshal([]byte(body), &services); err != nil {
		return fmt.Errorf("not valid JSON array of services: %w", err)
	}
	seen := map[string]bool{}
	for i, s := range services {
		if s.Name == "" {
			return fmt.Errorf("services[%d]: name is required", i)
		}
		if !serviceNameRe.MatchString(s.Name) {
			return fmt.Errorf("services[%q]: name must be a DNS label", s.Name)
		}
		if seen[s.Name] {
			return fmt.Errorf("services[%q]: duplicate name", s.Name)
		}
		seen[s.Name] = true
		if s.Image == "" {
			return fmt.Errorf("services[%q]: image is required", s.Name)
		}
		seenVol := map[string]bool{}
		for j, v := range s.Volumes {
			if v.Name == "" || v.Mount == "" {
				return fmt.Errorf("services[%q].volumes[%d]: name and mount are required", s.Name, j)
			}
			if strings.HasPrefix(v.Name, "/") || strings.HasPrefix(v.Name, ".") {
				return fmt.Errorf("services[%q].volumes[%q]: bind mounts are not supported; use a named volume", s.Name, v.Name)
			}
			if seenVol[v.Mount] {
				return fmt.Errorf("services[%q]: duplicate mount %q", s.Name, v.Mount)
			}
			seenVol[v.Mount] = true
		}
		if s.Healthcheck != nil {
			if len(s.Healthcheck.Test) == 0 {
				return fmt.Errorf("services[%q]: healthcheck declared without test command", s.Name)
			}
			// Parse-validate each duration string up front so a
			// typo ("5sec" instead of "5s") is reported at write
			// time. Without this, chatbridge.parseDuration silently
			// defaults any unparseable value to its hardcoded
			// fallback, hiding config drift behind a happy-looking
			// runtime.
			for fieldName, value := range map[string]string{
				"interval":     s.Healthcheck.Interval,
				"timeout":      s.Healthcheck.Timeout,
				"start_period": s.Healthcheck.StartPeriod,
			} {
				if value == "" {
					continue
				}
				if _, err := time.ParseDuration(value); err != nil {
					return fmt.Errorf("services[%q]: healthcheck.%s %q is not a valid duration: %w", s.Name, fieldName, value, err)
				}
			}
		}
	}
	return nil
}

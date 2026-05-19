package api

// Crew services helpers. The wire format for services is an opaque
// JSON document stored in crews.services_json; this file parses,
// validates, and translates it into the provider.CrewService shape
// the docker provider consumes at EnsureCrewRuntime time.
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

	"github.com/crewship-ai/crewship/internal/provider"
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
var serviceNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}[a-z0-9]$`)

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
		if s.Healthcheck != nil && len(s.Healthcheck.Test) == 0 {
			return fmt.Errorf("services[%q]: healthcheck declared without test command", s.Name)
		}
	}
	return nil
}

// servicesFromJSON deserialises services_json from a crew row and
// resolves env_refs against the workspace's credentials table to
// produce a provider.CrewService slice ready for the docker
// provider. resolver is a function that, given an env var name,
// returns its plaintext value (or empty when the credential is
// PENDING; the provider treats empty as "skip injection").
//
// envValueFor MUST be a closure over the workspace's credential
// vault — never a literal map exposed by the request body. The
// only call path is loadCrewServices below.
func servicesFromJSON(body string, envValueFor func(envVar string) string) ([]provider.CrewService, error) {
	if strings.TrimSpace(body) == "" {
		return nil, nil
	}
	var services []serviceWire
	if err := json.Unmarshal([]byte(body), &services); err != nil {
		return nil, fmt.Errorf("unmarshal services_json: %w", err)
	}
	out := make([]provider.CrewService, 0, len(services))
	for _, s := range services {
		env := map[string]string{}
		for k, v := range s.Env {
			env[k] = v
		}
		for _, ref := range s.EnvRefs {
			if v := envValueFor(ref); v != "" {
				env[ref] = v
			}
			// PENDING / missing credentials simply don't get
			// injected. The sidecar may still start (e.g. Postgres
			// with no POSTGRES_PASSWORD just fails inside its
			// entrypoint) and the operator sees the failure mode
			// they'd expect from a half-configured deploy.
		}
		vols := make([]provider.CrewServiceVolume, 0, len(s.Volumes))
		for _, v := range s.Volumes {
			vols = append(vols, provider.CrewServiceVolume{Name: v.Name, Mount: v.Mount})
		}
		var hc *provider.CrewServiceHealthcheck
		if s.Healthcheck != nil {
			hc = &provider.CrewServiceHealthcheck{
				Test:    s.Healthcheck.Test,
				Retries: s.Healthcheck.Retries,
			}
			hc.Interval = parseDuration(s.Healthcheck.Interval, 5*time.Second)
			hc.Timeout = parseDuration(s.Healthcheck.Timeout, 3*time.Second)
			hc.StartPeriod = parseDuration(s.Healthcheck.StartPeriod, 0)
		}
		out = append(out, provider.CrewService{
			Name:        s.Name,
			Image:       s.Image,
			Command:     s.Command,
			Env:         env,
			Ports:       s.Ports,
			Volumes:     vols,
			Healthcheck: hc,
		})
	}
	return out, nil
}

func parseDuration(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

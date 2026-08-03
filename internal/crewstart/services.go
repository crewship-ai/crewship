package crewstart

// The crews.services_json decoder.
//
// The on-disk JSON shape is owned by package api (which writes + validates it).
// It is re-parsed at crew-start time into a strongly-typed provider.CrewService
// slice with env_refs resolved. This lived in internal/chatbridge, which is why
// only the chat path ever produced sidecars: the decoder and the code that
// started them were in the same package as one of the thirteen callers. It sits
// with the crew-start contract now, so every path decodes the same bytes the
// same way — and, just as importantly, produces the same sidecar spec hash, so
// the docker provider reattaches to a running sidecar instead of recreating it
// when a crew is next started from a different path.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

type serviceWire struct {
	Name        string              `json:"name"`
	Image       string              `json:"image"`
	Command     []string            `json:"command,omitempty"`
	Env         map[string]string   `json:"env,omitempty"`
	EnvRefs     []string            `json:"env_refs,omitempty"`
	Ports       []string            `json:"ports,omitempty"`
	Volumes     []serviceVolumeWire `json:"volumes,omitempty"`
	Healthcheck *serviceHealthWire  `json:"healthcheck,omitempty"`
}

type serviceVolumeWire struct {
	Name  string `json:"name"`
	Mount string `json:"mount"`
}

type serviceHealthWire struct {
	Test        []string `json:"test"`
	Interval    string   `json:"interval,omitempty"`
	Timeout     string   `json:"timeout,omitempty"`
	Retries     int      `json:"retries,omitempty"`
	StartPeriod string   `json:"start_period,omitempty"`
}

// DecodeServices parses services_json and resolves env_refs via the supplied
// lookup. The lookup returns "" for missing or PENDING credentials; those env
// vars are silently omitted from the sidecar's environment so docker doesn't
// see a half-populated KEY= line that some upstream images choke on.
//
// envValueFor may be nil, which resolves every ref to "". Auto-managed sidecar
// credentials (internal/manifest/auto_managed.go) travel as literal Env values
// rather than refs, so that is the common, fully-working case.
func DecodeServices(body string, envValueFor func(envVar string) string) ([]provider.CrewService, error) {
	if strings.TrimSpace(body) == "" {
		return nil, nil
	}
	var services []serviceWire
	if err := json.Unmarshal([]byte(body), &services); err != nil {
		return nil, fmt.Errorf("services_json: %w", err)
	}
	lookup := envValueFor
	if lookup == nil {
		lookup = func(string) string { return "" }
	}

	out := make([]provider.CrewService, 0, len(services))
	for i, s := range services {
		// Lightweight schema guards. services_json is validated on write by the
		// API layer, but a stored row could still go stale through a manual DB
		// edit or an older crewship version writing a different shape. Catching
		// it here lets the caller treat the run as "sidecars not configured"
		// instead of hard-failing in the docker provider with a less actionable
		// error.
		if strings.TrimSpace(s.Name) == "" || strings.TrimSpace(s.Image) == "" {
			return nil, fmt.Errorf("services[%d]: name and image are required", i)
		}
		if s.Healthcheck != nil && len(s.Healthcheck.Test) == 0 {
			return nil, fmt.Errorf("services[%q]: healthcheck declared without a test command", s.Name)
		}
		env := map[string]string{}
		for k, v := range s.Env {
			env[k] = v
		}
		for _, ref := range s.EnvRefs {
			if v := lookup(ref); v != "" {
				env[ref] = v
			}
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

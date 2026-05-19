package chatbridge

// Crew services decoder. The on-disk JSON shape is owned by package
// api (which writes + validates it); chatbridge re-parses it here
// at run-start time so the provider gets a strongly-typed
// provider.CrewService slice with env_refs already resolved against
// the workspace credential vault.
//
// Keeping this in chatbridge (rather than calling into a public
// api.servicesFromJSON) avoids a chatbridge → api dependency that
// would invert the current direction (api → chatbridge via the
// ChatHandler interface).

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
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

// decodeServicesForRuntime parses services_json and resolves
// env_refs via the supplied lookup. The lookup returns "" for
// missing or PENDING credentials; those env vars are silently
// omitted from the sidecar's environment so docker doesn't see a
// half-populated KEY= line that some upstream images choke on.
func decodeServicesForRuntime(body string, envValueFor func(envVar string) string) ([]provider.CrewService, error) {
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
	for _, s := range services {
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

// buildServiceEnvLookup returns a closure that, given an env var
// name, looks up its plaintext value across the agent's resolved
// credentials. Sidecar services use env_refs (a slug list) and
// this is where those slugs become actual values — without going
// through the orchestrator path that would otherwise inject them
// into the agent's own env.
//
// Workspace credentials with status=PENDING land here as empty
// PlainValue strings; the caller (decodeServicesForRuntime) drops
// such entries from the sidecar's env so we don't pass a
// half-populated KEY= line that some images choke on.
func buildServiceEnvLookup(creds []orchestrator.Credential) func(envVar string) string {
	byName := make(map[string]string, len(creds))
	for _, c := range creds {
		byName[c.EnvVarName] = c.PlainValue
	}
	return func(envVar string) string {
		return byName[envVar]
	}
}

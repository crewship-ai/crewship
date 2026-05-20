package manifest

// Auto-managed sidecar credentials — the apply-time machinery that
// turns a `services: [{ name: postgres, image: postgres:16-alpine }]`
// block into:
//
//   1. Generated 32-byte values, one per declared (explicit + sugar)
//      AutoCredential entry.
//   2. Sidecar containers that boot with the value already in their
//      env (POSTGRES_PASSWORD set, postgres no longer refuses to
//      start with "superuser password is not specified").
//   3. Encrypted credential rows in the workspace, attributed to the
//      crew's LEAD agent so the UI surfaces "Created by trapper" on
//      the row.
//   4. Crew agents whose env_refs are auto-extended to include each
//      credential name, so the agent container also receives
//      POSTGRES_PASSWORD at runtime.
//
// Threat model (justification for plaintext in services_json):
//
//   For the v1 release, the generated value lives in TWO places in
//   the workspace DB:
//
//     - credentials.encrypted_value: AES-256-GCM encrypted with
//       ENCRYPTION_KEY. Used by the UI / audit / future rotation.
//
//     - crews.services_json: plaintext, embedded in the sidecar
//       env literal. Used by the docker provider at sidecar start
//       — the runtime path currently has no env_refs resolution for
//       sidecars (separate gap tracked as a bug; see PR description),
//       so the value must travel literally to be reachable.
//
//   This duplicates the secret into a non-encrypted column. It is
//   bounded to crew-private sidecars whose port is NOT published on
//   the host — the validator refuses auto_credentials on services
//   with `ports:` that escape the bridge — so an attacker who can
//   read the workspace DB also already controls bridge isolation
//   and the threat model is "host root", under which a separate
//   encrypted column doesn't help. A future PR moves the literal
//   value to an encrypted sibling column once the sidecar env-refs
//   runtime path is wired.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// expandAutoCredentialsInCrewSpec mutates the CrewSpec in place to
// implement the AUTO_MANAGED contract before the rest of the plan
// pipeline serialises it. The mutations are intentionally
// idempotent: a second call on the same spec with the same generated
// values is a no-op.
//
// Returned slice carries the (cred-name, generated-value, sugar-
// description, lead-required) tuples that the credential-create
// closure needs later — the closure runs AFTER the crew + agents
// are created, at which point we can look up the lead agent ID for
// attribution and POST the credential row.
//
// The function uses crypto/rand for value generation. Errors are
// purely "rand source failed," which on any modern platform means
// the host has bigger problems than a manifest apply.
func expandAutoCredentialsInCrewSpec(spec *CrewSpec) ([]plannedAutoCredential, error) {
	if spec == nil || len(spec.Services) == 0 {
		return nil, nil
	}

	// Track names so we can detect cross-service collisions early —
	// two services can't both declare POSTGRES_PASSWORD because the
	// workspace credentials table is keyed by name.
	seen := make(map[string]string) // name -> first service

	var out []plannedAutoCredential
	for i := range spec.Services {
		svc := &spec.Services[i]
		// Apply sugar defaults to env (postgres → POSTGRES_USER=postgres etc.)
		if resolved := svc.ResolveEnv(); resolved != nil {
			svc.Env = resolved
		}
		autoCreds := svc.ResolveAutoCredentials()
		if len(autoCreds) == 0 {
			continue
		}
		for _, ac := range autoCreds {
			if first, dup := seen[ac.Name]; dup {
				return nil, fmt.Errorf(
					"auto_credential %q is declared by both service %q and %q (names must be unique within a workspace)",
					ac.Name, first, svc.Name)
			}
			seen[ac.Name] = svc.Name

			value, err := generateAutoCredentialValue(ac.EffectiveLength())
			if err != nil {
				return nil, fmt.Errorf("generate value for %s: %w", ac.Name, err)
			}

			// Sidecar env: write under inject_as_env (often same as Name).
			if svc.Env == nil {
				svc.Env = make(map[string]string, 1)
			}
			svc.Env[ac.EffectiveInjectAsEnv()] = value

			// Agent env_refs: append to every agent in the crew that
			// inject_to_agents=true asks us to reach.
			if ac.EffectiveInjectToAgents() {
				for ai := range spec.Agents {
					ag := &spec.Agents[ai]
					if !containsString(ag.EnvRefs, ac.Name) {
						ag.EnvRefs = append(ag.EnvRefs, ac.Name)
					}
				}
			}

			out = append(out, plannedAutoCredential{
				Name:                  ac.Name,
				Value:                 value,
				Description:           ac.Description,
				ProvisionedForService: svc.Name, // crew slug joined at plan time
			})
		}
	}
	return out, nil
}

// plannedAutoCredential carries everything the deferred credential-
// create closure needs to write a row after the crew + agents land.
// Value is captured at plan time (so the closure runs with the same
// value already embedded in services_json) — the plan output itself
// never renders Value, only the metadata around it.
type plannedAutoCredential struct {
	Name                  string
	Value                 string
	Description           string
	ProvisionedForService string // "<service-name>", joined with crew slug at exec
}

// generateAutoCredentialValue returns hex(crypto-random(bytes)). Hex
// (not base64) so the resulting env var is safe to embed in shell
// scripts, container env files, and Docker --env arg lists without
// quoting concerns. The length parameter is in bytes; the hex output
// is 2× that many characters.
func generateAutoCredentialValue(bytes int) (string, error) {
	if bytes <= 0 {
		bytes = 32
	}
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("crypto/rand: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// deepCopyCrewSpec returns a CrewSpec safe to mutate without
// affecting the caller's manifest. Only the slices and maps we
// touch in expandAutoCredentialsInCrewSpec (Services[i].Env,
// Agents[i].EnvRefs) are deep-copied; the rest is a shallow copy
// because nothing else in the auto-managed path mutates it.
func deepCopyCrewSpec(in *CrewSpec) CrewSpec {
	if in == nil {
		return CrewSpec{}
	}
	out := *in

	if len(in.Services) > 0 {
		out.Services = make([]Service, len(in.Services))
		copy(out.Services, in.Services)
		for i := range out.Services {
			if in.Services[i].Env != nil {
				dup := make(map[string]string, len(in.Services[i].Env))
				for k, v := range in.Services[i].Env {
					dup[k] = v
				}
				out.Services[i].Env = dup
			}
		}
	}

	if len(in.Agents) > 0 {
		out.Agents = make([]Agent, len(in.Agents))
		copy(out.Agents, in.Agents)
		for i := range out.Agents {
			if len(in.Agents[i].EnvRefs) > 0 {
				dup := make([]string, len(in.Agents[i].EnvRefs))
				copy(dup, in.Agents[i].EnvRefs)
				out.Agents[i].EnvRefs = dup
			}
		}
	}

	return out
}

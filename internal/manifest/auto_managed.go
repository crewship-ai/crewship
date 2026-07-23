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
	"encoding/json"
	"fmt"
)

// expandAutoCredentialsInCrewSpec mutates the CrewSpec in place to
// implement the AUTO_MANAGED contract before the rest of the plan
// pipeline serialises it. The mutations are idempotent across
// re-applies: when an existing crew already has the value baked into
// its services_json, the same value is reused instead of regenerated.
// That guarantee is load-bearing — sidecars boot from services_json,
// credential rows live in the DB, and the two MUST agree.
//
// existingServicesJSON, when non-empty, is the current state of
// crews.services_json for the crew being re-applied. It's parsed
// best-effort: malformed JSON is treated as "no prior state" and
// fresh values are generated. Pre-existing values that don't satisfy
// the auto_credential's Length / shape requirements are also
// regenerated — the validator enforces shape at the manifest
// boundary, so any stored value that fails the same check is
// treated as drift to repair.
//
// Returned slice carries the (cred-name, generated-or-reused-value,
// description) tuples the credential-create closure needs later.
// The closure runs AFTER the crew + agents are created.
//
// The function uses crypto/rand for fresh value generation. Errors
// are purely "rand source failed," which on any modern platform
// means the host has bigger problems than a manifest apply.
func expandAutoCredentialsInCrewSpec(spec *CrewSpec, existingServicesJSON string) ([]plannedAutoCredential, error) {
	if spec == nil || len(spec.Services) == 0 {
		return nil, nil
	}

	// Parse the existing services_json into a name → Service lookup so
	// we can pull out the prior value per service — from Env for the
	// env-injected creds, or from Command for command-injected ones
	// (redis --requirepass), which is where their value lives.
	priorByService := parseExistingServices(existingServicesJSON)

	// Track names so we can detect cross-service collisions early —
	// two services can't both declare POSTGRES_PASSWORD because the
	// workspace credentials table is keyed by name.
	seen := make(map[string]string) // name -> first service

	var out []plannedAutoCredential
	for i := range spec.Services {
		svc := &spec.Services[i]

		// Snapshot the env keys the operator literally pinned in the
		// manifest, BEFORE ResolveEnv overlays catalog defaults. This
		// is the only way to tell "operator wrote
		// POSTGRES_PASSWORD: my-literal" apart from "catalog set
		// POSTGRES_USER=postgres" once the two maps have merged.
		//
		// The sidecar catalog deliberately never lists an
		// auto-credential's inject_as_env as a default in its Env map
		// (passwords are minted, not defaulted), so any key collision
		// here is genuinely operator-pinned and we treat it as
		// authoritative — skip generation, skip the DB row, skip the
		// agent env_refs append. Mirrors the schema docstring on
		// ResolveEnv: "Operator values always win on key collision."
		operatorPinned := make(map[string]bool, len(svc.Env))
		for k, v := range svc.Env {
			if v != "" {
				operatorPinned[k] = true
			}
		}

		// Apply sugar defaults to env (postgres → POSTGRES_USER=postgres etc.)
		if resolved := svc.ResolveEnv(); resolved != nil {
			svc.Env = resolved
		}
		autoCreds := svc.ResolveAutoCredentials()
		if len(autoCreds) == 0 {
			continue
		}
		for _, ac := range autoCreds {
			injectKey := ac.EffectiveInjectAsEnv()
			cmdInject := len(ac.InjectAsCommand) > 0

			// Precedence: an operator who already declared the auth for
			// this service owns the secret — we skip generation, the
			// credential row, and the agent env_ref append. For env-
			// injected creds that means an operator-pinned env literal;
			// for command-injected creds (redis) it means a non-empty
			// operator-supplied Command (they wrote their own
			// --requirepass, or any other command). This mirrors the
			// "operator values always win on collision" contract.
			if cmdInject {
				if len(svc.Command) > 0 {
					continue
				}
			} else if operatorPinned[injectKey] {
				// Operator-pinned literal wins; no sugar generation,
				// no credential row, no agent env_refs append.
				// svc.Env already carries the operator's value from
				// the ResolveEnv overlay above.
				continue
			}
			if first, dup := seen[ac.Name]; dup {
				return nil, fmt.Errorf(
					"auto_credential %q is declared by both service %q and %q (names must be unique within a workspace)",
					ac.Name, first, svc.Name)
			}
			seen[ac.Name] = svc.Name

			value, reused := reuseOrGenerate(ac, priorByService[svc.Name])
			if !reused {
				v, err := generateAutoCredentialValue(ac.EffectiveLength())
				if err != nil {
					return nil, fmt.Errorf("generate value for %s: %w", ac.Name, err)
				}
				value = v
			}

			if cmdInject {
				// Command channel: the sidecar receives the value via
				// its argv, never as an env literal. The rendered
				// command lands in services_json (invariant: the value
				// still travels literally so the provider can boot the
				// container), exactly like the env channel does.
				svc.Command = renderCommandTemplate(ac.InjectAsCommand, value)
			} else {
				// Sidecar env: write under inject_as_env (often same as Name).
				if svc.Env == nil {
					svc.Env = make(map[string]string, 1)
				}
				svc.Env[injectKey] = value
			}

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
				ProvisionedForService: svc.Name,
			})
		}
	}
	return out, nil
}

// reuseOrGenerate returns (value, true) when the prior service carries
// a value that satisfies the AutoCredential's length contract (≥
// EffectiveLength * 2 hex chars). The prior value is read from the
// channel the credential injects into: the prior Command for
// command-injected creds (redis --requirepass), the prior Env under
// inject_as_env otherwise. Anything else returns ("", false) so the
// caller generates fresh — including malformed prior content, drift,
// or first-ever apply (prior is the zero Service).
//
// Why the length check: an operator who lowered the auto_credential
// length in the manifest expects a fresh shorter value, and an
// operator who raised it expects a fresh longer one. Reusing values
// that no longer satisfy the declared length would leave the
// manifest and the DB out of sync.
func reuseOrGenerate(ac AutoCredential, prior Service) (string, bool) {
	var value string
	if len(ac.InjectAsCommand) > 0 {
		v, ok := extractCommandArgValue(ac.InjectAsCommand, prior.Command)
		if !ok {
			return "", false
		}
		value = v
	} else {
		if prior.Env == nil {
			return "", false
		}
		v, ok := prior.Env[ac.EffectiveInjectAsEnv()]
		if !ok || v == "" {
			return "", false
		}
		value = v
	}
	wantChars := ac.EffectiveLength() * 2 // hex doubles byte count
	if len(value) != wantChars {
		return "", false
	}
	// Confirm the prior value is hex — a hand-edited services_json
	// could carry anything; we'd rather regen than copy junk forward.
	if _, err := hex.DecodeString(value); err != nil {
		return "", false
	}
	return value, true
}

// renderCommandTemplate returns a fresh argv with every
// autoCredentialValuePlaceholder element replaced by value. The input
// template is never mutated — the catalog entry is a package global
// shared across every crew, so aliasing it would leak one crew's
// secret into another's command.
func renderCommandTemplate(template []string, value string) []string {
	out := make([]string, len(template))
	for i, tok := range template {
		if tok == autoCredentialValuePlaceholder {
			out[i] = value
			continue
		}
		out[i] = tok
	}
	return out
}

// extractCommandArgValue recovers the value previously spliced into a
// rendered command by renderCommandTemplate, so an idempotent re-apply
// can reuse it instead of rotating the secret. It returns ok=false on
// any structural mismatch (different length, a fixed token that no
// longer matches, an empty prior) — the caller then regenerates. When
// the template has multiple placeholders they must all have resolved
// to the same value; a disagreement is treated as drift (ok=false).
func extractCommandArgValue(template, prior []string) (string, bool) {
	if len(prior) != len(template) || len(template) == 0 {
		return "", false
	}
	found := false
	var value string
	for i, tok := range template {
		if tok == autoCredentialValuePlaceholder {
			if found && prior[i] != value {
				return "", false
			}
			value = prior[i]
			found = true
			continue
		}
		if prior[i] != tok {
			return "", false
		}
	}
	if !found || value == "" {
		return "", false
	}
	return value, true
}

// parseExistingServices decodes a services_json blob into a
// service-name → Service lookup. The full Service is retained (not just
// Env) so the reuse path can recover a command-injected value from the
// prior Command as well as an env value from the prior Env. Returns nil
// when the input is empty or unparsable — the caller treats a nil map
// as "first apply, generate fresh." We intentionally don't surface
// parse errors: services_json on a real crew is server-authored, but
// operator drift can land there too, and a malformed prior shouldn't
// block a fresh-value path.
func parseExistingServices(servicesJSON string) map[string]Service {
	if servicesJSON == "" {
		return nil
	}
	var services []Service
	if err := json.Unmarshal([]byte(servicesJSON), &services); err != nil {
		return nil
	}
	if len(services) == 0 {
		return nil
	}
	out := make(map[string]Service, len(services))
	for i := range services {
		out[services[i].Name] = services[i]
	}
	return out
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

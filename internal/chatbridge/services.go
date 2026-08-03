package chatbridge

// The sidecar credential lookup for the chat/agent-resolve path.
//
// The services_json DECODER used to live here too, which is a large part of
// why only the chat path ever started a crew's sidecars: the code that turned
// the column into provider.CrewService entries sat inside one of the thirteen
// callers. It now lives with the crew-start contract
// (internal/crewstart.DecodeServices) and every path decodes the same way.

import (
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

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

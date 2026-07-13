package governance

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// WatchPresets is the catalog of admin-selectable watch presets (issue #1001,
// M1). A preset is a stable key whose value is the human rule injected into the
// Keeper evaluator prompt when the preset is enabled for a workspace. The keys
// are the wire/storage identity (stored in keeper_governance_settings.watch_presets
// as a JSON array); the values are the actual instruction text the governance
// model sees, so treat wording changes as a product decision, not a refactor.
//
// Presets are additive to the evaluator's built-in anti-pattern list — enabling
// none leaves the built-ins in force.
var WatchPresets = map[string]string{
	"credentials":  "Flag credential access that is disproportionate to the stated task — rapid or bulk access to multiple secrets, access to high-security-level credentials without a matching justification, or credential reads unrelated to the current work.",
	"egress":       "Flag network egress or tool calls that look like data exfiltration — posting data to non-allowlisted hosts, encoding/piping secrets to the network, or unexpected outbound connections.",
	"memory":       "Flag writes or deletes to agent/crew memory that look like tampering — overwriting prior facts, mass deletions, or planting misleading entries.",
	"destructive":  "Flag destructive filesystem or database operations without verification — recursive deletes (rm -rf), DROP/TRUNCATE/DELETE without a WHERE clause, force-pushes, or overwriting source files wholesale.",
	"secret_files": "Flag reads of sensitive files — ~/.ssh, id_rsa/id_ed25519, .env files, cloud credential files (~/.aws, ~/.config/gcloud), or private keys.",
}

// MaxWatchSpecLen bounds the free-form watch_spec at the storage/API layer so a
// single workspace can't persist an unbounded rule blob. The API rejects a
// longer spec with 400; kept here next to the catalog so the CLI can mirror the
// check client-side.
const MaxWatchSpecLen = 4096

// maxCompiledWatchSpecLen caps the *compiled* prompt block (all enabled presets
// + the free-form spec). It is a backstop against a pathological spec pushing
// the strict-JSON instruction line out of the model's attention — legitimate
// input (the five presets ≈1 KB plus a MaxWatchSpecLen free-form spec) fits
// comfortably under it, so it never truncates a well-formed policy.
const maxCompiledWatchSpecLen = 8192

const watchSpecTruncMarker = "…(truncated)"

// PresetKeys returns the catalog keys in sorted order. CompileWatchSpec and the
// API/CLI validation share this so preset expansion is deterministic.
func PresetKeys() []string {
	keys := make([]string, 0, len(WatchPresets))
	for k := range WatchPresets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ValidatePresets rejects any key not in the catalog. Called at the API layer
// (400 on unknown) and mirrored client-side by the CLI. nil/empty is valid.
func ValidatePresets(keys []string) error {
	for _, k := range keys {
		if _, ok := WatchPresets[k]; !ok {
			return fmt.Errorf("unknown watch preset %q", k)
		}
	}
	return nil
}

// ResolveWatchBlock returns the watch-spec prompt block that should actually be
// injected for a workspace, or "" when none applies. It is the exact function
// the gatekeeper's resolver wraps.
//
// It gates on Settings.Enabled: the watch spec is part of the *behavioral*
// watchdog, which is opt-in and default OFF per workspace (issue #1001, M0). A
// disabled workspace has no active policy, so authoring a spec is inert until an
// OWNER/ADMIN runs `keeper enable` — this is the contract the CLI and docs
// promise, and it keeps a merely-authored spec from silently activating on the
// always-on credential-access evaluator path.
func ResolveWatchBlock(s Settings) string {
	if !s.Enabled {
		return ""
	}
	return CompileWatchSpec(s)
}

// CompileWatchSpec renders a workspace's enabled presets + free-form rules into
// a single prompt block, or "" when there are none. The gatekeeper wraps the
// result in an authoritative "[WORKSPACE WATCH POLICY …]" label and places it
// above the untrusted fences (see internal/keeper/gatekeeper) — this function
// only produces the rule body.
//
// Presets expand in sorted-key order (deterministic for the replay/determinism
// harness); unknown preset keys are skipped rather than surfaced as noise. The
// free-form spec is appended after the presets. The whole block is length-capped
// (maxCompiledWatchSpecLen) as a prompt-budget backstop.
func CompileWatchSpec(s Settings) string {
	enabled := make(map[string]struct{}, len(s.WatchPresets))
	for _, k := range s.WatchPresets {
		enabled[k] = struct{}{}
	}

	var b strings.Builder
	for _, k := range PresetKeys() {
		if _, on := enabled[k]; !on {
			continue
		}
		b.WriteString("- ")
		b.WriteString(WatchPresets[k])
		b.WriteByte('\n')
	}
	if spec := strings.TrimSpace(s.WatchSpec); spec != "" {
		b.WriteString("- ")
		b.WriteString(spec)
		b.WriteByte('\n')
	}

	out := strings.TrimRight(b.String(), "\n")
	if len(out) > maxCompiledWatchSpecLen {
		// Back up to a rune boundary so truncating never splits a multi-byte
		// character (the free-form spec may contain non-ASCII). This branch is
		// unreachable given the 4 KB API cap on watch_spec + the ≈1 KB preset
		// catalog, but it is a backstop — a backstop that emits invalid UTF-8
		// when it fires is worse than none, and stays correct if the caps change.
		cut := maxCompiledWatchSpecLen
		for cut > 0 && !utf8.RuneStart(out[cut]) {
			cut--
		}
		out = out[:cut] + watchSpecTruncMarker
	}
	return out
}

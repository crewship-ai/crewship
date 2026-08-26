package database

import "testing"

// TestEffectiveCrewDevcontainerConfig covers the chokepoint every reader of
// crews.devcontainer_config must go through. Each case names the real failure
// it prevents — see crew_devcontainer_default.go for the end-to-end
// reproduction (exit 127, "claude: No such file or directory") this guards
// against.
func TestEffectiveCrewDevcontainerConfig(t *testing.T) {
	cases := []struct {
		name string
		// stored/ok mirror what a `sql.NullString` scan of
		// crews.devcontainer_config produces.
		stored string
		ok     bool
		want   string
	}{
		{
			// This is the actual shape of the bug: crew_templates.go,
			// services/onboarding.go, recipes.go and internal_status.go all
			// omit devcontainer_config on INSERT, so the column scans as
			// SQL NULL (ok=false). Before this chokepoint, every reader
			// tested `!Valid || String == ""` and treated that as "nothing
			// to provision" (crew_provisioning_jobs.go: ErrCrewNoDevcontainer)
			// or "nothing to launch with" (bare debian:bookworm-slim, no
			// agent CLI, no uid 1001) — exit 127 on first agent run.
			name:   "NULL column defaults instead of leaving the crew unprovisionable",
			stored: "",
			ok:     false,
			want:   DefaultCrewDevcontainerConfig,
		},
		{
			// `crewship crew config --clear` (and PATCH with
			// devcontainer_config: "") writes an empty string rather than
			// NULL. A reader that only checked `!Valid` (and not also
			// String == "") would let this slip through as if it were a
			// real, explicit config — an empty string is not valid
			// devcontainer.json and would either crash the JSON parse or
			// silently produce a config with no image, no features, and no
			// agent CLI.
			name:   "empty string defaults the same as NULL",
			stored: "",
			ok:     true,
			want:   DefaultCrewDevcontainerConfig,
		},
		{
			// A config column that is present but whitespace-only (seen in
			// practice from a form field that trims to nothing, or a
			// hand-edited SQL UPDATE) must not be treated as "an explicit
			// config was set" — devcontainer.ParseBytes on "   " fails to
			// parse, which upstream readers (crewNeedsProvision,
			// crewIsPrivileged) already had to special-case as "unparseable
			// → false". Collapsing it to the default here means every
			// downstream reader sees one consistent, always-parseable value
			// instead of reimplementing that special case.
			name:   "whitespace-only defaults rather than reaching a reader as unparseable JSON",
			stored: "   \n\t  ",
			ok:     true,
			want:   DefaultCrewDevcontainerConfig,
		},
		{
			// The whole point of "effective, not default": a crew that HAS
			// its own config must keep it verbatim. A chokepoint that
			// defaulted everything (or mutated a real config) would silently
			// discard an operator's chosen base image / features / pinned
			// tool versions — the inverse failure, an operator-configured
			// crew quietly reverting to the shared default.
			name:   "a crew with its own config keeps it, not the default",
			stored: `{"image":"mcr.microsoft.com/devcontainers/python:3.12","features":{"ghcr.io/devcontainers/features/docker-in-docker:2":{}}}`,
			ok:     true,
			want:   `{"image":"mcr.microsoft.com/devcontainers/python:3.12","features":{"ghcr.io/devcontainers/features/docker-in-docker:2":{}}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveCrewDevcontainerConfig(tc.stored, tc.ok); got != tc.want {
				t.Errorf("EffectiveCrewDevcontainerConfig(%q, %v) = %q, want %q", tc.stored, tc.ok, got, tc.want)
			}
		})
	}
}

// TestCrewDevcontainerIsDefaulted covers the companion predicate callers use
// when they need to know WHETHER a crew is running on the default rather than
// WHAT it is running — e.g. ProvisionStatus surfacing
// "devcontainer_config_defaulted" to an operator so a crew running on a
// default it never chose is visible instead of looking identical to one it
// explicitly configured.
func TestCrewDevcontainerIsDefaulted(t *testing.T) {
	cases := []struct {
		name   string
		stored string
		ok     bool
		want   bool
	}{
		{
			// Same NULL case as above: must report "defaulted" so an
			// operator-facing surface (ProvisionStatus, the CLI doctor
			// check) can say so instead of implying nothing is configured.
			name:   "NULL column is defaulted",
			stored: "",
			ok:     false,
			want:   true,
		},
		{
			name:   "empty string is defaulted",
			stored: "",
			ok:     true,
			want:   true,
		},
		{
			name:   "whitespace-only is defaulted",
			stored: "  ",
			ok:     true,
			want:   true,
		},
		{
			// A real, explicit config must NOT be reported as defaulted —
			// the false-positive direction is what would make an operator
			// distrust their own settings.
			name:   "an explicit config is not defaulted",
			stored: `{"image":"debian:bookworm-slim"}`,
			ok:     true,
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CrewDevcontainerIsDefaulted(tc.stored, tc.ok); got != tc.want {
				t.Errorf("CrewDevcontainerIsDefaulted(%q, %v) = %v, want %v", tc.stored, tc.ok, got, tc.want)
			}
		})
	}
}

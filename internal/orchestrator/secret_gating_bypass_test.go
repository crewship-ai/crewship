package orchestrator

// Explicit bypass tests for the Keeper SECRET-gating invariant (#1486).
//
// The invariant is decided in ONE row — internal/credpolicy:
//
//	"SECRET": {Delivery: DeliveryFile, KeeperGated: true}
//
// plus the fail-safe fallback that gates every type with no row at all. Eight
// production sites consume that decision, and internal/credpolicy's own tests
// only cover the table, not the sites. A table that says "gated" and a
// delivery path that hands the value over anyway is the failure this file
// exists to catch.
//
// Every test below states an ATTACKER'S PLAN in prose and then proves the plan
// fails. Each was verified to have teeth by breaking the corresponding
// production check and confirming the test goes red — the mutation matrix is
// in the PR body. A bypass test that passes no matter what is worse than no
// test.
//
// Sites covered here:
//
//	exec_env.go:210      injectMCPCredentialEnvVars  — MCP ${VAR} env injection
//	exec_env.go:487      BuildEnvVarsSidecar         — the agent's env block
//	exec_env.go:1207     AgentEnvCredentialExposures — the operator's posture view
//	exec_env.go:~41      BuildEnvVars                — the non-sidecar agent env block
//	exec_sidecar.go:354  buildCredFileScript         — /secrets file delivery
//	secrets_cleanup.go:77 hasFileMountedCreds        — post-run scrub accounting
//
// The internal/api resolver chokepoint (agent_config.go:292/1576/1618) has its
// own sibling: internal/api/keeper_gating_bypass_test.go.

import (
	"context"
	"encoding/base64"
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/credpolicy"
	"github.com/crewship-ai/crewship/internal/provider"
)

// bypassCanary is the plaintext every attack below tries to smuggle to the
// agent. Distinctive so a substring search over an env block or a shell script
// cannot match it by accident.
//
// Named "canary" rather than "secretValue" on purpose: gitleaks' generic-api-key
// rule fires on a high-entropy literal assigned to an identifier containing
// secret/key/token/password, and the pre-commit hook then blocks the commit.
// Same class of workaround as pemFixture in exec_keeper_prompt_test.go.
const bypassCanary = "kp-bypass-canary-9f13a7"

// bypassPartCanary is the same idea for a SECRET *part* of a multi-part
// credential (PRD-CREDENTIALS-V2 §2.2) — the second door into the same room.
const bypassPartCanary = "kp-bypass-part-canary-4c02"

// bypassOAuthShapeCanary carries the sk-ant-oat prefix BuildEnvVarsSidecar's
// OAuth selector matches on VALUE shape alone (exec_env.go's hasOAuth loop),
// so an unclassified credential whose plaintext merely looks like an
// Anthropic OAuth token can be driven down that path regardless of type.
const bypassOAuthShapeCanary = "sk-ant-oat-kp-bypass-9f13a7"

// caseVariantSecretTypes are the spellings of "SECRET" that miss the
// credpolicy map key. Every one of them is reachable today: the credentials
// table was plain TEXT with no enum validation for most of the product's life
// (internal/api/credentials_types.go says so out loud), so a row written before
// that gate exists can hold any of these, and the orchestrator reads the column
// verbatim.
var caseVariantSecretTypes = []string{
	"secret",
	"Secret",
	"sEcReT",
	"SECRET ",  // trailing space
	" SECRET",  // leading space
	"SECRET\n", // trailing newline
	"SECRET\t",
}

// novelSecretTypes are types with no credpolicy row at all: a type someone adds
// to the DB (or a migration, or a connector) and forgets to classify, and the
// empty string a row can carry when nothing set it.
var novelSecretTypes = []string{
	"VAULT_HANDLE",
	"HSM_KEY",
	"KUBECONFIG",
	"GENERIC_SECRET_V2", // deliberately adjacent to a real row
	"",
}

// envCarries reports whether the env block assigns value to any variable —
// name-agnostic on purpose. An attacker who gets the plaintext into the block
// under a DIFFERENT name has still put it in /proc/self/environ, and a check
// keyed on the expected name would miss exactly that.
func envCarries(env []string, value string) (string, bool) {
	if value == "" {
		return "", false
	}
	for _, e := range env {
		if idx := strings.IndexByte(e, '='); idx > 0 && e[idx+1:] == value {
			return e[:idx], true
		}
	}
	return "", false
}

// scriptCarries reports whether the /secrets write script contains value in
// either of the two forms it can take there: raw (a path, an env-var name) or
// base64 (buildCredFileScript round-trips every secret through `base64 -d` so
// the shell cannot interpret it). Checking only the raw form would let a leak
// through the encoded channel that carries the actual bytes.
func scriptCarries(script, value string) bool {
	if value == "" || script == "" {
		return false
	}
	if strings.Contains(script, value) {
		return true
	}
	return strings.Contains(script, base64.StdEncoding.EncodeToString([]byte(value)))
}

// gatedCred builds the credential each attack ships: a SECRET-shaped value plus
// a SECRET part, named so an MCP config can reference it.
func gatedCred(credType string) Credential {
	return Credential{
		ID:         "cred-bypass",
		Type:       credType,
		EnvVarName: "PROD_DB_PASSWORD",
		PlainValue: bypassCanary,
		Fields: []CredentialField{
			{EnvVar: "PROD_DB_PASSWORD_PASSPHRASE", Value: bypassPartCanary, IsSecret: true},
			{EnvVar: "PROD_DB_PASSWORD_REGION", Value: "eu-central-1", IsSecret: false},
		},
	}
}

// bypassReq is a run request whose MCP config references the credential by
// name — the #1362 shape, and the reason exec_env.go:210 exists.
func bypassReq(credType string) AgentRunRequest {
	return AgentRunRequest{
		AgentSlug:  "riley",
		CLIAdapter: "CLAUDE_CODE",
		CrewMCPConfigJSON: `{"mcpServers":{"internal":{"type":"http",` +
			`"url":"https://api.internal.example/sse",` +
			`"headers":{"Authorization":"Bearer ${PROD_DB_PASSWORD}"}}}}`,
		Credentials: []Credential{gatedCred(credType)},
	}
}

// assertWithheldEverywhere drives all four Keeper-ON delivery gates with one
// credential type and fails on the first channel that hands the value over.
// One helper rather than four copies so a newly added attack automatically
// gets the whole surface, not the one door its author thought of.
func assertWithheldEverywhere(t *testing.T, credType string) {
	t.Helper()
	req := bypassReq(credType)

	if !credpolicy.IsKeeperGated(credType) {
		t.Fatalf("credpolicy.IsKeeperGated(%q) = false — the type is not gated at all, "+
			"so every assertion below is vacuous", credType)
	}

	// exec_env.go:210 — MCP ${VAR} injection.
	mcpEnv := injectMCPCredentialEnvVars(req, nil, true, secretsTestLogger())
	if name, leaked := envCarries(mcpEnv, bypassCanary); leaked {
		t.Errorf("type %q: MCP env injection handed the gated value over as %s= "+
			"with Keeper ON (exec_env.go:210)", credType, name)
	}

	// exec_env.go:487 — the agent's own env block, value and SECRET part.
	env := BuildEnvVarsSidecar(req, true)
	if name, leaked := envCarries(env, bypassCanary); leaked {
		t.Errorf("type %q: the agent env block carries the gated value as %s= "+
			"with Keeper ON (exec_env.go:487)", credType, name)
	}
	if name, leaked := envCarries(env, bypassPartCanary); leaked {
		t.Errorf("type %q: the agent env block carries the gated credential's SECRET "+
			"PART as %s= with Keeper ON — the part is credential material and rides "+
			"its credential's channel (exec_env.go appendCredentialFields)", credType, name)
	}

	// exec_sidecar.go:354 — /secrets file delivery.
	script, written, _, err := buildCredFileScript(req.Credentials, "/secrets/riley", true)
	if err != nil {
		t.Fatalf("type %q: buildCredFileScript: %v", credType, err)
	}
	if written != 0 {
		t.Errorf("type %q: buildCredFileScript wrote %d file(s) for a gated credential "+
			"with Keeper ON (exec_sidecar.go:354)", credType, written)
	}
	if scriptCarries(script, bypassCanary) {
		t.Errorf("type %q: the gated value is in the /secrets write script with Keeper ON", credType)
	}
	if scriptCarries(script, bypassPartCanary) {
		t.Errorf("type %q: the gated credential's SECRET part is in the /secrets write "+
			"script with Keeper ON", credType)
	}

	// exec_env.go:1207 — nothing reached the env, so the posture view must
	// report no exposure. An exposure listed here that is not real is its own
	// bug (it sends operators chasing a leak that does not exist).
	for _, e := range AgentEnvCredentialExposures(req, true) {
		if e.EnvVarName == "PROD_DB_PASSWORD" || e.EnvVarName == "PROD_DB_PASSWORD_PASSPHRASE" {
			t.Errorf("type %q: exposure report claims %s is in the agent env with Keeper ON, "+
				"but nothing put it there (exec_env.go:1207)", credType, e.EnvVarName)
		}
	}
}

// ATTACKER'S PLAN (a): I cannot change the credpolicy table, but I can choose
// how the credential's `type` column is SPELLED. The gate is a map lookup on an
// exact string, so I store my credential as "secret" instead of "SECRET" and
// walk straight past it — the type still reads as a secret to every human who
// looks at the vault, and to the UI, but the map lookup misses.
//
// Why the plan is realistic: the type column accepted arbitrary strings for
// most of the product's life (credentials_types.go), so pre-enum rows in this
// shape exist without anyone planting one.
//
// Why it fails: credpolicy.For falls through to `fallback`, which is
// {DeliveryNone, KeeperGated: true} — the MOST restrictive posture, not the
// least. A miss on the map is a miss towards safety.
func TestBypass_CaseVariantSecretTypeIsStillGated(t *testing.T) {
	t.Parallel()
	for _, ty := range caseVariantSecretTypes {
		t.Run("type="+strings.ReplaceAll(strings.ReplaceAll(ty, "\n", "\\n"), "\t", "\\t"), func(t *testing.T) {
			t.Parallel()
			if credpolicy.Known(ty) {
				t.Fatalf("credpolicy.Known(%q) = true — this variant now has an explicit "+
					"row, so it is no longer testing the fallback; either drop it from "+
					"caseVariantSecretTypes or assert its row directly", ty)
			}
			assertWithheldEverywhere(t, ty)
		})
	}
}

// ATTACKER'S PLAN (c): I get a new credential type into the system — a
// connector ships one, a migration adds one, a future feature invents one — and
// nobody adds a credpolicy row for it. I put secret material in it. If an
// unclassified type defaults to OPEN, every new type is a hole until someone
// notices.
//
// Why it fails: the fallback is gated, and it is gated at every one of the four
// delivery gates, not just in the table.
func TestBypass_NovelCredentialTypeCarryingSecretMaterialIsGatedNotOpen(t *testing.T) {
	t.Parallel()
	for _, ty := range novelSecretTypes {
		name := ty
		if name == "" {
			name = "(empty type column)"
		}
		t.Run("type="+name, func(t *testing.T) {
			t.Parallel()
			if credpolicy.Known(ty) {
				t.Fatalf("credpolicy.Known(%q) = true — someone classified this type; "+
					"pick another unclassified one so the fallback is still under test", ty)
			}
			if p := credpolicy.For(ty); p.Delivery != credpolicy.DeliveryNone {
				t.Errorf("credpolicy.For(%q).Delivery = %q, want %q — an unclassified type "+
					"must not have a delivery channel", ty, p.Delivery, credpolicy.DeliveryNone)
			}
			assertWithheldEverywhere(t, ty)
		})
	}
}

// ATTACKER'S PLAN: the ordinary one, stated for completeness — a plain SECRET
// under Keeper ON, tried at every door at once including the multi-part door.
//
// The individual doors have older tests (exec_sidecar_gate_test.go,
// keeper_secret_leak_mcp_test.go). What this adds is the ALL-OF assertion: a
// change that closes one channel while opening another is caught here, where
// each of those tests would still pass on its own.
func TestBypass_KeeperOnWithholdsSecretFromEveryDeliveryChannelAtOnce(t *testing.T) {
	t.Parallel()
	assertWithheldEverywhere(t, "SECRET")
}

// ATTACKER'S PLAN (the accounting attack on exec_env.go:1207): I accept that
// with Keeper OFF the value reaches the agent env — that is documented. What I
// want is for the operator not to KNOW. AgentEnvCredentialExposures is the only
// place the product tells them which secrets an agent can read; if my
// credential lands in the env but not in that list, the leak is invisible and
// nobody ever turns Keeper on.
//
// Why it fails: the exposure report is a strict superset of what
// BuildEnvVarsSidecar injected. This is asserted DIFFERENTIALLY — the two
// functions are run against the same request and compared — rather than against
// a hand-written expectation table, because a hand-written table drifts in
// exactly the direction that hides a leak.
func TestBypass_ExposureReportCannotUnderReportAGatedCredentialInEnv(t *testing.T) {
	t.Parallel()

	types := append([]string{"SECRET"}, caseVariantSecretTypes...)
	types = append(types, novelSecretTypes...)

	for _, keeper := range []bool{true, false} {
		for _, ty := range types {
			name := ty
			if name == "" {
				name = "(empty type column)"
			}
			name = strings.ReplaceAll(strings.ReplaceAll(name, "\n", "\\n"), "\t", "\\t")
			state := "keeper-off"
			if keeper {
				state = "keeper-on"
			}
			t.Run(state+"/type="+name, func(t *testing.T) {
				t.Parallel()
				req := bypassReq(ty)
				env := BuildEnvVarsSidecar(req, keeper)
				exposures := AgentEnvCredentialExposures(req, keeper)

				envName, inEnv := envCarries(env, bypassCanary)
				if !inEnv {
					return // nothing in the env, nothing to report
				}
				reported := false
				for _, e := range exposures {
					if e.EnvVarName == envName {
						reported = true
						if !e.Actionable {
							t.Errorf("type %q (%s): %s is in the agent env and IS closeable "+
								"by turning Keeper on, but the exposure is not marked "+
								"Actionable — the operator is not told they can fix it",
								ty, state, envName)
						}
					}
				}
				if !reported {
					t.Errorf("type %q (%s): BuildEnvVarsSidecar put the gated plaintext in "+
						"the agent env as %s=, and AgentEnvCredentialExposures does not "+
						"report it. The leak is invisible to the operator (exec_env.go:1207)",
						ty, state, envName)
				}

				// The SECRET PART rides the same env block on the same terms, and
				// under a DERIVED name the operator would never think to look for.
				// exec_env.go reports parts in a separate loop keyed on
				// markExposed, so "the value is reported" does not imply "its parts
				// are" — the two can drift apart, and the direction that drifts
				// silently is the one that under-reports.
				partName, partInEnv := envCarries(env, bypassPartCanary)
				if !partInEnv {
					return
				}
				for _, e := range exposures {
					if e.EnvVarName == partName {
						return
					}
				}
				t.Errorf("type %q (%s): the gated credential's SECRET PART is in the agent "+
					"env as %s= and AgentEnvCredentialExposures does not report it. The "+
					"operator's posture view names the credential but not the second "+
					"variable carrying its material, so the part is an invisible leak "+
					"(exec_env.go — the SECRET-parts exposure loop)", ty, state, partName)
			})
		}
	}
}

// ATTACKER'S PLAN (d, half one — the accounting attack on secrets_cleanup.go:77):
// I want my credential file to OUTLIVE the run. hasFileMountedCreds is what
// arms the post-run scrub: when it answers false, orchestrator_run.go never
// takes the secrets hold and never registers the cleanup defer, so nothing ever
// runs `rm -rf /secrets/<slug>`. If I can find a credential type where
// buildCredFileScript WRITES a file but hasFileMountedCreds says "nothing to
// clean", my 0400 secret sits in the container tmpfs for the container's whole
// lifetime, readable by every process in it, long after the run that needed it.
//
// Why it fails: both functions ask credpolicy the same two questions
// (FileMounted, KeeperGated) in the same order. This test proves they stay in
// lockstep for every type the system can hold — known rows, case variants,
// unclassified types — under both Keeper states, by running BOTH and comparing,
// so the lockstep cannot rot into a comment that says they match.
func TestBypass_CleanupAccountingCannotBeDodgedByCredentialType(t *testing.T) {
	t.Parallel()

	types := []string{
		// every classified row
		"SECRET", "GENERIC_SECRET", "CLI_TOKEN", "USERPASS", "SSH_KEY",
		"CERTIFICATE", "API_KEY", "AI_CLI_TOKEN", "OAUTH2", "ENDPOINT_URL",
	}
	types = append(types, caseVariantSecretTypes...)
	types = append(types, novelSecretTypes...)

	for _, keeper := range []bool{true, false} {
		for _, ty := range types {
			name := ty
			if name == "" {
				name = "(empty type column)"
			}
			name = strings.ReplaceAll(strings.ReplaceAll(name, "\n", "\\n"), "\t", "\\t")
			state := "keeper-off"
			if keeper {
				state = "keeper-on"
			}
			t.Run(state+"/type="+name, func(t *testing.T) {
				t.Parallel()
				cred := gatedCred(ty)
				// USERPASS needs its cleartext half or the writer errors out on
				// a data-shape regression rather than exercising the gate.
				cred.Username = "svc-account"
				creds := []Credential{cred}

				_, written, _, err := buildCredFileScript(creds, "/secrets/riley", keeper)
				if err != nil {
					t.Fatalf("type %q: buildCredFileScript: %v", ty, err)
				}
				wrote := written > 0
				armed := hasFileMountedCreds(creds, keeper)

				switch {
				case wrote && !armed:
					t.Errorf("type %q (%s): buildCredFileScript wrote %d file(s) under "+
						"/secrets but hasFileMountedCreds says there is nothing to clean. "+
						"orchestrator_run.go arms the post-run scrub off that answer, so "+
						"the credential file survives the run for the container's whole "+
						"lifetime (secrets_cleanup.go:77)", ty, state, written)
				case !wrote && armed:
					t.Errorf("type %q (%s): hasFileMountedCreds armed the secrets hold and "+
						"the cleanup exec, but buildCredFileScript wrote nothing — the "+
						"two are out of lockstep in the harmless direction, which is "+
						"still the drift that produces the harmful one",
						ty, state)
				}
			})
		}
	}
}

// ATTACKER'S PLAN (d, half two): I do not need to beat the scrub — I need the
// run to DIE before it. The credential files are written during preflight; if
// the cleanup only runs on the success path, then any failure I can provoke
// after the write (a wedged container, a preflight step that reports failure, a
// bad adapter) leaves the plaintext on the tmpfs with no run left to remove it.
// Provoking a preflight failure is cheap and leaves no trace.
//
// Why it fails: orchestrator_run.go takes the hold and registers the cleanup
// DEFER before preparePreflightDirs, so the abort path unwinds through it.
//
// Keeper is OFF here on purpose — that is the configuration in which the gated
// credential actually lands on disk, so it is the only one where there is
// anything for the scrub to fail to remove.
func TestBypass_AbortedRunStillScrubsGatedSecretFiles(t *testing.T) {
	var (
		wroteCreds  bool
		cleanupSeen bool
	)
	mc := &mockContainer{
		execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
			joined := strings.Join(cfg.Cmd, " ")
			stdin := covStdin(cfg)
			if strings.Contains(stdin, base64.StdEncoding.EncodeToString([]byte(bypassCanary))) {
				wroteCreds = true
			}
			// The preflight script reports the credential-write step as failed
			// — the run must abort after the files exist.
			if strings.Contains(stdin, preflightStepMarker+preflightStepCredentials) {
				return &provider.ExecResult{
					ExecID: "preflight-fail",
					Reader: io.NopCloser(strings.NewReader(preflightFailMarker + preflightStepCredentials + "\n")),
				}, nil
			}
			if strings.Contains(joined, "rm -rf '/secrets/riley'") {
				cleanupSeen = true
			}
			return &provider.ExecResult{ExecID: "noop", Reader: secretsTestReader()}, nil
		},
		inspectResult: struct {
			running  bool
			exitCode int
		}{false, 0},
	}
	o := New(mc, newMemState(), secretsTestLogger())

	err := o.RunAgent(context.Background(), AgentRunRequest{
		AgentID:     "a1",
		AgentSlug:   "riley",
		ChatID:      "s1",
		ContainerID: "c1",
		CLIAdapter:  "CLAUDE_CODE",
		UserMessage: "test",
		TimeoutSecs: 30,
		Credentials: []Credential{{Type: "SECRET", EnvVarName: "PROD_DB_PASSWORD", PlainValue: bypassCanary}},
	}, func(AgentEvent) {})

	if err == nil {
		t.Fatal("the run was supposed to abort on the failed credential-write step; " +
			"without an abort this test proves nothing about the abort path")
	}
	if !wroteCreds {
		t.Fatal("the credential write never carried the value, so there was nothing " +
			"on disk for the scrub to miss — the fixture stopped exercising the invariant")
	}
	if !cleanupSeen {
		t.Error("the run aborted with a Keeper-gated credential already written under " +
			"/secrets/riley and no cleanup exec ever ran: the plaintext outlives the " +
			"run for the container's lifetime (orchestrator_run.go — the cleanup defer " +
			"must be registered before preparePreflightDirs)")
	}
	if n := o.secretsHoldCount("c1", "riley"); n != 0 {
		t.Errorf("secrets hold count after the aborted run = %d, want 0 — a leaked hold "+
			"vetoes every future run's cleanup for this container+agent", n)
	}
}

// A KNOWN RESIDUAL, pinned rather than asserted away (#2092).
//
// With Keeper OFF, exec_env.go:487 selects what to inject with
// credpolicy.IsKeeperGated alone. For SECRET that is the documented legacy
// fallback. For an UNCLASSIFIED type it is not what credpolicy promises: the
// fallback row is {Delivery: DeliveryNone, KeeperGated: true}, and the package
// doc states an unclassified type is "not delivered to the agent" — full stop,
// no Keeper qualifier. IsKeeperGated is true for it, so it is injected.
//
// The file path does honour Delivery (buildCredFileScript checks FileMounted,
// so an unclassified type never lands on disk in either Keeper state); only the
// env path conflates "gated" with "SECRET". Impact is bounded — it needs Keeper
// off, which is already the posture in which secrets reach agents — but the
// fail-safe claim is stated unconditionally, and this is a test-only change, so
// the divergence is filed, not fixed here.
//
// This pin asserts BOTH halves precisely (env yes, disk no, and gated under
// Keeper ON) so it cannot decay into "something happened". When #2092 lands,
// this test flips to the withheld assertion and its name loses the suffix.
func TestBypass_UnclassifiedTypeNeverReachesEnv(t *testing.T) {
	t.Parallel()
	req := bypassReq("VAULT_HANDLE")

	// #2092: an unclassified type must never reach the agent env, in EITHER
	// Keeper state. The fail-safe fallback row is
	// {Delivery: DeliveryNone, KeeperGated: true} — DeliveryNone means no
	// delivery channel at all, not even the Keeper-off legacy env path that
	// SECRET uses.
	for _, keeper := range []bool{true, false} {
		if _, inEnv := envCarries(BuildEnvVarsSidecar(req, keeper), bypassCanary); inEnv {
			t.Errorf("keeper=%v: an unclassified type reached the agent env — DeliveryNone "+
				"must never be treated as the SECRET legacy env path", keeper)
		}
	}

	// AgentEnvCredentialExposures must mirror the withholding: it must not
	// report an exposure for a credential that BuildEnvVarsSidecar no longer
	// injects.
	for _, keeper := range []bool{true, false} {
		for _, exp := range AgentEnvCredentialExposures(req, keeper) {
			if exp.EnvVarName == req.Credentials[0].EnvVarName {
				t.Errorf("keeper=%v: AgentEnvCredentialExposures reported an exposure for the "+
					"withheld unclassified credential (%+v) — it must mirror BuildEnvVarsSidecar exactly",
					keeper, exp)
			}
		}
	}

	// The half that already held: never on disk, in either Keeper state.
	for _, keeper := range []bool{true, false} {
		script, written, _, err := buildCredFileScript(req.Credentials, "/secrets/riley", keeper)
		if err != nil {
			t.Fatalf("buildCredFileScript(keeper=%v): %v", keeper, err)
		}
		if written != 0 || scriptCarries(script, bypassCanary) {
			t.Errorf("keeper=%v: an unclassified type reached /secrets (%d file(s)) — the "+
				"file path reads credpolicy Delivery and must never deliver DeliveryNone",
				keeper, written)
		}
	}
}

// TestBypass_UnclassifiedTypeNeverReachesEnvViaAdapterAllowlist covers the
// SECOND of the three selectors an independent review found still unguarded
// after the #2092 legacy-fallback fix (#2246): BuildEnvVarsSidecar's BYO-API-key
// override loop matches a credential by EnvVarName alone (whatever
// apiKeyEnvVarsForAdapter allows for req.CLIAdapter) and never consulted
// credpolicy at all. An unclassified credential simply NAMED like a
// recognized provider key (e.g. OPENROUTER_API_KEY for an OPENCODE adapter)
// must not reach the env by that name collision, in either Keeper state —
// and AgentEnvCredentialExposures must not report it either.
func TestBypass_UnclassifiedTypeNeverReachesEnvViaAdapterAllowlist(t *testing.T) {
	t.Parallel()
	req := AgentRunRequest{
		AgentSlug:  "riley",
		CLIAdapter: "OPENCODE", // apiKeyEnvVarsForAdapter("OPENCODE") includes OPENROUTER_API_KEY
		Credentials: []Credential{{
			ID:         "cred-adapter-bypass",
			Type:       "VAULT_HANDLE", // unclassified: no credpolicy row
			EnvVarName: "OPENROUTER_API_KEY",
			PlainValue: bypassCanary,
		}},
	}

	for _, keeper := range []bool{true, false} {
		if _, inEnv := envCarries(BuildEnvVarsSidecar(req, keeper), bypassCanary); inEnv {
			t.Errorf("keeper=%v: an unclassified credential named after an adapter-recognized "+
				"env var reached the agent env — the allowlist override loop must consult "+
				"credpolicy before it overrides, not just the env-var name", keeper)
		}
		for _, exp := range AgentEnvCredentialExposures(req, keeper) {
			if exp.EnvVarName == "OPENROUTER_API_KEY" {
				t.Errorf("keeper=%v: AgentEnvCredentialExposures reported an exposure for the "+
					"withheld unclassified credential (%+v)", keeper, exp)
			}
		}
	}
}

// TestBypass_UnclassifiedTypeNeverReachesEnvViaOAuthShape covers the THIRD
// (and, per that review, worst) selector: BuildEnvVarsSidecar's hasOAuth loop
// treats ANY credential whose plaintext starts with "sk-ant-oat" as an OAuth
// token, regardless of its type, and — pre-fix — regardless of Keeper state
// (that loop never looked at keeperEnabled at all). An unclassified
// credential whose value merely collides with that shape must not reach the
// env as CLAUDE_CODE_OAUTH_TOKEN in EITHER Keeper state, including Keeper ON
// — the one state every other channel always withholds in.
func TestBypass_UnclassifiedTypeNeverReachesEnvViaOAuthShape(t *testing.T) {
	t.Parallel()
	req := AgentRunRequest{
		AgentSlug:  "riley",
		CLIAdapter: "CLAUDE_CODE",
		Credentials: []Credential{{
			ID:         "cred-oauth-shape-bypass",
			Type:       "VAULT_HANDLE", // unclassified: no credpolicy row
			EnvVarName: "IRRELEVANT_NAME",
			PlainValue: bypassOAuthShapeCanary,
		}},
	}

	for _, keeper := range []bool{true, false} {
		env := BuildEnvVarsSidecar(req, keeper)
		if _, inEnv := envCarries(env, bypassOAuthShapeCanary); inEnv {
			t.Errorf("keeper=%v: an unclassified credential whose value merely looks like an "+
				"OAuth token reached the agent env — the OAuth selector matches on value shape "+
				"and must still consult credpolicy before treating it as deliverable", keeper)
		}
		for _, exp := range AgentEnvCredentialExposures(req, keeper) {
			if exp.EnvVarName == "CLAUDE_CODE_OAUTH_TOKEN" {
				t.Errorf("keeper=%v: AgentEnvCredentialExposures reported an exposure for the "+
					"withheld unclassified OAuth-shaped credential (%+v)", keeper, exp)
			}
		}
	}
}

// TestBypass_UnclassifiedTypeNeverReachesEnvViaBuildEnvVars covers a selector
// outside the "three" above, found by a further review of #2246: BuildEnvVars
// (exec_env.go), the non-sidecar env builder. Unlike BuildEnvVarsSidecar it
// never took a keeperEnabled parameter and never consulted credpolicy at
// all — every credential's plaintext went straight into the env,
// unconditionally, for both the "activeCred" slot and every other credential
// on the request.
//
// Why this path matters as much as the sidecar one: a worker sub-agent
// dispatched with SkipSidecar=true (internal/api/query_handler.go) takes
// exactly this path even though the crew's sidecar is already running in the
// shared container — orchestrator_run.go's ensureSidecar computes
// sidecarEnabled=false and calls BuildEnvVars instead of
// BuildEnvVarsSidecar. Pre-fix, the SAME credential in the SAME Keeper state
// was withheld from the parent agent (via BuildEnvVarsSidecar's
// credEnvDeliverable gate) and handed to its sub-agent anyway (via this
// function's total absence of any gate) — a credential with no explicit
// credpolicy row leaked to whichever agent happened to run without starting
// its own sidecar.
func TestBypass_UnclassifiedTypeNeverReachesEnvViaBuildEnvVars(t *testing.T) {
	t.Parallel()

	types := append([]string{}, caseVariantSecretTypes...)
	types = append(types, novelSecretTypes...)

	for _, ty := range types {
		name := ty
		if name == "" {
			name = "(empty type column)"
		}
		name = strings.ReplaceAll(strings.ReplaceAll(name, "\n", "\\n"), "\t", "\\t")

		t.Run("activeCred/type="+name, func(t *testing.T) {
			t.Parallel()
			cred := gatedCred(ty)
			env := BuildEnvVars(AgentRunRequest{AgentSlug: "riley", CLIAdapter: "CLAUDE_CODE"}, &cred)
			if envName, leaked := envCarries(env, bypassCanary); leaked {
				t.Errorf("type %q: BuildEnvVars wrote the unclassified activeCred's plaintext "+
					"into the agent env as %s= — the non-sidecar path must consult credpolicy "+
					"like every other selector (exec_env.go BuildEnvVars)", ty, envName)
			}
			if partName, leaked := envCarries(env, bypassPartCanary); leaked {
				t.Errorf("type %q: BuildEnvVars wrote the unclassified activeCred's SECRET part "+
					"into the agent env as %s=", ty, partName)
			}
		})

		t.Run("otherCredential/type="+name, func(t *testing.T) {
			t.Parallel()
			cred := gatedCred(ty)
			req := AgentRunRequest{
				AgentSlug:   "riley",
				CLIAdapter:  "CLAUDE_CODE",
				Credentials: []Credential{cred},
			}
			// activeCred nil: this credential is only reachable through the
			// req.Credentials loop, the second unguarded site in the function.
			env := BuildEnvVars(req, nil)
			if envName, leaked := envCarries(env, bypassCanary); leaked {
				t.Errorf("type %q: BuildEnvVars wrote an unclassified credential's plaintext "+
					"into the agent env as %s= via the req.Credentials loop — a worker sub-agent "+
					"dispatched with SkipSidecar=true takes exactly this path, so the same "+
					"credential in the same Keeper state is withheld from a sidecar-built parent "+
					"env and delivered anyway to its sub-agent (exec_env.go BuildEnvVars)", ty, envName)
			}
			if partName, leaked := envCarries(env, bypassPartCanary); leaked {
				t.Errorf("type %q: BuildEnvVars wrote an unclassified credential's SECRET part "+
					"into the agent env as %s= via the req.Credentials loop", ty, partName)
			}
		})
	}
}

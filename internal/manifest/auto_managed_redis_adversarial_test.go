package manifest

// Adversarial coverage for the always-auth Redis feature
// (feat/redis-auto-credentials). Each test chases one attack angle
// against the command-arg credential channel and either proves a
// safety property or characterises a residual/by-design hole so a
// regression is loud. See the review report for severity ranking.

import (
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

// --- Angle 2: agent<->server value consistency ------------------------
//
// The redis SERVER receives the secret via --requirepass on Command;
// the AGENT receives it via the credential row (plannedAutoCredential
// .Value) surfaced under REDIS_PASSWORD. If those two ever diverge the
// agent cannot authenticate. Prove both come from the SAME generated
// value in one expansion — on the fresh path AND on the idempotent
// reuse path.
func TestRedis_ServerAndAgentValueAgree_FreshAndReuse(t *testing.T) {
	assertAgree := func(t *testing.T, spec *CrewSpec, prior string) {
		t.Helper()
		planned, err := expandAutoCredentialsInCrewSpec(spec, prior)
		if err != nil {
			t.Fatalf("expand: %v", err)
		}
		if len(planned) != 1 || planned[0].Name != "REDIS_PASSWORD" {
			t.Fatalf("want one REDIS_PASSWORD entry, got %+v", planned)
		}
		agentValue := planned[0].Value // what the credential row / agent env_ref resolves to
		cmd := spec.Services[0].Command
		if len(cmd) != 3 {
			t.Fatalf("redis Command not the 3-token requirepass argv: %+v", cmd)
		}
		serverValue := cmd[2] // the --requirepass argument the redis server boots with
		if agentValue != serverValue {
			t.Fatalf("agent value %q != server --requirepass value %q — agents could not authenticate",
				agentValue, serverValue)
		}
	}

	t.Run("fresh", func(t *testing.T) {
		spec := &CrewSpec{
			Services: []Service{{Name: "redis", Image: "redis:7-alpine"}},
			Agents:   []Agent{{Slug: "lead", AgentRole: "LEAD"}},
		}
		assertAgree(t, spec, "")
	})

	t.Run("reuse", func(t *testing.T) {
		prior := strings.Repeat("cd", 32) // 64 valid hex chars
		priorJSON := `[{"name":"redis","image":"redis:7-alpine","command":["redis-server","--requirepass","` + prior + `"]}]`
		spec := &CrewSpec{
			Services: []Service{{Name: "redis", Image: "redis:7-alpine"}},
			Agents:   []Agent{{Slug: "lead", AgentRole: "LEAD"}},
		}
		assertAgree(t, spec, priorJSON)
		// And it must actually be the reused value, not a fresh one.
		planned, _ := expandAutoCredentialsInCrewSpec(&CrewSpec{
			Services: []Service{{Name: "redis", Image: "redis:7-alpine"}},
			Agents:   []Agent{{Slug: "lead", AgentRole: "LEAD"}},
		}, priorJSON)
		if planned[0].Value != prior {
			t.Fatalf("reuse path did not reuse prior value: got %q want %q", planned[0].Value, prior)
		}
	})
}

// --- Angle 3: reuse-path trust boundary -------------------------------
//
// extractCommandArgValue reads the prior --requirepass value from
// services_json, which can carry operator/hand-edited drift. The only
// gate is: exact expected length + valid hex. That gate does NOT check
// entropy, so a hand-edited services_json carrying a low-entropy but
// hex+correct-length value (e.g. 64 zeros) is accepted and PERSISTS as
// the crew's redis password. This test documents that residual risk
// (CONFIRMED, but bounded by the services_json trust boundary — see
// report) and pins the parts of the gate that DO hold.
func TestRedis_ReusePath_WeakButWellFormedHexIsAccepted_ResidualRisk(t *testing.T) {
	weak := strings.Repeat("00", 32) // 64 hex chars, all-zero: valid hex, correct length, ~0 entropy
	priorJSON := `[{"name":"redis","image":"redis:7-alpine","command":["redis-server","--requirepass","` + weak + `"]}]`
	spec := &CrewSpec{Services: []Service{{Name: "redis", Image: "redis:7-alpine"}}}

	planned, err := expandAutoCredentialsInCrewSpec(spec, priorJSON)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	// RESIDUAL RISK: the length+hex gate accepts this weak value and
	// carries it forward as the live password. If a future hardening
	// adds an entropy floor this assertion flips — that's intended.
	if planned[0].Value != weak {
		t.Fatalf("expected the weak-but-wellformed value to be reused (documents the entropy gap); got %q", planned[0].Value)
	}
}

// The gate that DOES hold: a prior value that is the wrong length or
// not hex is rejected and a fresh strong value is generated. This is
// the safety property the reuse path guarantees.
func TestRedis_ReusePath_RejectsMalformedPrior(t *testing.T) {
	cases := map[string]string{
		"too short": strings.Repeat("ab", 16), // 32 hex chars, but default wants 64
		"non hex":   strings.Repeat("zz", 32), // 64 chars, not hex
		"empty arg": "",
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			priorJSON := `[{"name":"redis","image":"redis:7-alpine","command":["redis-server","--requirepass","` + bad + `"]}]`
			spec := &CrewSpec{Services: []Service{{Name: "redis", Image: "redis:7-alpine"}}}
			planned, err := expandAutoCredentialsInCrewSpec(spec, priorJSON)
			if err != nil {
				t.Fatalf("expand: %v", err)
			}
			got := planned[0].Value
			if got == bad {
				t.Fatalf("malformed prior %q was reused; must regenerate", bad)
			}
			if len(got) != 64 {
				t.Fatalf("fresh value not 64 hex chars: %q", got)
			}
			if _, e := hex.DecodeString(got); e != nil {
				t.Fatalf("fresh value not hex: %v", e)
			}
		})
	}
}

// --- Angle 5 (CRUX): operator-precedence bypass -----------------------
//
// The feature's headline guarantee is "a stock redis sidecar is ALWAYS
// password-protected." Precedence skips generation when svc.Command is
// non-empty. That means an operator (or a crafted manifest) can ship a
// redis with command:["redis-server"] — NO --requirepass — and expand
// leaves it untouched: no credential, no agent env_ref, and crucially
// a PASSWORDLESS redis reachable by every container on the crew bridge.
//
// This test CONFIRMS the bypass. It is by-design precedence (mirrors
// operator-pinned-env), so it is reported, not "fixed" — but it means
// the always-auth guarantee does NOT hold whenever an operator supplies
// their own Command. See report, Angle 5, for the blunt verdict.
func TestRedis_OperatorPasswordlessCommand_DefeatsAlwaysAuth(t *testing.T) {
	passwordless := []string{"redis-server"} // no --requirepass at all
	spec := &CrewSpec{
		Services: []Service{{Name: "redis", Image: "redis:7-alpine", Command: passwordless}},
		Agents:   []Agent{{Slug: "lead", AgentRole: "LEAD"}},
	}
	planned, err := expandAutoCredentialsInCrewSpec(spec, "")
	if err != nil {
		t.Fatalf("expand: %v", err)
	}

	// CONFIRMED HOLE: no credential is minted for the passwordless redis.
	if len(planned) != 0 {
		t.Fatalf("expected precedence to suppress generation (documents the bypass); got %+v", planned)
	}
	// The redis boots exactly as the operator wrote it — with NO auth.
	if !reflect.DeepEqual(spec.Services[0].Command, passwordless) {
		t.Fatalf("operator command mutated: %+v", spec.Services[0].Command)
	}
	if strings.Contains(strings.Join(spec.Services[0].Command, " "), "--requirepass") {
		t.Fatalf("unexpected requirepass injected; test premise broken")
	}
	// No agent env_ref either, so nothing signals the missing auth.
	if containsString(spec.Agents[0].EnvRefs, "REDIS_PASSWORD") {
		t.Fatalf("agent got REDIS_PASSWORD env_ref for a passwordless redis: %+v", spec.Agents[0].EnvRefs)
	}
}

// A partial operator command that names a DIFFERENT flag (still no
// requirepass) is likewise passed through untouched — the precedence
// gate is "any non-empty Command", it does not inspect the args.
func TestRedis_OperatorNonAuthCommand_StillBypasses(t *testing.T) {
	cmd := []string{"redis-server", "--maxmemory", "256mb"}
	spec := &CrewSpec{
		Services: []Service{{Name: "redis", Image: "redis:7-alpine", Command: cmd}},
	}
	planned, err := expandAutoCredentialsInCrewSpec(spec, "")
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(planned) != 0 {
		t.Fatalf("non-auth operator command must still bypass; got %+v", planned)
	}
	if !reflect.DeepEqual(spec.Services[0].Command, cmd) {
		t.Fatalf("command mutated: %+v", spec.Services[0].Command)
	}
}

// --- Angle 6: render / extract template edge cases --------------------

func TestRenderCommandTemplate_PlaceholderCounts(t *testing.T) {
	const v = "SECRETVAL"
	cases := []struct {
		name string
		tmpl []string
		want []string
	}{
		{"zero placeholders", []string{"redis-server", "--appendonly", "yes"}, []string{"redis-server", "--appendonly", "yes"}},
		{"one placeholder", []string{"redis-server", "--requirepass", "{{value}}"}, []string{"redis-server", "--requirepass", v}},
		{"two placeholders", []string{"{{value}}", "--requirepass", "{{value}}"}, []string{v, "--requirepass", v}},
		{"placeholder as substring is NOT replaced", []string{"prefix-{{value}}-suffix"}, []string{"prefix-{{value}}-suffix"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Copy the template so we can prove it was not mutated in place —
			// the catalog entry is a package global shared across every crew.
			orig := append([]string(nil), tc.tmpl...)
			got := renderCommandTemplate(tc.tmpl, v)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("render = %+v, want %+v", got, tc.want)
			}
			if !reflect.DeepEqual(tc.tmpl, orig) {
				t.Errorf("template mutated in place: %+v (was %+v)", tc.tmpl, orig)
			}
			// Output must be a fresh backing array (no aliasing into template).
			if len(got) > 0 && len(tc.tmpl) > 0 && &got[0] == &tc.tmpl[0] {
				t.Errorf("render aliases the template backing array — one crew's secret could leak into another")
			}
		})
	}
}

func TestExtractCommandArgValue_EdgeCases(t *testing.T) {
	tmpl := []string{"redis-server", "--requirepass", "{{value}}"}
	dbl := []string{"{{value}}", "--requirepass", "{{value}}"}
	v := strings.Repeat("ab", 32)

	cases := []struct {
		name    string
		tmpl    []string
		prior   []string
		wantVal string
		wantOK  bool
	}{
		{"round trip", tmpl, []string{"redis-server", "--requirepass", v}, v, true},
		{"length mismatch", tmpl, []string{"redis-server", "--requirepass"}, "", false},
		{"fixed token drift", tmpl, []string{"redis-server", "--PWNED", v}, "", false},
		{"empty prior value", tmpl, []string{"redis-server", "--requirepass", ""}, "", false},
		{"empty template", []string{}, []string{}, "", false},
		{"dup placeholders agree", dbl, []string{v, "--requirepass", v}, v, true},
		{"dup placeholders disagree", dbl, []string{v, "--requirepass", "deadbeef"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotVal, gotOK := extractCommandArgValue(tc.tmpl, tc.prior)
			if gotOK != tc.wantOK || gotVal != tc.wantVal {
				t.Errorf("extract = (%q,%v), want (%q,%v)", gotVal, gotOK, tc.wantVal, tc.wantOK)
			}
		})
	}
}

// An AutoCredential that declares BOTH InjectAsCommand and InjectAsEnv:
// the command channel must win and the env literal must NOT be written
// to the sidecar (command-injected creds never travel as env). InjectAsEnv
// is silently ignored for the sidecar in that case — this pins that
// behaviour so a future change that starts double-writing is caught.
func TestExpand_InjectAsCommandAndEnvSimultaneously_CommandWins(t *testing.T) {
	spec := &CrewSpec{
		Services: []Service{
			{
				Name:  "cache",
				Image: "ghcr.io/example/custom-redis:v1", // unknown image, explicit auto-cred
				AutoCredentials: []AutoCredential{
					{
						Name:            "REDIS_PASSWORD",
						InjectAsEnv:     "REDIS_PWD_ENV",
						InjectAsCommand: []string{"redis-server", "--requirepass", autoCredentialValuePlaceholder},
					},
				},
			},
		},
		Agents: []Agent{{Slug: "lead", AgentRole: "LEAD"}},
	}
	planned, err := expandAutoCredentialsInCrewSpec(spec, "")
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(planned) != 1 {
		t.Fatalf("want 1 planned, got %+v", planned)
	}
	svc := spec.Services[0]
	if len(svc.Command) != 3 || svc.Command[2] != planned[0].Value {
		t.Fatalf("command channel did not receive value: %+v", svc.Command)
	}
	// The env literal must NOT be present under EITHER key.
	if _, has := svc.Env["REDIS_PWD_ENV"]; has {
		t.Errorf("command-injected cred leaked into sidecar env under InjectAsEnv: %+v", svc.Env)
	}
	if _, has := svc.Env["REDIS_PASSWORD"]; has {
		t.Errorf("command-injected cred leaked into sidecar env under Name: %+v", svc.Env)
	}
	// Agent still gets it under Name so it can authenticate.
	if !containsString(spec.Agents[0].EnvRefs, "REDIS_PASSWORD") {
		t.Errorf("agent missing REDIS_PASSWORD env_ref: %+v", spec.Agents[0].EnvRefs)
	}
}

// --- Angle 7: invariant regressions -----------------------------------
//
// The redis catalog entry must satisfy the byte floor and the expected
// shape, and default-inject to agents. minAutoCredentialBytes is the
// entropy floor for generated values; the redis entry uses the default
// length (no explicit Length), which must clear the floor.
func TestRedisCatalog_MeetsByteFloorAndShape(t *testing.T) {
	defs, ok := lookupSidecarDefaults("redis:7-alpine")
	if !ok {
		t.Fatal("redis catalog entry missing")
	}
	if len(defs.AutoCredentials) != 1 {
		t.Fatalf("want exactly one redis auto-cred, got %+v", defs.AutoCredentials)
	}
	ac := defs.AutoCredentials[0]
	if ac.Name != "REDIS_PASSWORD" {
		t.Errorf("name = %q, want REDIS_PASSWORD", ac.Name)
	}
	if ac.EffectiveLength() < minAutoCredentialBytes {
		t.Errorf("EffectiveLength %d below the %d-byte floor", ac.EffectiveLength(), minAutoCredentialBytes)
	}
	if !ac.EffectiveInjectToAgents() {
		t.Error("redis auto-cred must inject to agents (else they cannot authenticate)")
	}
	want := []string{"redis-server", "--requirepass", autoCredentialValuePlaceholder}
	if !reflect.DeepEqual(ac.InjectAsCommand, want) {
		t.Errorf("InjectAsCommand = %+v, want %+v", ac.InjectAsCommand, want)
	}
	// The generated value must clear the floor in hex-char terms too.
	if got := len(strings.Repeat("x", ac.EffectiveLength()*2)); got < 2*minAutoCredentialBytes {
		t.Errorf("hex value width %d below floor", got)
	}
}

// Normalisation invariant: a fully-qualified redis reference (registry
// + namespace + digest) still resolves the catalog entry, so a crew
// pulling redis from a mirror is still always-auth (subject to Angle 5).
func TestRedisCatalog_ResolvesNormalisedReferences(t *testing.T) {
	refs := []string{
		"redis:7-alpine",
		"redis:latest",
		"docker.io/library/redis:7",
		"redis@sha256:" + strings.Repeat("a", 64),
	}
	for _, ref := range refs {
		t.Run(ref, func(t *testing.T) {
			s := &Service{Name: "redis", Image: ref}
			got := s.ResolveAutoCredentials()
			if len(got) != 1 || got[0].Name != "REDIS_PASSWORD" {
				t.Fatalf("ref %q did not resolve redis auto-cred: %+v", ref, got)
			}
		})
	}
}

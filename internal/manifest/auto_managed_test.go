package manifest

import (
	"context"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

func TestExpandAutoCredentials_PostgresSugarFires(t *testing.T) {
	spec := CrewSpec{
		Services: []Service{
			{Name: "postgres", Image: "postgres:16-alpine"},
		},
		Agents: []Agent{
			{Slug: "lead", Name: "Lead", AgentRole: "LEAD"},
			{Slug: "worker", Name: "Worker", AgentRole: "AGENT"},
		},
	}
	planned, err := expandAutoCredentialsInCrewSpec(&spec, "")
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(planned) != 1 || planned[0].Name != "POSTGRES_PASSWORD" {
		t.Fatalf("expected one POSTGRES_PASSWORD entry, got %+v", planned)
	}
	if planned[0].ProvisionedForService != "postgres" {
		t.Errorf("ProvisionedForService = %q, want postgres", planned[0].ProvisionedForService)
	}
	// Value should be a 64-char hex string (32 bytes).
	if got := planned[0].Value; len(got) != 64 {
		t.Errorf("Value length = %d, want 64 hex chars", len(got))
	}
	if _, err := hex.DecodeString(planned[0].Value); err != nil {
		t.Errorf("Value not hex-decodable: %v", err)
	}
	// Sidecar Env must include POSTGRES_PASSWORD=<value> AND sugar
	// POSTGRES_USER=postgres default.
	pg := spec.Services[0]
	if pg.Env["POSTGRES_PASSWORD"] != planned[0].Value {
		t.Errorf("sidecar env missing literal value: %+v", pg.Env)
	}
	if pg.Env["POSTGRES_USER"] != "postgres" {
		t.Errorf("sugar env POSTGRES_USER missing: %+v", pg.Env)
	}
	// Every agent gets POSTGRES_PASSWORD in env_refs.
	for _, ag := range spec.Agents {
		if !containsString(ag.EnvRefs, "POSTGRES_PASSWORD") {
			t.Errorf("agent %q missing POSTGRES_PASSWORD env_ref: %+v", ag.Slug, ag.EnvRefs)
		}
	}
}

// TestExpandAutoCredentials_RedisRequirepassCommand is the core of the
// always-auth-Redis feature. A crew declares a stock redis sidecar with
// no auth of its own; expansion must:
//   - generate a strong-random hex secret (>= the byte floor),
//   - splice it into the redis server's Command as `--requirepass <value>`
//     (the official image ignores env passwords, so command is the channel),
//   - NOT bake the value into the sidecar env,
//   - append REDIS_PASSWORD to every agent's env_refs (so the agent can
//     authenticate), and
//   - queue a plannedAutoCredential for the credential row.
func TestExpandAutoCredentials_RedisRequirepassCommand(t *testing.T) {
	spec := CrewSpec{
		Services: []Service{
			{Name: "redis", Image: "redis:7-alpine"},
		},
		Agents: []Agent{
			{Slug: "lead", Name: "Lead", AgentRole: "LEAD"},
			{Slug: "worker", Name: "Worker", AgentRole: "AGENT"},
		},
	}
	planned, err := expandAutoCredentialsInCrewSpec(&spec, "")
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(planned) != 1 || planned[0].Name != "REDIS_PASSWORD" {
		t.Fatalf("want one REDIS_PASSWORD planned entry, got %+v", planned)
	}
	if planned[0].ProvisionedForService != "redis" {
		t.Errorf("ProvisionedForService = %q, want redis", planned[0].ProvisionedForService)
	}

	val := planned[0].Value
	// Strong-random hex, not empty/static, at least the byte floor long.
	if len(val) < 2*minAutoCredentialBytes {
		t.Errorf("value length = %d, want >= %d hex chars", len(val), 2*minAutoCredentialBytes)
	}
	if _, err := hex.DecodeString(val); err != nil {
		t.Errorf("value not hex-decodable (%q): %v", val, err)
	}

	redis := spec.Services[0]
	// The generated value must reach the server via --requirepass on Command.
	wantCmd := []string{"redis-server", "--requirepass", val}
	if !reflect.DeepEqual(redis.Command, wantCmd) {
		t.Errorf("redis Command = %+v, want %+v", redis.Command, wantCmd)
	}
	// It must NOT be baked as a sidecar env literal — command is the channel.
	if _, has := redis.Env["REDIS_PASSWORD"]; has {
		t.Errorf("redis sidecar env should not carry REDIS_PASSWORD; command is the channel: %+v", redis.Env)
	}
	// Every agent gets REDIS_PASSWORD via env_refs so it can authenticate.
	for _, ag := range spec.Agents {
		if !containsString(ag.EnvRefs, "REDIS_PASSWORD") {
			t.Errorf("agent %q missing REDIS_PASSWORD env_ref: %+v", ag.Slug, ag.EnvRefs)
		}
	}
}

// TestExpandAutoCredentials_RedisOperatorCommandWins proves that a redis
// service which ALREADY declares its own auth (an operator-supplied
// Command) is left untouched: no generation, no credential row, no agent
// env_ref append. Mirrors the operator-pinned-env precedence for the
// command-injection channel.
func TestExpandAutoCredentials_RedisOperatorCommandWins(t *testing.T) {
	opCmd := []string{"redis-server", "--requirepass", "operator-chose-this"}
	spec := CrewSpec{
		Services: []Service{
			{Name: "redis", Image: "redis:7-alpine", Command: opCmd},
		},
		Agents: []Agent{
			{Slug: "lead", Name: "Lead", AgentRole: "LEAD"},
		},
	}
	planned, err := expandAutoCredentialsInCrewSpec(&spec, "")
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(planned) != 0 {
		t.Errorf("operator-supplied command must suppress the auto-cred; got %+v", planned)
	}
	if !reflect.DeepEqual(spec.Services[0].Command, opCmd) {
		t.Errorf("operator command mutated: %+v", spec.Services[0].Command)
	}
	if containsString(spec.Agents[0].EnvRefs, "REDIS_PASSWORD") {
		t.Errorf("no agent env_ref expected when operator manages auth: %+v", spec.Agents[0].EnvRefs)
	}
}

// TestExpandAutoCredentials_RedisReusesPriorCommandValue is the idempotence
// guarantee for command-injected creds: a re-apply reuses the prior value
// carried in services_json's Command instead of rotating the password.
func TestExpandAutoCredentials_RedisReusesPriorCommandValue(t *testing.T) {
	spec := CrewSpec{
		Services: []Service{{Name: "redis", Image: "redis:7-alpine"}},
	}
	prior := strings.Repeat("ab", 32) // 64 hex chars, valid
	priorJSON := `[{"name":"redis","image":"redis:7-alpine","command":["redis-server","--requirepass","` + prior + `"]}]`

	planned, err := expandAutoCredentialsInCrewSpec(&spec, priorJSON)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(planned) != 1 {
		t.Fatalf("expected 1 planned, got %d", len(planned))
	}
	if planned[0].Value != prior {
		t.Errorf("expected reuse of prior command value, got fresh: %q vs %q", planned[0].Value, prior)
	}
	want := []string{"redis-server", "--requirepass", prior}
	if !reflect.DeepEqual(spec.Services[0].Command, want) {
		t.Errorf("redis Command = %+v, want reused %+v", spec.Services[0].Command, want)
	}
}

func TestExpandAutoCredentials_InjectToAgentsFalseSkipsAgentEnvRef(t *testing.T) {
	falseVal := false
	spec := CrewSpec{
		Services: []Service{
			{
				Name:  "internal-cache",
				Image: "ghcr.io/example/internal:v1",
				AutoCredentials: []AutoCredential{
					{Name: "INTERNAL_TOKEN", InjectToAgents: &falseVal},
				},
			},
		},
		Agents: []Agent{
			{Slug: "lead", Name: "Lead", AgentRole: "LEAD"},
		},
	}
	planned, err := expandAutoCredentialsInCrewSpec(&spec, "")
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(planned) != 1 {
		t.Fatalf("expected 1 planned, got %d", len(planned))
	}
	// Sidecar should have the env literal...
	if spec.Services[0].Env["INTERNAL_TOKEN"] == "" {
		t.Errorf("sidecar env missing INTERNAL_TOKEN")
	}
	// ...but the agent must NOT.
	if containsString(spec.Agents[0].EnvRefs, "INTERNAL_TOKEN") {
		t.Errorf("agent got INTERNAL_TOKEN env_ref despite inject_to_agents=false")
	}
}

func TestExpandAutoCredentials_InjectAsEnvOverride(t *testing.T) {
	spec := CrewSpec{
		Services: []Service{
			{
				Name:  "postgres",
				Image: "postgres:16",
				AutoCredentials: []AutoCredential{
					{Name: "POSTGRES_PASSWORD", InjectAsEnv: "PG_PWD"},
				},
			},
		},
	}
	planned, err := expandAutoCredentialsInCrewSpec(&spec, "")
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(planned) != 1 || planned[0].Name != "POSTGRES_PASSWORD" {
		t.Fatalf("unexpected planned: %+v", planned)
	}
	// Sidecar env should have PG_PWD (the override), NOT POSTGRES_PASSWORD.
	if _, has := spec.Services[0].Env["PG_PWD"]; !has {
		t.Errorf("sidecar env missing PG_PWD: %+v", spec.Services[0].Env)
	}
	if _, has := spec.Services[0].Env["POSTGRES_PASSWORD"]; has {
		t.Errorf("sidecar env unexpectedly has POSTGRES_PASSWORD too: %+v", spec.Services[0].Env)
	}
}

func TestExpandAutoCredentials_DuplicateNameAcrossServicesError(t *testing.T) {
	spec := CrewSpec{
		Services: []Service{
			{Name: "primary", Image: "postgres:16"},
			{Name: "replica", Image: "postgres:16"},
		},
	}
	_, err := expandAutoCredentialsInCrewSpec(&spec, "")
	if err == nil {
		t.Fatal("expected error for duplicate POSTGRES_PASSWORD across services, got nil")
	}
	if !strings.Contains(err.Error(), "POSTGRES_PASSWORD") {
		t.Errorf("error should name the colliding credential: %v", err)
	}
}

func TestExpandAutoCredentials_NoOpOnEmptyOrUnknown(t *testing.T) {
	cases := []*CrewSpec{
		nil,
		{},
		{Services: []Service{{Name: "x", Image: "nginx:latest"}}},
		{Services: []Service{{Name: "x", Image: "busybox:latest"}}}, // unknown, no auto-creds
	}
	for i, spec := range cases {
		t.Run("case_"+string(rune('0'+i)), func(t *testing.T) {
			planned, err := expandAutoCredentialsInCrewSpec(spec, "")
			if err != nil {
				t.Errorf("expected nil err for no-op case, got %v", err)
			}
			if len(planned) != 0 {
				t.Errorf("expected empty planned, got %+v", planned)
			}
		})
	}
}

func TestDeepCopyCrewSpec_MutationsDontLeak(t *testing.T) {
	orig := CrewSpec{
		Services: []Service{
			{Name: "postgres", Image: "postgres:16", Env: map[string]string{"POSTGRES_DB": "app"}},
		},
		Agents: []Agent{
			{Slug: "lead", EnvRefs: []string{"ANTHROPIC_API_KEY"}},
		},
	}
	clone := deepCopyCrewSpec(&orig)
	// Mutate clone aggressively.
	clone.Services[0].Env["POSTGRES_PASSWORD"] = "mutated"
	clone.Agents[0].EnvRefs = append(clone.Agents[0].EnvRefs, "POSTGRES_PASSWORD")

	if _, leaked := orig.Services[0].Env["POSTGRES_PASSWORD"]; leaked {
		t.Errorf("mutation of clone leaked into orig.Services[0].Env: %+v", orig.Services[0].Env)
	}
	if containsString(orig.Agents[0].EnvRefs, "POSTGRES_PASSWORD") {
		t.Errorf("mutation of clone leaked into orig.Agents[0].EnvRefs: %+v", orig.Agents[0].EnvRefs)
	}
}

func TestGenerateAutoCredentialValue_LengthAndHex(t *testing.T) {
	cases := []struct{ in, wantLen int }{
		{0, 64},  // default 32 bytes → 64 hex
		{16, 32}, // 16 bytes → 32 hex
		{48, 96}, // 48 bytes → 96 hex
	}
	for _, tc := range cases {
		got, err := generateAutoCredentialValue(tc.in)
		if err != nil {
			t.Fatalf("generate(%d): %v", tc.in, err)
		}
		if len(got) != tc.wantLen {
			t.Errorf("generate(%d) length = %d, want %d", tc.in, len(got), tc.wantLen)
		}
		if _, err := hex.DecodeString(got); err != nil {
			t.Errorf("generate(%d) not hex: %v", tc.in, err)
		}
	}
}

// End-to-end with the fake API client: apply a manifest with a
// postgres sidecar and assert that the credential row gets POSTed
// with the AUTO_MANAGED provider + system attribution + service tag.
func TestApply_PostgresAutoManagedCredential_EndToEnd(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: ECrew, slug: ecrew}
spec:
  services:
    - { name: pg, image: postgres:16-alpine }
  agents:
    - {slug: lead, name: Lead, agent_role: LEAD, prompt: x}
`)
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	fake := newFakeAPI(t)
	client := NewClient(fake)
	if _, err := Apply(context.Background(), client, b, Options{Mode: ApplyUpsert, Yes: true}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Find the POST /api/v1/credentials call that created the auto-cred.
	var credBody map[string]any
	for _, call := range fake.Calls {
		if call.Method != "POST" || call.Path != "/api/v1/credentials" {
			continue
		}
		if name, _ := call.Body["name"].(string); name == "POSTGRES_PASSWORD" {
			credBody = call.Body
			break
		}
	}
	if credBody == nil {
		t.Fatal("no POST /api/v1/credentials for POSTGRES_PASSWORD was issued")
	}
	if got, _ := credBody["provider"].(string); got != "AUTO_MANAGED" {
		t.Errorf("provider = %q, want AUTO_MANAGED", got)
	}
	if got, _ := credBody["created_by_actor_type"].(string); got != "system" {
		t.Errorf("created_by_actor_type = %q, want system", got)
	}
	if got, _ := credBody["provisioned_for_service"].(string); got != "ecrew/pg" {
		t.Errorf("provisioned_for_service = %q, want ecrew/pg", got)
	}
	if got, _ := credBody["value"].(string); len(got) != 64 {
		t.Errorf("value len = %d, want 64 hex chars", len(got))
	}

	// And the POST /crews body must carry POSTGRES_PASSWORD as a
	// literal env on the sidecar (so the docker provider sees it).
	var crewServicesJSON string
	for _, call := range fake.Calls {
		if call.Method == "POST" && call.Path == "/api/v1/crews" {
			crewServicesJSON, _ = call.Body["services_json"].(string)
		}
	}
	if crewServicesJSON == "" {
		t.Fatal("crew body missing services_json")
	}
	if !strings.Contains(crewServicesJSON, "POSTGRES_PASSWORD") {
		t.Errorf("services_json missing POSTGRES_PASSWORD literal env: %s", crewServicesJSON)
	}
	if !strings.Contains(crewServicesJSON, "POSTGRES_USER") {
		t.Errorf("services_json missing POSTGRES_USER sugar default: %s", crewServicesJSON)
	}
}

// End-to-end for Redis: apply a manifest with a stock redis sidecar and
// assert the credential row is POSTed as a GENERIC_SECRET / AUTO_MANAGED
// row (not plaintext-only), and that the crew's services_json carries the
// generated secret via --requirepass on the redis Command.
func TestApply_RedisAutoManagedCredential_EndToEnd(t *testing.T) {
	body := []byte(`
apiVersion: crewship/v1
kind: Crew
metadata: {name: RCrew, slug: rcrew}
spec:
  services:
    - { name: cache, image: redis:7-alpine }
  agents:
    - {slug: lead, name: Lead, agent_role: LEAD, prompt: x}
`)
	b, err := Load(body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	fake := newFakeAPI(t)
	client := NewClient(fake)
	if _, err := Apply(context.Background(), client, b, Options{Mode: ApplyUpsert, Yes: true}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var credBody map[string]any
	for _, call := range fake.Calls {
		if call.Method != "POST" || call.Path != "/api/v1/credentials" {
			continue
		}
		if name, _ := call.Body["name"].(string); name == "REDIS_PASSWORD" {
			credBody = call.Body
			break
		}
	}
	if credBody == nil {
		t.Fatal("no POST /api/v1/credentials for REDIS_PASSWORD was issued")
	}
	if got, _ := credBody["type"].(string); got != "GENERIC_SECRET" {
		t.Errorf("type = %q, want GENERIC_SECRET", got)
	}
	if got, _ := credBody["provider"].(string); got != "AUTO_MANAGED" {
		t.Errorf("provider = %q, want AUTO_MANAGED", got)
	}
	if got, _ := credBody["provisioned_for_service"].(string); got != "rcrew/cache" {
		t.Errorf("provisioned_for_service = %q, want rcrew/cache", got)
	}
	genValue, _ := credBody["value"].(string)
	if len(genValue) != 64 {
		t.Errorf("value len = %d, want 64 hex chars", len(genValue))
	}

	var crewServicesJSON string
	for _, call := range fake.Calls {
		if call.Method == "POST" && call.Path == "/api/v1/crews" {
			crewServicesJSON, _ = call.Body["services_json"].(string)
		}
	}
	if crewServicesJSON == "" {
		t.Fatal("crew body missing services_json")
	}
	if !strings.Contains(crewServicesJSON, "--requirepass") {
		t.Errorf("services_json missing --requirepass command arg: %s", crewServicesJSON)
	}
	// The exact generated secret must be the requirepass argument (the
	// server boots redis from services_json, so the two MUST agree).
	if genValue != "" && !strings.Contains(crewServicesJSON, genValue) {
		t.Errorf("services_json requirepass value does not match the credential value %q: %s", genValue, crewServicesJSON)
	}
}

// Idempotent re-apply: when a prior services_json already carries
// a generated value, expand reuses it instead of producing a fresh
// one. This is load-bearing — sidecars boot from services_json,
// credential rows live in the DB, the two MUST agree.
func TestExpandAutoCredentials_ReusesPriorValueOnReapply(t *testing.T) {
	spec := CrewSpec{
		Services: []Service{{Name: "postgres", Image: "postgres:16-alpine"}},
	}
	priorValue := strings.Repeat("ab", 32)
	priorJSON := `[{"name":"postgres","image":"postgres:16-alpine","env":{"POSTGRES_USER":"postgres","POSTGRES_PASSWORD":"` + priorValue + `"}}]`

	planned, err := expandAutoCredentialsInCrewSpec(&spec, priorJSON)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(planned) != 1 {
		t.Fatalf("expected 1 planned, got %d", len(planned))
	}
	if planned[0].Value != priorValue {
		t.Errorf("expected reuse of prior value, got fresh: %q vs %q", planned[0].Value, priorValue)
	}
	if spec.Services[0].Env["POSTGRES_PASSWORD"] != priorValue {
		t.Errorf("sidecar env did not get the reused value: %q", spec.Services[0].Env["POSTGRES_PASSWORD"])
	}
}

// Drift recovery: a prior value with the wrong length (operator
// changed AutoCredential.Length in the manifest) is regenerated.
func TestExpandAutoCredentials_RegeneratesOnLengthMismatch(t *testing.T) {
	spec := CrewSpec{
		Services: []Service{
			{
				Name:  "postgres",
				Image: "postgres:16-alpine",
				AutoCredentials: []AutoCredential{
					{Name: "POSTGRES_PASSWORD", Length: 64},
				},
			},
		},
	}
	priorShort := strings.Repeat("ab", 32)
	priorJSON := `[{"name":"postgres","image":"postgres:16-alpine","env":{"POSTGRES_PASSWORD":"` + priorShort + `"}}]`

	planned, err := expandAutoCredentialsInCrewSpec(&spec, priorJSON)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if planned[0].Value == priorShort {
		t.Errorf("expected regeneration on length drift, but old value was reused")
	}
	if len(planned[0].Value) != 128 {
		t.Errorf("expected new 64-byte (128 hex) value, got %d chars", len(planned[0].Value))
	}
}

// Defends against a hand-edited services_json (operator drift).
func TestExpandAutoCredentials_RegeneratesOnNonHexPrior(t *testing.T) {
	spec := CrewSpec{
		Services: []Service{{Name: "postgres", Image: "postgres:16"}},
	}
	priorJSON := `[{"name":"postgres","image":"postgres:16","env":{"POSTGRES_PASSWORD":"not-hex-but-64-chars-long-padding-padding-padding-padding-padd"}}]`
	planned, err := expandAutoCredentialsInCrewSpec(&spec, priorJSON)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if _, decodeErr := hex.DecodeString(planned[0].Value); decodeErr != nil {
		t.Errorf("regenerated value is not hex: %v", decodeErr)
	}
}

// Robustness: malformed prior JSON is treated as "no prior state"
// (fresh generation) rather than crashing the plan.
func TestExpandAutoCredentials_TolerantOfMalformedPrior(t *testing.T) {
	spec := CrewSpec{
		Services: []Service{{Name: "postgres", Image: "postgres:16"}},
	}
	planned, err := expandAutoCredentialsInCrewSpec(&spec, `{not valid json at all`)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(planned[0].Value) != 64 {
		t.Errorf("expected fresh 64-char value, got %d", len(planned[0].Value))
	}
}

// TestExpandAutoCredentials_OperatorPinnedEnvWins is the regression
// for the C-test bug: when an operator writes
// `services[0].env.POSTGRES_PASSWORD: my-literal`, the sugar layer
// MUST NOT overwrite that literal with an auto-generated value, mint
// a credential row, or append the env_ref to agents. The schema
// docstring on ResolveEnv explicitly says "operator values always
// win on key collision," but pre-fix expandAutoCredentialsInCrewSpec
// did the inverse: it ran ResolveEnv (operator overlay correct),
// then unconditionally re-wrote svc.Env[inject_as_env]=<generated>
// at the end of each auto-cred loop iteration — clobbering the very
// override it had just preserved.
func TestExpandAutoCredentials_OperatorPinnedEnvWins(t *testing.T) {
	const literal = "operator-chose-this-value-explicitly"
	spec := CrewSpec{
		Services: []Service{
			{
				Name:  "postgres",
				Image: "postgres:16-alpine",
				Env:   map[string]string{"POSTGRES_PASSWORD": literal},
			},
		},
		Agents: []Agent{
			{Slug: "lead", Name: "Lead", AgentRole: "LEAD"},
		},
	}
	planned, err := expandAutoCredentialsInCrewSpec(&spec, "")
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(planned) != 0 {
		t.Errorf("operator-pinned key must not produce a planned credential row; got %+v", planned)
	}
	if got := spec.Services[0].Env["POSTGRES_PASSWORD"]; got != literal {
		t.Errorf("operator literal must survive expansion; got %q (len=%d), want %q", got, len(got), literal)
	}
	if containsString(spec.Agents[0].EnvRefs, "POSTGRES_PASSWORD") {
		t.Errorf("operator-pinned key must not auto-append to agent env_refs; got %+v", spec.Agents[0].EnvRefs)
	}
	// Sugar's *other* defaults (the ones the operator did NOT pin)
	// still apply — POSTGRES_USER must land via the catalog default,
	// otherwise we've over-corrected and broken the docstring's
	// "operator + catalog merge" contract for non-collision keys.
	if got := spec.Services[0].Env["POSTGRES_USER"]; got != "postgres" {
		t.Errorf("non-pinned catalog default must still apply; POSTGRES_USER=%q", got)
	}
}

// TestExpandAutoCredentials_OperatorPinnedEmptyStringRejected makes
// the precedence rule precise under the #1363 always-auth invariant:
// an empty-string operator value on a recognised datastore means the
// operator took ownership of the auth channel but supplied NO auth, so
// the apply is REJECTED (a half-edited manifest with
// `POSTGRES_PASSWORD: ""` must not silently boot an open datastore, and
// must not silently mint a value the operator did not ask for either).
// The operator fixes it by supplying a real password or opting out with
// allow_unauthenticated: true (covered in the authguard suite).
func TestExpandAutoCredentials_OperatorPinnedEmptyStringRejected(t *testing.T) {
	spec := CrewSpec{
		Services: []Service{
			{
				Name:  "postgres",
				Image: "postgres:16-alpine",
				Env:   map[string]string{"POSTGRES_PASSWORD": ""},
			},
		},
	}
	_, err := expandAutoCredentialsInCrewSpec(&spec, "")
	if err == nil {
		t.Fatalf("empty-string operator password on a catalog datastore must be rejected; got nil")
	}
	if !strings.Contains(err.Error(), "POSTGRES_PASSWORD") {
		t.Errorf("error %q should name the empty auth env", err.Error())
	}
}

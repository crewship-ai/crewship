package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/modelcatalog"
)

// A hand-built stand-in for the embedded snapshot, parsed through the real
// modelcatalog.Parse so these tests exercise the same normalization the
// production path gets. "zebra" sorts after "deepseek" — that is what pins the
// catalog-only tail being sorted rather than map-ordered.
const testProviderCatalogJSON = `{
  "anthropic": {"id":"anthropic","name":"Anthropic","models":{
    "claude-x":{"id":"claude-x","name":"Claude X","tool_call":true,
      "limit":{"context":200000,"output":64000},"cost":{"input":5,"output":25}}}},
  "openai": {"id":"openai","name":"OpenAI","models":{"gpt-x":{"id":"gpt-x"}}},
  "deepseek": {"id":"deepseek","name":"DeepSeek","models":{
    "ds-pro":{"id":"ds-pro","name":"DeepSeek Pro","cost":{"input":0.14,"output":0.28}},
    "ds-lite":{"id":"ds-lite"}}},
  "zebra": {"id":"zebra","name":"Zebra Gateway","models":{}}
}`

func testProviderCatalog(t *testing.T) modelcatalog.Catalog {
	t.Helper()
	cat, err := modelcatalog.Parse([]byte(testProviderCatalogJSON))
	if err != nil {
		t.Fatalf("parse fixture catalog: %v", err)
	}
	return cat
}

// envMap turns a map into a lookupEnvFunc. Injected rather than set with
// t.Setenv because t.Setenv forbids t.Parallel, and "is this variable set" is
// exactly the sort of thing several table cases want to describe at once.
func envMap(m map[string]string) lookupEnvFunc {
	return func(name string) (string, bool) {
		v, ok := m[name]
		return v, ok
	}
}

func TestBuildProviderRows_RegistryOrderAndKeyState(t *testing.T) {
	cat := testProviderCatalog(t)

	tests := []struct {
		name string
		env  map[string]string
		// want, keyed by provider id: (KeyRequired, KeySet, Endpoint)
		wantKeyRequired map[string]bool
		wantKeySet      map[string]bool
		wantEndpoint    map[string]string
	}{
		{
			name: "no environment at all",
			env:  map[string]string{},
			wantKeyRequired: map[string]bool{
				"anthropic": true, "openai": true, "ollama": false,
			},
			wantKeySet: map[string]bool{
				"anthropic": false, "openai": false, "ollama": false,
			},
			wantEndpoint: map[string]string{
				"ollama": "http://localhost:11434",
			},
		},
		{
			name: "one key set, one blank, endpoint overridden",
			env: map[string]string{
				"ANTHROPIC_API_KEY": "sk-ant-real",
				"OPENAI_API_KEY":    "   ",
				"KEEPER_OLLAMA_URL": "http://gpu-box:11434",
			},
			wantKeyRequired: map[string]bool{
				"anthropic": true, "openai": true, "ollama": false,
			},
			wantKeySet: map[string]bool{
				// A variable set to whitespace authenticates nothing; reporting
				// it as set sends the operator looking in the wrong place.
				"anthropic": true, "openai": false, "ollama": false,
			},
			wantEndpoint: map[string]string{
				"ollama": "http://gpu-box:11434",
			},
		},
		{
			name: "endpoint variable present but empty falls back to the default",
			env:  map[string]string{"KEEPER_OLLAMA_URL": ""},
			wantKeyRequired: map[string]bool{
				"anthropic": true, "openai": true, "ollama": false,
			},
			wantKeySet: map[string]bool{
				"anthropic": false, "openai": false, "ollama": false,
			},
			wantEndpoint: map[string]string{
				"ollama": "http://localhost:11434",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows := buildProviderRows(cat, false, envMap(tc.env))

			// Registry rows only, in declaration order.
			want := llm.RegisteredProviders()
			if len(rows) != len(want) {
				t.Fatalf("got %d rows, want %d (%v)", len(rows), len(want), want)
			}
			for i, id := range want {
				if rows[i].ID != id {
					t.Errorf("row %d = %q, want %q (declaration order is load-bearing)", i, rows[i].ID, id)
				}
				if !rows[i].Registered {
					t.Errorf("row %q: Registered = false, want true", id)
				}
			}

			byID := map[string]providerRow{}
			for _, r := range rows {
				byID[r.ID] = r
			}
			for id, want := range tc.wantKeyRequired {
				if got := byID[id].KeyRequired; got != want {
					t.Errorf("%s KeyRequired = %v, want %v", id, got, want)
				}
			}
			for id, want := range tc.wantKeySet {
				if got := byID[id].KeySet; got != want {
					t.Errorf("%s KeySet = %v, want %v", id, got, want)
				}
			}
			for id, want := range tc.wantEndpoint {
				if got := byID[id].Endpoint; got != want {
					t.Errorf("%s Endpoint = %q, want %q", id, got, want)
				}
			}
		})
	}
}

func TestBuildProviderRows_NeverEmitsAKeyValue(t *testing.T) {
	// The one thing this command must never do. A row carries the variable's
	// NAME and a boolean, never its contents.
	const secret = "sk-ant-do-not-print-me"
	rows := buildProviderRows(testProviderCatalog(t), true, envMap(map[string]string{
		"ANTHROPIC_API_KEY": secret,
		"OPENAI_API_KEY":    secret,
	}))
	blob, err := json.Marshal(providerListResult{Providers: rows})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), secret) {
		t.Fatalf("provider list JSON leaked the key value:\n%s", blob)
	}
	for _, r := range rows {
		for _, cell := range providerListRows([]providerRow{r})[0] {
			if strings.Contains(cell, secret) {
				t.Fatalf("provider list table leaked the key value in row %q: %q", r.ID, cell)
			}
		}
	}
}

func TestBuildProviderRows_All(t *testing.T) {
	cat := testProviderCatalog(t)
	rows := buildProviderRows(cat, true, envMap(nil))

	registered := llm.RegisteredProviders()
	if len(rows) != len(registered)+2 {
		// deepseek and zebra are catalog-only; anthropic and openai are both.
		t.Fatalf("got %d rows, want %d\nrows: %+v", len(rows), len(registered)+2, rows)
	}
	tail := rows[len(registered):]
	if tail[0].ID != "deepseek" || tail[1].ID != "zebra" {
		t.Errorf("catalog-only tail = %q, %q; want deepseek, zebra (sorted)", tail[0].ID, tail[1].ID)
	}
	for _, r := range tail {
		if r.Registered {
			t.Errorf("%s: Registered = true, want false — it has no codec in this build", r.ID)
		}
		if r.Codec != "" || r.Auth != "" || r.KeyEnv != "" {
			t.Errorf("%s: catalog-only row carries registry fields: %+v", r.ID, r)
		}
	}
	if tail[0].DisplayName != "DeepSeek" {
		t.Errorf("deepseek DisplayName = %q, want the catalog name", tail[0].DisplayName)
	}
	if tail[0].CatalogModels != 2 {
		t.Errorf("deepseek CatalogModels = %d, want 2", tail[0].CatalogModels)
	}
	if tail[1].CatalogModels != 0 {
		t.Errorf("zebra CatalogModels = %d, want 0", tail[1].CatalogModels)
	}
}

func TestBuildProviderRows_CatalogCounts(t *testing.T) {
	rows := buildProviderRows(testProviderCatalog(t), false, envMap(nil))
	want := map[string]int{"anthropic": 1, "openai": 1, "ollama": 0}
	for _, r := range rows {
		if got, ok := want[r.ID]; ok && r.CatalogModels != got {
			t.Errorf("%s CatalogModels = %d, want %d", r.ID, r.CatalogModels, got)
		}
	}
}

func TestRedactURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain", "http://localhost:11434", "http://localhost:11434"},
		{"hosted default", "https://api.anthropic.com/v1/messages", "https://api.anthropic.com/v1/messages"},
		{"user and password", "https://bob:hunter2@gpu-box:8000/v1", "https://redacted@gpu-box:8000/v1"},
		{"user only", "https://token@gpu-box:8000/v1", "https://redacted@gpu-box:8000/v1"},
		// No "@" at all is the fast path; an "@" that is not userinfo must
		// survive byte-identical.
		{"at in the path", "http://host/p@th", "http://host/p@th"},
		// Unparseable, and it still has an "@": the host survives so the typo
		// stays visible, but whatever preceded the "@" is replaced. url.Parse
		// fails here because of the space, not because of the userinfo — the
		// old expectation returned this verbatim, which printed the credential
		// in the one case the redaction exists for.
		{"unparseable", "http://a b@c", "http://redacted@c"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactURL(tc.in); got != tc.want {
				t.Errorf("redactURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRedactURL_NeverKeepsThePassword(t *testing.T) {
	got := redactURL("https://bob:hunter2@gpu-box:8000/v1")
	for _, secret := range []string{"hunter2", "bob"} {
		if strings.Contains(got, secret) {
			t.Errorf("redactURL kept %q: %q", secret, got)
		}
	}
}

func TestEnvIsSet(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		key  string
		want bool
	}{
		{"absent", map[string]string{}, "K", false},
		{"set", map[string]string{"K": "v"}, "K", true},
		{"empty", map[string]string{"K": ""}, "K", false},
		{"whitespace", map[string]string{"K": "  \t "}, "K", false},
		{"other key set", map[string]string{"OTHER": "v"}, "K", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := envIsSet(envMap(tc.env), tc.key); got != tc.want {
				t.Errorf("envIsSet(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

func TestResolveProviderEndpoint(t *testing.T) {
	tests := []struct {
		name string
		spec llm.ProviderSpec
		env  map[string]string
		want string
	}{
		{
			name: "no BaseEnv uses the default",
			spec: llm.ProviderSpec{BaseDefault: "https://api.example/v1"},
			env:  map[string]string{"ANY": "http://nope"},
			want: "https://api.example/v1",
		},
		{
			name: "BaseEnv set wins",
			spec: llm.ProviderSpec{BaseEnv: "URL", BaseDefault: "http://localhost:11434"},
			env:  map[string]string{"URL": "http://gpu-box:11434"},
			want: "http://gpu-box:11434",
		},
		{
			name: "BaseEnv blank falls back",
			spec: llm.ProviderSpec{BaseEnv: "URL", BaseDefault: "http://localhost:11434"},
			env:  map[string]string{"URL": "  "},
			want: "http://localhost:11434",
		},
		{
			name: "BaseEnv value is trimmed",
			spec: llm.ProviderSpec{BaseEnv: "URL"},
			env:  map[string]string{"URL": " http://gpu-box:11434\n"},
			want: "http://gpu-box:11434",
		},
		{
			name: "nothing at all",
			spec: llm.ProviderSpec{},
			env:  map[string]string{},
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveProviderEndpoint(tc.spec, envMap(tc.env)); got != tc.want {
				t.Errorf("resolveProviderEndpoint = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProviderKeyCell(t *testing.T) {
	tests := []struct {
		name string
		row  providerRow
		want string
	}{
		{"needs a key and has one", providerRow{Registered: true, KeyRequired: true, KeySet: true}, "set"},
		{"needs a key and has none", providerRow{Registered: true, KeyRequired: true}, "unset"},
		{"needs no key", providerRow{Registered: true}, "not needed"},
		{"catalog only", providerRow{}, dashIfEmpty("")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := providerKeyCell(tc.row); got != tc.want {
				t.Errorf("providerKeyCell = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProviderListRows_MatchesHeaderWidth(t *testing.T) {
	// The header list lives in providerListCmd's RunE; a row that does not
	// match it renders a table with a ragged final column.
	const wantCols = 8
	rows := providerListRows(buildProviderRows(testProviderCatalog(t), true, envMap(nil)))
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	for i, r := range rows {
		if len(r) != wantCols {
			t.Errorf("row %d has %d cells, want %d: %q", i, len(r), wantCols, r)
		}
		if r[0] == "" {
			t.Errorf("row %d has an empty column 0 — that is what --format quiet prints", i)
		}
	}
}

func TestCatalogIDFor(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"registry row with a catalog id", "anthropic", "anthropic"},
		{"uppercase from the API enum form", "OPENAI", "openai"},
		{"registry row the catalog has nothing for", "ollama", "ollama"},
		{"catalog-only provider", "deepseek", "deepseek"},
		{"padded", "  DeepSeek  ", "deepseek"},
		{"unknown", "nope", "nope"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := catalogIDFor(tc.in); got != tc.want {
				t.Errorf("catalogIDFor(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// --- acceptance: `provider list` must answer with no config and no server ---

// offlineEnv is a deliberately EMPTY environment plus the minimum a process
// needs. Not os.Environ(): this box exports CREWSHIP_SERVER, and inheriting it
// would let an "offline" test quietly pass by reaching a real server.
func offlineEnv(t *testing.T) []string {
	t.Helper()
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"NO_COLOR=1",
	}
}

func TestAcceptance_ProviderList_Offline(t *testing.T) {
	bin := buildCrewshipBinary(t)

	cmd := exec.Command(bin, "provider", "list")
	cmd.Env = offlineEnv(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	got := string(out)
	for _, want := range []string{"anthropic", "openai", "ollama", "ANTHROPIC_API_KEY", "anthropic-messages"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, got)
		}
	}
	// The Ollama row must not read as a missing credential.
	if !strings.Contains(got, "not needed") {
		t.Errorf("output does not mark the keyless provider:\n%s", got)
	}
}

func TestAcceptance_ProviderList_JSONOffline(t *testing.T) {
	bin := buildCrewshipBinary(t)

	cmd := exec.Command(bin, "provider", "list", "--all", "--format", "json")
	cmd.Env = offlineEnv(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}

	var res providerListResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal --format json output: %v\noutput: %s", err, out)
	}
	if len(res.Providers) < 4 {
		t.Fatalf("got %d providers, want the registry plus catalog-only ones: %+v", len(res.Providers), res.Providers)
	}

	byID := map[string]providerRow{}
	for _, p := range res.Providers {
		byID[p.ID] = p
	}
	anthropic, ok := byID["anthropic"]
	if !ok {
		t.Fatalf("no anthropic row: %+v", res.Providers)
	}
	if !anthropic.Registered || anthropic.Codec != "anthropic-messages" || anthropic.KeyEnv != "ANTHROPIC_API_KEY" {
		t.Errorf("anthropic row = %+v", anthropic)
	}
	if anthropic.KeySet {
		t.Errorf("anthropic KeySet = true in an environment with no ANTHROPIC_API_KEY")
	}
	if anthropic.CatalogModels == 0 {
		t.Errorf("anthropic CatalogModels = 0; the embedded snapshot carries anthropic models")
	}
	// A catalog-only provider proves --all reached the snapshot.
	if ds, ok := byID["deepseek"]; !ok || ds.Registered {
		t.Errorf("deepseek row = %+v, ok=%v; want present and Registered=false", ds, ok)
	}
}

func TestAcceptance_ProviderList_QuietPrintsIDs(t *testing.T) {
	bin := buildCrewshipBinary(t)

	cmd := exec.Command(bin, "provider", "list", "--format", "quiet")
	cmd.Env = offlineEnv(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	lines := strings.Fields(strings.TrimSpace(string(out)))
	want := llm.RegisteredProviders()
	if len(lines) != len(want) {
		t.Fatalf("quiet printed %d lines, want %d: %q", len(lines), len(want), lines)
	}
	for i, id := range want {
		if lines[i] != id {
			t.Errorf("quiet line %d = %q, want %q", i, lines[i], id)
		}
	}
}

func TestAcceptance_ProviderList_RejectsArgs(t *testing.T) {
	bin := buildCrewshipBinary(t)

	cmd := exec.Command(bin, "provider", "list", "stray")
	cmd.Env = offlineEnv(t)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit for a stray positional arg; output: %s", out)
	}
}

// The provider group must be reachable and self-describing — the smoke script
// walks `crewship commands --format json`, so a group that failed to register
// is invisible rather than a failure.
func TestAcceptance_ProviderCommandIsRegistered(t *testing.T) {
	bin := buildCrewshipBinary(t)

	cmd := exec.Command(bin, "commands", "--format", "quiet")
	cmd.Env = offlineEnv(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	paths := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		paths[strings.TrimSpace(line)] = true
	}
	for _, want := range []string{"provider", "provider list", "model", "model list"} {
		if !paths[want] {
			t.Errorf("command manifest is missing %q", want)
		}
	}
}

// Guards the build variant: the local commands must link into `-tags clionly`,
// which is where a stray server-side import would surface.
func TestProviderCommandBuildsCLIOnly(t *testing.T) {
	// SKIP-WAIVER: -short exists to drop exactly this kind of work — the test
	// shells out to `go build -tags clionly`, which is tens of seconds. There is
	// no tracking issue because there is nothing to come back and fix: the guard
	// is permanent by design, matching the #1546 precedent recorded in
	// scripts/skip-budget.txt. CI does not pass -short, so the build variant is
	// still covered on every run; the guard only spares a developer's inner loop.
	if testing.Short() {
		t.Skip("skipping build in -short mode")
	}
	out := filepath.Join(t.TempDir(), "crewship-clionly")
	cmd := exec.Command("go", "build", "-tags", "clionly", "-o", out, ".")
	cmd.Env = os.Environ()
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build -tags clionly: %v\n%s", err, combined)
	}
}

// url.Parse fails for reasons unrelated to userinfo — a raw space is enough —
// and the credential in such a string is no less real. Returning it unchanged
// printed the secret.
func TestRedactURL_UnparseableStillRedacts(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"space in host", "http://user:supersecret@a b/v1"},
		{"space in userinfo", "https://us er:supersecret@host/v1"},
		{"control character", "http://user:supersecret@ho\x7fst/v1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := redactURL(tc.raw)
			if strings.Contains(got, "supersecret") {
				t.Errorf("redactURL(%q) = %q — leaked the credential", tc.raw, got)
			}
			if !strings.Contains(got, "redacted") {
				t.Errorf("redactURL(%q) = %q — want the redaction marker so the reader knows something was removed", tc.raw, got)
			}
		})
	}
}

// The leak the first fix missed. url.Parse SUCCEEDS on a scheme-less endpoint
// with userinfo — it reads "user:" as the scheme and the rest as Opaque, so
// u.User is nil and a u.User-based check finds nothing to redact. That is the
// exact shape KEEPER_OLLAMA_URL holds when someone puts the credential in the
// variable, and it printed the password.
func TestRedactURL_SchemelessUserinfoIsRedacted(t *testing.T) {
	tests := []struct {
		name, raw, mustNotContain string
	}{
		{"scheme-less with password", "user:hunter2@gpu-box:11434", "hunter2"},
		{"scheme-less user only", "tokenvalue@gpu-box:11434", "tokenvalue"},
		{"scheme-less with path", "user:hunter2@gpu-box:11434/v1", "hunter2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := redactURL(tc.raw)
			if strings.Contains(got, tc.mustNotContain) {
				t.Errorf("redactURL(%q) = %q — leaked %q", tc.raw, got, tc.mustNotContain)
			}
			if !strings.Contains(got, "gpu-box:11434") {
				t.Errorf("redactURL(%q) = %q — lost the host, which is the diagnosis", tc.raw, got)
			}
		})
	}
}

// A credential in the query string is a credential. An api-version is not, and
// a reader needs it, so only credential-shaped names lose their value.
func TestRedactURL_QueryCredentialsAreRedacted(t *testing.T) {
	tests := []struct {
		name, raw, mustNotContain, mustContain string
	}{
		{"api-key", "https://host/v1?api-key=SECRET", "SECRET", "api-key=redacted"},
		{"sas token", "https://host/v1?sasToken=SECRET", "SECRET", "sasToken=redacted"},
		{"api-version survives", "https://host/v1?api-version=2026-02-01", "", "api-version=2026-02-01"},
		{"mixed", "https://host/v1?api-version=2026-02-01&api-key=SECRET", "SECRET", "api-version=2026-02-01"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := redactURL(tc.raw)
			if tc.mustNotContain != "" && strings.Contains(got, tc.mustNotContain) {
				t.Errorf("redactURL(%q) = %q — leaked %q", tc.raw, got, tc.mustNotContain)
			}
			if !strings.Contains(got, tc.mustContain) {
				t.Errorf("redactURL(%q) = %q, want it to contain %q", tc.raw, got, tc.mustContain)
			}
		})
	}
}

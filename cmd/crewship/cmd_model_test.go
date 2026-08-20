package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/modelcatalog"
)

// --- unit: fetchModels decodes the API payload ---

func TestFetchModels_Decodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("provider"); got != "anthropic" {
			t.Errorf("provider = %q", got)
		}
		_, _ = w.Write([]byte(`{"provider":"ANTHROPIC","source":"live","models":[
			{"id":"claude-opus-4-8","display_name":"Claude Opus 4.8","provider":"anthropic"},
			{"id":"claude-haiku-4-5","provider":"anthropic"}
		]}`))
	}))
	defer srv.Close()

	res, err := fetchModels(cli.NewClient(srv.URL, "t", "c000000000000000000000ws"), "anthropic")
	if err != nil {
		t.Fatalf("fetchModels: %v", err)
	}
	if res.Source != "live" || res.Provider != "ANTHROPIC" {
		t.Errorf("res = %+v", res)
	}
	if len(res.Models) != 2 || res.Models[0].ID != "claude-opus-4-8" {
		t.Errorf("models = %+v", res.Models)
	}
}

func TestFetchModels_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":"provider query parameter is required"}`))
	}))
	defer srv.Close()

	if _, err := fetchModels(cli.NewClient(srv.URL, "t", "c000000000000000000000ws"), "x"); err == nil {
		t.Fatalf("expected error on 400")
	}
}

func TestFetchModels_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	if _, err := fetchModels(cli.NewClient(url, "t", "c000000000000000000000ws"), "anthropic"); err == nil {
		t.Fatalf("expected transport error against closed server")
	}
}

func TestFetchModels_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	}))
	defer srv.Close()
	if _, err := fetchModels(cli.NewClient(srv.URL, "t", "c000000000000000000000ws"), "anthropic"); err == nil {
		t.Fatalf("expected decode error on malformed body")
	}
}

// --- unit: which source a --provider resolves to ---

func TestProviderServesLiveModels(t *testing.T) {
	// The set GET /api/v1/models answers for is ANTHROPIC, OPENAI, GOOGLE,
	// OLLAMA (internal/api/models.go). This must reproduce it from the
	// registry plus the curated lists WITHOUT a fifth hardcoded copy — if this
	// test starts failing, the derivation and the server have drifted.
	tests := []struct {
		name     string
		provider string
		want     bool
	}{
		{"registry row with a curated list", "anthropic", true},
		{"registry row, curated", "openai", true},
		{"registry row, live-only", "ollama", true},
		{"curated but not in the registry", "google", true},
		{"API enum casing", "ANTHROPIC", true},
		{"padded", "  openai ", true},
		{"catalog only", "deepseek", false},
		{"gateway", "openrouter", false},
		{"catalog only", "xai", false},
		{"unknown", "nope", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name+"/"+tc.provider, func(t *testing.T) {
			if got := providerServesLiveModels(tc.provider); got != tc.want {
				t.Errorf("providerServesLiveModels(%q) = %v, want %v", tc.provider, got, tc.want)
			}
		})
	}
}

func TestKnownProviderIDs(t *testing.T) {
	got := knownProviderIDs()

	registered := llm.RegisteredProviders()
	if len(got) < len(registered) {
		t.Fatalf("knownProviderIDs = %v, shorter than the registry %v", got, registered)
	}
	// Registry rows first, in declaration order.
	for i, id := range registered {
		if got[i] != id {
			t.Errorf("position %d = %q, want %q (registry declaration order first)", i, got[i], id)
		}
	}
	// Catalog-only tail, sorted, no duplicates.
	tail := got[len(registered):]
	for i := 1; i < len(tail); i++ {
		if tail[i-1] >= tail[i] {
			t.Errorf("catalog tail is not sorted: %v", tail)
			break
		}
	}
	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Errorf("duplicate provider id %q in %v", id, got)
		}
		seen[id] = true
	}
	// The whole point of the change: providers with no registry row are still
	// nameable, so the error string is no longer four hardcoded words.
	for _, want := range []string{"deepseek", "openrouter", "xai", "mistral", "google", "amazon-bedrock"} {
		if !seen[want] {
			t.Errorf("knownProviderIDs is missing %q: %v", want, got)
		}
	}
	// Guard against the registry loading nothing.
	if !seen["anthropic"] || !seen["ollama"] {
		t.Errorf("knownProviderIDs is missing a registry row: %v", got)
	}
}

func TestIsAutoSource(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"", true},
		{modelSourceAuto, true},
		{modelSourceLive, false},
		{modelSourceCatalog, false},
	}
	for _, tc := range tests {
		t.Run("source="+tc.in, func(t *testing.T) {
			if got := isAutoSource(tc.in); got != tc.want {
				t.Errorf("isAutoSource(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// --- unit: the catalog path ---

func TestResolveModelList_Catalog(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		source    string
		wantModel string // an id that must be present
		wantRated bool
	}{
		{"catalog-only provider on auto", "deepseek", modelSourceAuto, "deepseek-chat", true},
		{"catalog-only provider, source unset", "deepseek", "", "deepseek-chat", true},
		{"gateway", "xai", modelSourceAuto, "grok-4.6", true},
		{"live provider forced offline", "anthropic", modelSourceCatalog, "claude-opus-4-5", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := resolveModelList(tc.provider, tc.source)
			if err != nil {
				t.Fatalf("resolveModelList(%q, %q): %v", tc.provider, tc.source, err)
			}
			if res.Source != modelSourceCatalog {
				t.Errorf("Source = %q, want %q", res.Source, modelSourceCatalog)
			}
			if res.Provider != tc.provider {
				t.Errorf("Provider = %q, want %q", res.Provider, tc.provider)
			}
			if len(res.Models) == 0 {
				t.Fatalf("no models for %q", tc.provider)
			}
			var found *modelInfoRow
			for i := range res.Models {
				if res.Models[i].ID == tc.wantModel {
					found = &res.Models[i]
				}
			}
			if found == nil {
				t.Fatalf("model %q not in the %q list", tc.wantModel, tc.provider)
			}
			if found.Catalog == nil {
				t.Fatalf("%s carries no catalog facts", found.ID)
			}
			if found.Catalog.ContextTokens <= 0 {
				t.Errorf("%s ContextTokens = %d, want a real context window",
					found.ID, found.Catalog.ContextTokens)
			}
			if tc.wantRated && (found.Catalog.InputPerMTok == nil || found.Catalog.OutputPerMTok == nil) {
				t.Errorf("%s carries no rates: %+v", found.ID, found.Catalog)
			}
		})
	}
}

func TestResolveModelList_UnknownProviderIsNotFound(t *testing.T) {
	tests := []struct {
		name, provider, source string
		// wantSubstr distinguishes the two failures: an id that names no
		// provider at all gets the generated vocabulary, an id that names a
		// live-only provider gets told to stop forcing the catalog.
		wantSubstr string
	}{
		{"auto", "not-a-provider", modelSourceAuto, "deepseek"},
		{"forced catalog", "not-a-provider", modelSourceCatalog, "deepseek"},
		// Ollama has a registry row but no catalog entries; asked for the
		// catalog explicitly it has nothing to give, and that is a not-found,
		// not an empty success that reads as "you have no models".
		{"ollama has no catalog entries", "ollama", modelSourceCatalog, "list it live instead"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveModelList(tc.provider, tc.source)
			if err == nil {
				t.Fatalf("expected an error for %q", tc.provider)
			}
			if got := cli.ExitCodeFor(err); got != cli.ExitNotFound {
				t.Errorf("exit code = %d, want ExitNotFound (%d); err = %v", got, cli.ExitNotFound, err)
			}
			// For the unknown-id cases the hint must be GENERATED, not a
			// literal — a fifth copy of "anthropic, openai, google, ollama" is
			// exactly what this change removes.
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error %q does not contain %q", err, tc.wantSubstr)
			}
		})
	}
	// The live-only message must not tell the operator their provider does
	// not exist while the same binary lists it as known.
	_, err := resolveModelList("ollama", modelSourceCatalog)
	if err != nil && strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("ollama is a registered provider; error should not call it unknown: %v", err)
	}
}

func TestAttachCatalogFacts(t *testing.T) {
	cat := modelcatalog.Default()
	if err := modelcatalog.DefaultErr(); err != nil {
		t.Fatalf("embedded catalog: %v", err)
	}

	tests := []struct {
		name      string
		provider  string
		rows      []modelInfoRow
		wantFacts map[string]bool // id -> catalog facts expected
	}{
		{
			name:     "known and unknown ids side by side",
			provider: "anthropic",
			rows: []modelInfoRow{
				{ID: "claude-opus-4-5", Provider: "anthropic"},
				{ID: "claude-not-a-real-model", Provider: "anthropic"},
			},
			wantFacts: map[string]bool{
				"claude-opus-4-5":         true,
				"claude-not-a-real-model": false,
			},
		},
		{
			// Ollama tags are whatever the daemon pulled; the catalog has no
			// ollama provider at all, so every row stays bare.
			name:      "provider the catalog does not carry",
			provider:  "ollama",
			rows:      []modelInfoRow{{ID: "qwen2.5:3b", Provider: "ollama"}},
			wantFacts: map[string]bool{"qwen2.5:3b": false},
		},
		{
			name:      "empty input",
			provider:  "anthropic",
			rows:      nil,
			wantFacts: map[string]bool{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := attachCatalogFacts(cat, tc.provider, tc.rows)
			if len(got) != len(tc.rows) {
				t.Fatalf("got %d rows, want %d", len(got), len(tc.rows))
			}
			for _, row := range got {
				want, ok := tc.wantFacts[row.ID]
				if !ok {
					t.Fatalf("unexpected row %q", row.ID)
				}
				if (row.Catalog != nil) != want {
					t.Errorf("%s: catalog facts present = %v, want %v", row.ID, row.Catalog != nil, want)
				}
			}
			// The caller's slice must not be annotated in place — fetchModels
			// hands over a decoded payload that nothing else expects to change.
			for _, row := range tc.rows {
				if row.Catalog != nil {
					t.Errorf("attachCatalogFacts mutated the input row %q", row.ID)
				}
			}
		})
	}
}

func TestAttachCatalogFacts_UsesTheSpecCatalogID(t *testing.T) {
	// Every registry row must resolve to a catalog id that either has models
	// or is deliberately absent. A row that silently resolved to "" would
	// annotate nothing and the columns would just be blank forever.
	for _, spec := range llm.RegisteredProviderSpecs() {
		if got := catalogIDFor(spec.ID); got == "" {
			t.Errorf("catalogIDFor(%q) = %q", spec.ID, got)
		}
	}
}

func TestFilterModelRows(t *testing.T) {
	rows := []modelInfoRow{
		{ID: "claude-opus-5", DisplayName: "Claude Opus 5"},
		{ID: "claude-haiku-4-5", DisplayName: "Claude Haiku 4.5"},
		{ID: "gpt-5.5", DisplayName: "GPT-5.5"},
	}
	tests := []struct {
		name   string
		needle string
		want   []string
	}{
		{"empty keeps everything", "", []string{"claude-opus-5", "claude-haiku-4-5", "gpt-5.5"}},
		{"whitespace keeps everything", "   ", []string{"claude-opus-5", "claude-haiku-4-5", "gpt-5.5"}},
		{"matches the id", "haiku", []string{"claude-haiku-4-5"}},
		{"case insensitive", "OPUS", []string{"claude-opus-5"}},
		{"matches the display name", "GPT", []string{"gpt-5.5"}},
		{"display name only", "Haiku 4.5", []string{"claude-haiku-4-5"}},
		{"prefix shared by several", "claude", []string{"claude-opus-5", "claude-haiku-4-5"}},
		{"no match", "gemini", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := filterModelRows(rows, tc.needle)
			var ids []string
			for _, r := range got {
				ids = append(ids, r.ID)
			}
			if strings.Join(ids, ",") != strings.Join(tc.want, ",") {
				t.Errorf("filterModelRows(%q) = %v, want %v", tc.needle, ids, tc.want)
			}
		})
	}
}

// --- unit: rendering ---

func TestFormatTokenCount(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, ""},
		{-1, ""},
		{512, "512"},
		{1000, "1k"},
		{32000, "32k"},
		{65536, "65.5k"},
		{200000, "200k"},
		{384000, "384k"},
		{1000000, "1M"},
		{1048576, "1M"},
		{2000000, "2M"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			if got := formatTokenCount(tc.in); got != tc.want {
				t.Errorf("formatTokenCount(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFormatMTokRate(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name string
		in   *float64
		want string
	}{
		// nil is "we do not know", NOT free. An unpriced model rendered as
		// $0 is exactly the mistake the catalog's pointer types exist to stop.
		{"unpriced", nil, ""},
		{"explicit zero survives", f(0), "0"},
		{"cheap", f(0.0028), "0.0028"},
		{"typical", f(0.195), "0.195"},
		{"whole", f(5), "5"},
		{"expensive", f(49.5), "49.5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatMTokRate(tc.in); got != tc.want {
				t.Errorf("formatMTokRate = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestModelListRows(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	rows := modelListRows([]modelInfoRow{
		{ID: "claude-opus-5", DisplayName: "Claude Opus 5", Catalog: &modelCatalogFacts{
			ContextTokens: 1000000, MaxOutputTokens: 128000, ToolCall: true,
			InputPerMTok: f(5), OutputPerMTok: f(25),
		}},
		// No catalog entry: every column but the id dashes out.
		{ID: "qwen2.5:3b", DisplayName: "qwen2.5:3b"},
		// Known to the catalog but unpriced.
		{ID: "free-thing", Catalog: &modelCatalogFacts{ContextTokens: 8192}},
	})

	const wantCols = 7
	for i, r := range rows {
		if len(r) != wantCols {
			t.Fatalf("row %d has %d cells, want %d: %q", i, len(r), wantCols, r)
		}
		for j, cell := range r {
			if cell == "" {
				t.Errorf("row %d cell %d is empty; unknown values must dash", i, j)
			}
		}
	}
	if got := rows[0]; got[0] != "claude-opus-5" || got[2] != "1M" || got[3] != "128k" ||
		got[4] != "yes" || got[5] != "5" || got[6] != "25" {
		t.Errorf("priced row = %q", got)
	}
	// A display name identical to the id is noise in its own column.
	if rows[1][1] != dashIfEmpty("") {
		t.Errorf("row with name == id should not repeat it: %q", rows[1])
	}
	if rows[1][2] != dashIfEmpty("") || rows[1][5] != dashIfEmpty("") {
		t.Errorf("uncatalogued row should dash its catalog columns: %q", rows[1])
	}
	if rows[2][4] != "no" {
		t.Errorf("tool_call=false must render 'no', not a dash: %q", rows[2])
	}
	if rows[2][5] != dashIfEmpty("") {
		t.Errorf("unpriced model must dash, never read as free: %q", rows[2])
	}
}

func TestModelListRows_PreservesSourceOrder(t *testing.T) {
	// Both curatedModels and modelcatalog.Models return most-capable-and-most-
	// recent first on purpose. Sorting by id here put claude-haiku above
	// claude-opus, which is the wrong default for anything that renders the
	// list top-to-bottom.
	in := []modelInfoRow{{ID: "z-model"}, {ID: "a-model"}, {ID: "m-model"}}
	got := modelListRows(in)
	for i, want := range []string{"z-model", "a-model", "m-model"} {
		if got[i][0] != want {
			t.Errorf("row %d = %q, want %q — source order must survive", i, got[i][0], want)
		}
	}
}

func TestRenderModelList_MachineFormatsAreParseable(t *testing.T) {
	res := &modelListResult{
		Provider: "deepseek",
		Source:   modelSourceCatalog,
		Models: []modelInfoRow{
			{ID: "deepseek-chat", DisplayName: "DeepSeek Chat", Provider: "deepseek",
				Catalog: &modelCatalogFacts{ContextTokens: 1000000, ToolCall: true}},
		},
	}
	var buf bytes.Buffer
	f := cli.NewFormatter("json")
	f.Writer = &buf
	if err := renderModelList(f, res); err != nil {
		t.Fatalf("renderModelList: %v", err)
	}

	var round modelListResult
	if err := json.Unmarshal(buf.Bytes(), &round); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	if round.Source != modelSourceCatalog || len(round.Models) != 1 {
		t.Fatalf("round-tripped = %+v", round)
	}
	if round.Models[0].Catalog == nil || round.Models[0].Catalog.ContextTokens != 1000000 {
		t.Errorf("catalog facts did not survive the round trip: %+v", round.Models[0])
	}
	// No ANSI, no caption: the caption belongs on stderr and the machine
	// formats must not carry it at all.
	if strings.Contains(buf.String(), "\033[") || strings.Contains(buf.String(), "source=") {
		t.Errorf("json output carries human decoration:\n%s", buf.String())
	}
}

func TestRenderModelList_TableDoesNotPanicOnEmpty(t *testing.T) {
	var buf bytes.Buffer
	f := cli.NewFormatter("table")
	f.Writer = &buf
	if err := renderModelList(f, &modelListResult{Provider: "xai", Source: modelSourceCatalog}); err != nil {
		t.Fatalf("renderModelList: %v", err)
	}
	if !strings.Contains(buf.String(), "MODEL") {
		t.Errorf("empty table should still show the headers:\n%s", buf.String())
	}
}

// TestRenderModelList_TableRendersBareIDRows keeps the coverage the old
// TestPrintModelList_NoPanic had before printModelList was renamed to
// renderModelList: the TABLE formatter over one row that carries a display
// name and one that does not. The empty-table test above cannot catch a
// formatter that indexes a name column unconditionally, because it renders no
// rows at all.
func TestRenderModelList_TableRendersBareIDRows(t *testing.T) {
	var buf bytes.Buffer
	f := cli.NewFormatter("table")
	f.Writer = &buf
	res := &modelListResult{
		Provider: "OPENAI",
		Source:   "curated",
		Models: []modelInfoRow{
			{ID: "gpt-4o", DisplayName: "GPT-4o", Provider: "openai"},
			{ID: "o3", Provider: "openai"},
		},
	}
	if err := renderModelList(f, res); err != nil {
		t.Fatalf("renderModelList: %v", err)
	}
	for _, want := range []string{"gpt-4o", "GPT-4o", "o3"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("table missing %q:\n%s", want, buf.String())
		}
	}
}

// --- acceptance: drive the BUILT crewship binary ---

var (
	modelBinOnce sync.Once
	modelBinPath string
	modelBinErr  error
)

// buildCrewshipBinary compiles the CLI once per test binary and caches the
// path. Acceptance tests drive this binary (not hand-rolled HTTP) so the
// `crewship model list` contract is exercised end-to-end: flag parsing,
// config resolution, the HTTP call, and stdout rendering.
func buildCrewshipBinary(t *testing.T) string {
	t.Helper()
	modelBinOnce.Do(func() {
		// Build into a stable temp dir (not t.TempDir, which is cleaned per
		// test) so the once-built binary survives across all tests in this pkg.
		buildDir, err := os.MkdirTemp("", "crewship-bin-")
		if err != nil {
			modelBinErr = err
			return
		}
		out := filepath.Join(buildDir, "crewship")
		cmd := exec.Command("go", "build", "-o", out, ".")
		cmd.Env = os.Environ()
		if combined, err := cmd.CombinedOutput(); err != nil {
			modelBinErr = err
			t.Logf("build output: %s", combined)
			return
		}
		modelBinPath = out
	})
	if modelBinErr != nil {
		t.Fatalf("build crewship binary: %v", modelBinErr)
	}
	return modelBinPath
}

// exitCodeOf pulls the process exit status out of an exec error.
func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	t.Fatalf("not an exit error: %v", err)
	return 0
}

func TestAcceptance_ModelList(t *testing.T) {
	bin := buildCrewshipBinary(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("provider"); got != "anthropic" {
			t.Errorf("provider query = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("auth header = %q, want exact \"Bearer test-token\"", got)
		}
		_, _ = w.Write([]byte(`{"provider":"ANTHROPIC","source":"live","models":[
			{"id":"claude-opus-4-8","display_name":"Claude Opus 4.8","provider":"anthropic"},
			{"id":"claude-sonnet-4-6","provider":"anthropic"}
		]}`))
	}))
	defer srv.Close()

	// Minimal config file: token + workspace satisfy requireAuth/requireWorkspace.
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	if err := os.WriteFile(cfgPath, []byte("token: test-token\nworkspace: c000000000000000000acc\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := exec.Command(bin, "model", "list", "--provider", "anthropic", "--server", srv.URL, "--no-color")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfgPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	got := string(out)
	for _, want := range []string{"claude-opus-4-8", "claude-sonnet-4-6", "source=live"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, got)
		}
	}
	// The live rows are annotated from the embedded catalog: claude-opus-4-8
	// is in the snapshot, so its context window and rates ride along.
	if !strings.Contains(got, "CONTEXT") || !strings.Contains(got, "IN $/MTOK") {
		t.Errorf("live output does not carry the catalog columns:\n%s", got)
	}
}

func TestAcceptance_ModelList_JSON(t *testing.T) {
	bin := buildCrewshipBinary(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"provider":"OPENAI","source":"curated","models":[{"id":"gpt-4o","provider":"openai"}]}`))
	}))
	defer srv.Close()

	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	if err := os.WriteFile(cfgPath, []byte("token: test-token\nworkspace: c000000000000000000acc\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := exec.Command(bin, "model", "list", "--provider", "openai", "--server", srv.URL, "--format", "json")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfgPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, `"source": "curated"`) && !strings.Contains(got, `"source":"curated"`) {
		t.Errorf("json output missing source=curated:\n%s", got)
	}
	if !strings.Contains(got, "gpt-4o") {
		t.Errorf("json output missing gpt-4o:\n%s", got)
	}
}

func TestAcceptance_ModelList_MissingProvider(t *testing.T) {
	bin := buildCrewshipBinary(t)

	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	if err := os.WriteFile(cfgPath, []byte("token: test-token\nworkspace: c000000000000000000acc\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := exec.Command(bin, "model", "list", "--server", "http://127.0.0.1:0")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfgPath)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit for missing --provider; output: %s", out)
	}
	if got := exitCodeOf(t, err); got != cli.ExitValidation {
		t.Errorf("exit code = %d, want ExitValidation (%d)", got, cli.ExitValidation)
	}
	if !strings.Contains(string(out), "--provider is required") {
		t.Errorf("output missing required-provider error:\n%s", out)
	}
	// The hint is generated from the registry and the catalog, so it names
	// providers no hardcoded four-name list ever did.
	if !strings.Contains(string(out), "deepseek") {
		t.Errorf("required-provider error does not name the catalog providers:\n%s", out)
	}
}

// The headline of this change: a provider the server has never heard of is
// listable, with rates, from the binary alone.
func TestAcceptance_ModelList_CatalogProviderOffline(t *testing.T) {
	bin := buildCrewshipBinary(t)

	cmd := exec.Command(bin, "model", "list", "--provider", "deepseek", "--format", "json")
	cmd.Env = offlineEnv(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}

	var res modelListResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out)
	}
	if res.Source != modelSourceCatalog {
		t.Errorf("source = %q, want %q", res.Source, modelSourceCatalog)
	}
	if len(res.Models) == 0 {
		t.Fatalf("no models: %s", out)
	}
	var chat *modelInfoRow
	for i := range res.Models {
		if res.Models[i].ID == "deepseek-chat" {
			chat = &res.Models[i]
		}
	}
	if chat == nil {
		t.Fatalf("deepseek-chat missing from %+v", res.Models)
	}
	if chat.Catalog == nil || chat.Catalog.ContextTokens == 0 ||
		chat.Catalog.InputPerMTok == nil || chat.Catalog.OutputPerMTok == nil {
		t.Fatalf("deepseek-chat carries no catalog facts: %+v", chat.Catalog)
	}
	if !chat.Catalog.ToolCall {
		t.Errorf("deepseek-chat tool_call = false; the snapshot says otherwise")
	}
}

func TestAcceptance_ModelList_ForcedCatalogNeedsNoLogin(t *testing.T) {
	bin := buildCrewshipBinary(t)

	// anthropic normally goes to the server, which would need a token and a
	// workspace. --source catalog must not touch either.
	cmd := exec.Command(bin, "model", "list", "--provider", "anthropic",
		"--source", "catalog", "--search", "haiku", "--format", "quiet")
	cmd.Env = offlineEnv(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	lines := strings.Fields(strings.TrimSpace(string(out)))
	if len(lines) == 0 {
		t.Fatalf("no models matched 'haiku': %s", out)
	}
	for _, id := range lines {
		if !strings.Contains(id, "haiku") {
			t.Errorf("--search leaked a non-matching id: %q", id)
		}
	}
}

func TestAcceptance_ModelList_UnknownProviderExits3(t *testing.T) {
	bin := buildCrewshipBinary(t)

	cmd := exec.Command(bin, "model", "list", "--provider", "not-a-provider")
	cmd.Env = offlineEnv(t)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit; output: %s", out)
	}
	if got := exitCodeOf(t, err); got != cli.ExitNotFound {
		t.Errorf("exit code = %d, want ExitNotFound (%d)\noutput: %s", got, cli.ExitNotFound, out)
	}
	if !strings.Contains(string(out), "unknown provider") {
		t.Errorf("output missing the unknown-provider error:\n%s", out)
	}
}

func TestAcceptance_ModelList_BadSourceExits2(t *testing.T) {
	bin := buildCrewshipBinary(t)

	cmd := exec.Command(bin, "model", "list", "--provider", "deepseek", "--source", "nope")
	cmd.Env = offlineEnv(t)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit; output: %s", out)
	}
	if got := exitCodeOf(t, err); got != cli.ExitValidation {
		t.Errorf("exit code = %d, want ExitValidation (%d)\noutput: %s", got, cli.ExitValidation, out)
	}
	if !strings.Contains(string(out), "--source must be one of") {
		t.Errorf("output missing the --source error:\n%s", out)
	}
}

// An error in a machine format must be a parseable envelope on stderr, not
// prose — that is what exitWithError promises and what an agent parses.
func TestAcceptance_ModelList_JSONErrorEnvelope(t *testing.T) {
	bin := buildCrewshipBinary(t)

	cmd := exec.Command(bin, "model", "list", "--provider", "not-a-provider", "--format", "json")
	cmd.Env = offlineEnv(t)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("expected non-zero exit")
	}

	var env cli.ErrorEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(stderr.Bytes()), &env); err != nil {
		t.Fatalf("stderr is not a JSON error envelope: %v\n%s", err, stderr.String())
	}
	if env.Error.ExitCode != cli.ExitNotFound {
		t.Errorf("envelope exit_code = %d, want %d", env.Error.ExitCode, cli.ExitNotFound)
	}
}

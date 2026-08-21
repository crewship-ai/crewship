package main

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/llmroute"
)

// The row builder is exercised against the REAL descriptor table rather than a
// fixture. A fixture would prove the renderer renders a fixture; what matters
// here is that a provider added to internal/llmroute is complete enough to be
// printed, and that the two facts an operator acts on — where the traffic goes
// and whether a credential is required — are never rendered as a guess.

func TestBuildProviderRouteRows_EveryDescriptorRenders(t *testing.T) {
	t.Parallel()

	specs := llmroute.Specs()
	if len(specs) == 0 {
		// t.Fatal, never t.Skip: a derived guard that goes vacuous reports the
		// same "ok" as a passing one, and this whole file would then be testing
		// nothing while still looking green.
		t.Fatal("llmroute.Specs() is empty; the descriptor table this command renders has gone missing")
	}

	rows := buildProviderRouteRows(specs)
	if len(rows) != len(specs) {
		t.Fatalf("built %d rows for %d specs", len(rows), len(specs))
	}

	for i, row := range rows {
		t.Run(row.ID, func(t *testing.T) {
			if row.ID == "" || row.DisplayName == "" || row.PathPrefix == "" || row.LedgerProvider == "" {
				t.Errorf("row %d is incomplete: %+v", i, row)
			}
			if len(row.Auth) == 0 {
				t.Errorf("%s has no auth rules; the credential would be forwarded nowhere", row.ID)
			}
			// Upstream is empty EXACTLY when it comes from the credential.
			// Either half failing means the table prints a host it invented, or
			// prints nothing where it knows the answer.
			if row.UpstreamFromCredential != (row.Upstream == "") {
				t.Errorf("%s: upstream_from_credential=%v but upstream=%q", row.ID, row.UpstreamFromCredential, row.Upstream)
			}
			if row.UpstreamFromCredential && !row.RequiresCredential {
				t.Errorf("%s: upstream comes from the credential but the route is rendered as credential-optional", row.ID)
			}
			if got, want := len(providerRouteListRows(rows)[i]), len(providerRouteListHeaders); got != want {
				t.Errorf("%s: table row has %d cells, header has %d", row.ID, got, want)
			}
		})
	}
}

// The per-provider expectations. These are the four facts a reader of this
// command acts on, spelled out per descriptor so a change to any of them shows
// up here rather than in an agent's 404.
func TestBuildProviderRouteRow_PerProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		id             string
		wantPath       string
		wantUpstream   string // the rendered cell, not the raw field
		wantAuth       string
		wantCredential string
		wantLedger     string
	}{
		{
			id:             "ANTHROPIC",
			wantPath:       "/v1",
			wantUpstream:   "api.anthropic.com",
			wantAuth:       "header x-api-key (+1 by token prefix)",
			wantCredential: "optional",
			wantLedger:     "anthropic",
		},
		{
			id:             "OPENAI",
			wantPath:       "/openai",
			wantUpstream:   "api.openai.com",
			wantAuth:       "header Authorization: Bearer",
			wantCredential: "optional",
			wantLedger:     "openai",
		},
		{
			id:             "GOOGLE",
			wantPath:       "/gemini",
			wantUpstream:   "generativelanguage.googleapis.com",
			wantAuth:       "header x-goog-api-key + query key",
			wantCredential: "optional",
			wantLedger:     "google",
		},
		{
			id:           "OPENROUTER",
			wantPath:     "/llm/openrouter",
			wantUpstream: "openrouter.ai/api/v1",
			wantAuth:     "header Authorization: Bearer",
			// Required, because there is no env-carried token to fall back on:
			// forwarding without a credential would be a 401 that reads like a
			// bad key.
			wantCredential: "required",
			wantLedger:     "openrouter",
		},
		{
			id:       "OPENAI_COMPAT",
			wantPath: "/llm/openai-compat",
			// Never a hostname: the endpoint is operator data and this command
			// does not read credentials.
			wantUpstream:   "from credential",
			wantAuth:       "header Authorization: Bearer",
			wantCredential: "required",
			wantLedger:     "openai-compat",
		},
	}

	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			spec, ok := llmroute.Lookup(tc.id)
			if !ok {
				t.Fatalf("llmroute has no %s descriptor", tc.id)
			}
			row := buildProviderRouteRow(spec)
			if row.PathPrefix != tc.wantPath {
				t.Errorf("path = %q, want %q", row.PathPrefix, tc.wantPath)
			}
			if got := routeUpstreamCell(row); got != tc.wantUpstream {
				t.Errorf("upstream cell = %q, want %q", got, tc.wantUpstream)
			}
			if got := routeAuthCell(row); got != tc.wantAuth {
				t.Errorf("auth cell = %q, want %q", got, tc.wantAuth)
			}
			if got := routeCredentialCell(row); got != tc.wantCredential {
				t.Errorf("credential cell = %q, want %q", got, tc.wantCredential)
			}
			if row.LedgerProvider != tc.wantLedger {
				t.Errorf("ledger provider = %q, want %q", row.LedgerProvider, tc.wantLedger)
			}
		})
	}
}

// A bring-your-own endpoint has no rate row, so every call through it bills
// $0. That is the honest outcome, and the operator has to be able to SEE it —
// a $0 spend line that looks like a cheap month is the failure mode.
//
// The test is written the other way round too: OpenRouter must NOT be flagged,
// so the marker means something.
func TestRouteBilledCell_FlagsUnpricedRoutes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		id       string
		want     string
		unpriced bool
	}{
		{id: "ANTHROPIC", want: "anthropic"},
		{id: "OPENAI", want: "openai"},
		{id: "GOOGLE", want: "google"},
		{id: "OPENROUTER", want: "openrouter"},
		{id: "OPENAI_COMPAT", want: "openai-compat (unpriced)", unpriced: true},
	}

	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			spec, ok := llmroute.Lookup(tc.id)
			if !ok {
				t.Fatalf("llmroute has no %s descriptor", tc.id)
			}
			row := buildProviderRouteRow(spec)
			if got := routeBilledCell(row); got != tc.want {
				t.Errorf("billed cell = %q, want %q", got, tc.want)
			}
			if row.Priced == tc.unpriced {
				t.Errorf("priced = %v for %s", row.Priced, tc.id)
			}
		})
	}
}

func TestRouteAuthCell_RendersSlotsAndBranches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		auth []providerRouteAuthRule
		want string
	}{
		{
			name: "no rules at all",
			auth: nil,
			want: dashIfEmpty(""),
		},
		{
			name: "one header slot",
			auth: []providerRouteAuthRule{{Slots: []providerRouteSlot{{Placement: "header", Name: "x-api-key"}}}},
			want: "header x-api-key",
		},
		{
			name: "prefix is shown — it is the difference between a call and a 401",
			auth: []providerRouteAuthRule{{Slots: []providerRouteSlot{{Placement: "header", Name: "Authorization", Prefix: "Bearer "}}}},
			want: "header Authorization: Bearer",
		},
		{
			name: "two slots for one token",
			auth: []providerRouteAuthRule{{Slots: []providerRouteSlot{
				{Placement: "header", Name: "x-goog-api-key"},
				{Placement: "query", Name: "key"},
			}}},
			want: "header x-goog-api-key + query key",
		},
		{
			name: "the DEFAULT rule is the one summarised, not the first",
			auth: []providerRouteAuthRule{
				{TokenPrefix: "sk-ant-oat", Slots: []providerRouteSlot{{Placement: "header", Name: "Authorization", Prefix: "Bearer "}}},
				{Slots: []providerRouteSlot{{Placement: "header", Name: "x-api-key"}}},
			},
			want: "header x-api-key (+1 by token prefix)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := routeAuthCell(providerRouteRow{Auth: tc.auth}); got != tc.want {
				t.Errorf("routeAuthCell = %q, want %q", got, tc.want)
			}
		})
	}
}

// `route show` prints every auth branch, not just the summarised one: an
// operator whose OAuth token landed in the wrong header is looking for exactly
// the branch the table view collapses.
func TestProviderRouteDetailPairs_ShowsEveryAuthBranch(t *testing.T) {
	t.Parallel()

	spec, ok := llmroute.Lookup("ANTHROPIC")
	if !ok {
		t.Fatal("llmroute has no ANTHROPIC descriptor")
	}
	pairs := providerRouteDetailPairs(buildProviderRouteRow(spec))

	flat := map[string]string{}
	var text []string
	for _, p := range pairs {
		if len(p) != 2 {
			t.Fatalf("detail pair is not a (label, value): %v", p)
		}
		flat[p[0]] = p[1]
		text = append(text, p[0]+"="+p[1])
	}
	joined := strings.Join(text, "\n")

	for _, want := range []string{"Auth (token sk-ant-oat…)", "Auth (default)", "Static headers"} {
		if _, ok := flat[want]; !ok {
			t.Errorf("detail is missing the %q line:\n%s", want, joined)
		}
	}
	if got := flat["Auth (default)"]; got != "header x-api-key" {
		t.Errorf("default auth line = %q", got)
	}
	if got := flat["Static headers"]; got != "anthropic-version: 2023-06-01" {
		t.Errorf("static headers line = %q", got)
	}
	// "strip: false" reads as a missing feature; the line has to say what
	// actually happens to /v1/messages.
	if got := flat["Prefix"]; got != "forwarded verbatim" {
		t.Errorf("prefix line = %q, want it to spell out that /v1 is not stripped", got)
	}
}

func TestCanonRouteProviderID(t *testing.T) {
	t.Parallel()

	tests := []struct{ in, want string }{
		{"openrouter", "OPENROUTER"},
		{"OpenRouter", "OPENROUTER"},
		{"  openai_compat  ", "OPENAI_COMPAT"},
		{"OPENAI_COMPAT", "OPENAI_COMPAT"},
	}
	for _, tc := range tests {
		if got := canonRouteProviderID(tc.in); got != tc.want {
			t.Errorf("canonRouteProviderID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// Every id the table carries must survive the widening, or `route show`
	// would reject a value the table itself printed.
	for _, s := range llmroute.Specs() {
		if got := canonRouteProviderID(strings.ToLower(s.ID)); got != s.ID {
			t.Errorf("canonRouteProviderID(%q) = %q, want %q", strings.ToLower(s.ID), got, s.ID)
		}
	}
}

// --- acceptance: the built binary, offline ---

func TestAcceptance_ProviderRouteList_Offline(t *testing.T) {
	bin := buildCrewshipBinary(t)

	cmd := exec.Command(bin, "provider", "route", "list")
	cmd.Env = offlineEnv(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	got := string(out)
	for _, want := range []string{"OPENROUTER", "/llm/openrouter", "OPENAI_COMPAT", "from credential", "required", "optional"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestAcceptance_ProviderRouteList_JSONOffline(t *testing.T) {
	bin := buildCrewshipBinary(t)

	cmd := exec.Command(bin, "provider", "route", "list", "--format", "json")
	cmd.Env = offlineEnv(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}

	var res providerRouteListResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal --format json output: %v\noutput: %s", err, out)
	}
	if len(res.Routes) != len(llmroute.Specs()) {
		t.Fatalf("got %d routes, want %d", len(res.Routes), len(llmroute.Specs()))
	}

	byID := map[string]providerRouteRow{}
	for _, r := range res.Routes {
		byID[r.ID] = r
	}
	compat, ok := byID["OPENAI_COMPAT"]
	if !ok {
		t.Fatalf("no OPENAI_COMPAT route: %+v", res.Routes)
	}
	if !compat.UpstreamFromCredential || !compat.RequiresCredential || compat.Upstream != "" {
		t.Errorf("OPENAI_COMPAT row = %+v", compat)
	}
	if compat.Priced {
		t.Errorf("OPENAI_COMPAT reports as priced; nothing prices a bring-your-own endpoint")
	}
	// The forward-proxy asymmetry is a deliberate property, not an oversight:
	// claiming openrouter.ai here would turn today's pass-through into a 503
	// for every BYOK crew that dials it with its own key.
	if router := byID["OPENROUTER"]; len(router.ForwardProxyHosts) != 0 {
		t.Errorf("OPENROUTER forward_proxy_hosts = %v, want none", router.ForwardProxyHosts)
	}
	if anthropic := byID["ANTHROPIC"]; len(anthropic.ForwardProxyHosts) == 0 {
		t.Errorf("ANTHROPIC forward_proxy_hosts is empty; the forward-proxy path still routes it by host")
	}
}

func TestAcceptance_ProviderRouteShow_Offline(t *testing.T) {
	bin := buildCrewshipBinary(t)

	// Lower case on purpose: this is how the id is written on
	// `credential create --provider`, and the two must agree.
	cmd := exec.Command(bin, "provider", "route", "show", "openai_compat")
	cmd.Env = offlineEnv(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	got := string(out)
	for _, want := range []string{"OPENAI_COMPAT", "/llm/openai-compat", "from credential", "required", "unpriced"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestAcceptance_ProviderRouteShow_UnknownExits3(t *testing.T) {
	bin := buildCrewshipBinary(t)

	cmd := exec.Command(bin, "provider", "route", "show", "bedrock")
	cmd.Env = offlineEnv(t)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit; output: %s", out)
	}
	if got := exitCodeOf(t, err); got != cli.ExitNotFound {
		t.Errorf("exit code = %d, want ExitNotFound (%d)\noutput: %s", got, cli.ExitNotFound, out)
	}
	if !strings.Contains(string(out), "unknown provider route") {
		t.Errorf("output missing the unknown-route error:\n%s", out)
	}
}

// The command group has to be reachable from the manifest the smoke script
// walks — a subcommand that failed to register is invisible, not failing.
func TestAcceptance_ProviderRouteCommandIsRegistered(t *testing.T) {
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
	for _, want := range []string{"provider route", "provider route list", "provider route show"} {
		if !paths[want] {
			t.Errorf("command manifest is missing %q", want)
		}
	}
}

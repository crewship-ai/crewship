package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// covSetCapAdd sets the cap-add StringSlice flag cleanly (pflag StringSlice Set
// APPENDS after the first call, so covSetFlagCli5 is unusable for it) and marks
// it Changed so the #1380 security dispatch fires. Restores empty on cleanup.
func covSetCapAdd(t *testing.T, vals ...string) {
	t.Helper()
	f := crewConfigCmd.Flags().Lookup("cap-add")
	if f == nil {
		t.Fatal("flag --cap-add not registered")
	}
	sv, ok := f.Value.(pflag.SliceValue)
	if !ok {
		t.Fatal("cap-add is not a SliceValue")
	}
	_ = sv.Replace(vals)
	f.Changed = true
	t.Cleanup(func() {
		_ = sv.Replace(nil)
		f.Changed = false
	})
}

// ─── pure helpers ────────────────────────────────────────────────────────

func TestPrettyJSON(t *testing.T) {
	t.Parallel()
	if got := prettyJSON(""); got != "-" {
		t.Errorf("empty: got %q, want -", got)
	}
	if got := prettyJSON("not json"); got != "not json" {
		t.Errorf("invalid: got %q, want passthrough", got)
	}
	got := prettyJSON(`{"image":"debian"}`)
	if !strings.Contains(got, "\"image\": \"debian\"") || !strings.Contains(got, "\n") {
		t.Errorf("valid JSON should be re-indented; got %q", got)
	}
}

func TestDerefOrDash(t *testing.T) {
	t.Parallel()
	if got := derefOrDash(nil); got != "-" {
		t.Errorf("nil: got %q", got)
	}
	empty := ""
	if got := derefOrDash(&empty); got != "-" {
		t.Errorf("empty: got %q", got)
	}
	val := "x"
	if got := derefOrDash(&val); got != "x" {
		t.Errorf("value: got %q", got)
	}
}

func TestReadConfigFile(t *testing.T) {
	t.Parallel()
	_, err := readConfigFile(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil || !strings.Contains(err.Error(), "file not found") {
		t.Errorf("missing file: got %v", err)
	}

	p := filepath.Join(t.TempDir(), "dev.json")
	if err := os.WriteFile(p, []byte(`{"image":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := readConfigFile(p)
	if err != nil {
		t.Fatalf("readConfigFile: %v", err)
	}
	if data != `{"image":"x"}` {
		t.Errorf("data = %q", data)
	}
}

// ─── mode validation ─────────────────────────────────────────────────────

func covResetCrewConfigFlags(t *testing.T) {
	t.Helper()
	covSetFlagCli5(t, crewConfigCmd, "show", "false")
	covSetFlagCli5(t, crewConfigCmd, "export", "false")
	covSetFlagCli5(t, crewConfigCmd, "clear", "false")
	covSetFlagCli5(t, crewConfigCmd, "devcontainer", "")
	covSetFlagCli5(t, crewConfigCmd, "mise", "")
	covSetFlagCli5(t, crewConfigCmd, "runtime-image", "")
	covSetFlagCli5(t, crewConfigCmd, "privileged", "false")
	covSetFlagCli5(t, crewConfigCmd, "init", "false")
	// cap-add is a StringSlice (Set appends); reset it via SliceValue.Replace.
	if f := crewConfigCmd.Flags().Lookup("cap-add"); f != nil {
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			_ = sv.Replace(nil)
		}
	}
	// pflag marks a flag Changed on every Set (including the resets above and
	// any prior test's Set), and the crewConfigCmd is package-global — so the
	// #1380 security dispatch, which keys off Changed(), would leak across
	// tests. Clear the Changed bit for every flag we touch to keep tests
	// hermetic regardless of run order.
	for _, name := range []string{"show", "export", "clear", "devcontainer", "mise",
		"runtime-image", "privileged", "init", "cap-add"} {
		if f := crewConfigCmd.Flags().Lookup(name); f != nil {
			f.Changed = false
		}
	}
}

func TestCrewConfigRunE_NoMode(t *testing.T) {
	covSetupCli5(t)
	covResetCrewConfigFlags(t)

	err := crewConfigCmd.RunE(crewConfigCmd, []string{covCrewIDCli5})
	if err == nil || !strings.Contains(err.Error(), "specify one of") {
		t.Errorf("expected no-mode error; got %v", err)
	}
}

func TestCrewConfigRunE_ConflictingModes(t *testing.T) {
	covSetupCli5(t)
	covResetCrewConfigFlags(t)
	covSetFlagCli5(t, crewConfigCmd, "show", "true")
	covSetFlagCli5(t, crewConfigCmd, "clear", "true")

	err := crewConfigCmd.RunE(crewConfigCmd, []string{covCrewIDCli5})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected mutual-exclusion error; got %v", err)
	}
}

func TestCrewConfigRunE_NoAuth(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{}
	err := crewConfigCmd.RunE(crewConfigCmd, []string{covCrewIDCli5})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}
}

// ─── show / export / clear / set ─────────────────────────────────────────

func covStubCrewDetail(stub *clitest.StubServer) {
	stub.OnGet("/api/v1/crews/"+covCrewIDCli5, clitest.JSONResponse(200, map[string]any{
		"id":                  covCrewIDCli5,
		"name":                "Backend",
		"slug":                "backend",
		"runtime_image":       "debian:bookworm-slim",
		"devcontainer_config": `{"features":{}}`,
		"mise_config":         "not-json-at-all",
		"cached_image":        "crewship-cache:abc",
		"config_hash":         "deadbeef",
	}))
}

func TestCrewConfigRunE_Show(t *testing.T) {
	stub := covSetupCli5(t)
	covResetCrewConfigFlags(t)
	covSetFlagCli5(t, crewConfigCmd, "show", "true")
	covStubCrewDetail(stub)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli5+"/provision",
		clitest.JSONResponse(200, map[string]any{"status": "completed"}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = crewConfigCmd.RunE(crewConfigCmd, []string{covCrewIDCli5}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{
		"Name:          Backend",
		"Slug:          backend",
		"Runtime Image: debian:bookworm-slim",
		"Cached Image:  crewship-cache:abc",
		"Config Hash:   deadbeef",
		"Status:        completed",
		`"features": {}`,  // devcontainer pretty-printed
		"not-json-at-all", // mise passthrough
	} {
		if !strings.Contains(out, want) {
			t.Errorf("show output missing %q; got:\n%s", want, out)
		}
	}
}

func TestCrewConfigRunE_Export(t *testing.T) {
	stub := covSetupCli5(t)
	covResetCrewConfigFlags(t)
	covSetFlagCli5(t, crewConfigCmd, "export", "true")
	covStubCrewDetail(stub)

	var err error
	out := covCaptureStdoutCli5(t, func() { err = crewConfigCmd.RunE(crewConfigCmd, []string{covCrewIDCli5}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var v map[string]any
	if jsonErr := json.Unmarshal([]byte(out), &v); jsonErr != nil {
		t.Fatalf("export is not JSON: %v\n%s", jsonErr, out)
	}
	if v["runtime_image"] != "debian:bookworm-slim" {
		t.Errorf("runtime_image = %v", v["runtime_image"])
	}
	// Valid JSON column → embedded object; invalid → raw string.
	if _, ok := v["devcontainer_config"].(map[string]any); !ok {
		t.Errorf("devcontainer_config should embed parsed JSON; got %T", v["devcontainer_config"])
	}
	if v["mise_config"] != "not-json-at-all" {
		t.Errorf("mise_config = %v, want raw string", v["mise_config"])
	}
}

func TestCrewConfigRunE_Clear(t *testing.T) {
	stub := covSetupCli5(t)
	covResetCrewConfigFlags(t)
	covSetFlagCli5(t, crewConfigCmd, "clear", "true")
	stub.OnPatch("/api/v1/crews/"+covCrewIDCli5, clitest.JSONResponse(200, map[string]any{"ok": true}))

	var err error
	covCaptureAll(t, func() { err = crewConfigCmd.RunE(crewConfigCmd, []string{covCrewIDCli5}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("PATCH", "/api/v1/crews/"+covCrewIDCli5)
	if len(calls) != 1 {
		t.Fatalf("expected 1 PATCH, got %d", len(calls))
	}
	var body map[string]*string
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	for _, k := range []string{"devcontainer_config", "mise_config", "runtime_image"} {
		if body[k] == nil || *body[k] != "" {
			t.Errorf("clear must send empty string for %s; got %v", k, body[k])
		}
	}
}

func TestCrewConfigRunE_SetAllThree(t *testing.T) {
	stub := covSetupCli5(t)
	covResetCrewConfigFlags(t)
	dir := t.TempDir()
	devPath := filepath.Join(dir, "devcontainer.json")
	misePath := filepath.Join(dir, "mise.json")
	if err := os.WriteFile(devPath, []byte(`{"image":"base"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(misePath, []byte(`{"tools":{"go":"1.24"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	covSetFlagCli5(t, crewConfigCmd, "devcontainer", devPath)
	covSetFlagCli5(t, crewConfigCmd, "mise", misePath)
	covSetFlagCli5(t, crewConfigCmd, "runtime-image", "ubuntu:24.04")
	stub.OnPatch("/api/v1/crews/"+covCrewIDCli5, clitest.JSONResponse(200, map[string]any{"ok": true}))

	var err error
	covCaptureAll(t, func() { err = crewConfigCmd.RunE(crewConfigCmd, []string{covCrewIDCli5}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(stub.CallsFor("PATCH", "/api/v1/crews/"+covCrewIDCli5)[0].Body, &body)
	if body["devcontainer_config"] != `{"image":"base"}` {
		t.Errorf("devcontainer_config = %q", body["devcontainer_config"])
	}
	if body["mise_config"] != `{"tools":{"go":"1.24"}}` {
		t.Errorf("mise_config = %q", body["mise_config"])
	}
	if body["runtime_image"] != "ubuntu:24.04" {
		t.Errorf("runtime_image = %q", body["runtime_image"])
	}
}

// #1380: --privileged / --cap-add merge onto the stored devcontainer_config
// (preserving image/features) and PATCH it back, where the server validates.
func TestCrewConfigRunE_SetSecurityMergesConfig(t *testing.T) {
	stub := covSetupCli5(t)
	covResetCrewConfigFlags(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli5, clitest.JSONResponse(200, map[string]any{
		"id":                  covCrewIDCli5,
		"name":                "Backend",
		"slug":                "backend",
		"devcontainer_config": `{"image":"debian:bookworm-slim","features":{"x":{}}}`,
	}))
	stub.OnPatch("/api/v1/crews/"+covCrewIDCli5, clitest.JSONResponse(200, map[string]any{"ok": true}))
	covSetFlagCli5(t, crewConfigCmd, "privileged", "true")
	covSetCapAdd(t, "cap_net_bind_service")

	var err error
	covCaptureAll(t, func() { err = crewConfigCmd.RunE(crewConfigCmd, []string{covCrewIDCli5}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(stub.CallsFor("PATCH", "/api/v1/crews/"+covCrewIDCli5)[0].Body, &body)
	var sent map[string]any
	if jsonErr := json.Unmarshal([]byte(body["devcontainer_config"]), &sent); jsonErr != nil {
		t.Fatalf("patched config not JSON: %v", jsonErr)
	}
	if sent["image"] != "debian:bookworm-slim" {
		t.Errorf("image clobbered: %v", sent["image"])
	}
	if _, ok := sent["features"]; !ok {
		t.Errorf("features dropped on merge: %v", sent)
	}
	if sent["privileged"] != true {
		t.Errorf("privileged not set: %v", sent["privileged"])
	}
	caps, _ := sent["capAdd"].([]any)
	if len(caps) != 1 || caps[0] != "NET_BIND_SERVICE" {
		t.Errorf("capAdd = %v, want [NET_BIND_SERVICE] normalized", sent["capAdd"])
	}
}

// A crew with no stored devcontainer_config can't take privilege knobs alone —
// there'd be no base image, so the server would reject an image-less config.
func TestCrewConfigRunE_SetSecurityNoBaseConfig(t *testing.T) {
	stub := covSetupCli5(t)
	covResetCrewConfigFlags(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli5, clitest.JSONResponse(200, map[string]any{
		"id": covCrewIDCli5, "name": "Backend", "slug": "backend",
	}))
	covSetFlagCli5(t, crewConfigCmd, "privileged", "true")

	err := crewConfigCmd.RunE(crewConfigCmd, []string{covCrewIDCli5})
	if err == nil || !strings.Contains(err.Error(), "no devcontainer_config") {
		t.Errorf("expected no-base-config error; got %v", err)
	}
}

func TestCrewConfigRunE_SetMissingFile(t *testing.T) {
	covSetupCli5(t)
	covResetCrewConfigFlags(t)
	covSetFlagCli5(t, crewConfigCmd, "devcontainer", filepath.Join(t.TempDir(), "nope.json"))

	err := crewConfigCmd.RunE(crewConfigCmd, []string{covCrewIDCli5})
	if err == nil || !strings.Contains(err.Error(), "file not found") {
		t.Errorf("expected file-not-found; got %v", err)
	}
}

func TestPatchCrew_APIError(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPatch("/api/v1/crews/"+covCrewIDCli5, clitest.ErrorResponse(403, "Forbidden: requires ADMIN"))
	client := newAPIClient()

	err := patchCrew(client, covCrewIDCli5, map[string]interface{}{"runtime_image": "x"}, "nope")
	if err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Errorf("expected 403; got %v", err)
	}
}

func TestFetchCrewInfo_APIError(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli5, clitest.ErrorResponse(404, "Crew not found"))
	client := newAPIClient()

	_, err := fetchCrewInfo(client, covCrewIDCli5)
	if err == nil || !strings.Contains(err.Error(), "Crew not found") {
		t.Errorf("expected 404; got %v", err)
	}
}

func TestFetchProvisionStatus_APIError(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli5+"/provision", clitest.ErrorResponse(500, "wedged"))
	client := newAPIClient()

	_, err := fetchProvisionStatus(client, covCrewIDCli5)
	if err == nil || !strings.Contains(err.Error(), "wedged") {
		t.Errorf("expected 500; got %v", err)
	}
}

// ─── round 2: remaining error branches ───────────────────────────────────

func TestCrewConfigRunE_NoWorkspace(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token"}
	err := crewConfigCmd.RunE(crewConfigCmd, []string{covCrewIDCli5})
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}
}

func TestCrewConfigRunE_UnknownCrew(t *testing.T) {
	stub := covSetupCli5(t)
	covResetCrewConfigFlags(t)
	covSetFlagCli5(t, crewConfigCmd, "show", "true")
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))

	err := crewConfigCmd.RunE(crewConfigCmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "crew not found") {
		t.Errorf("expected crew-not-found; got %v", err)
	}
}

func TestFetchHelpers_MalformedResponses(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli5, clitest.TextResponse(200, "not json"))
	stub.OnGet("/api/v1/crews/"+covCrewIDCli5+"/provision", clitest.TextResponse(200, "not json"))
	client := newAPIClient()

	if _, err := fetchCrewInfo(client, covCrewIDCli5); err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("fetchCrewInfo: expected decode error; got %v", err)
	}
	if _, err := fetchProvisionStatus(client, covCrewIDCli5); err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("fetchProvisionStatus: expected decode error; got %v", err)
	}
}

func TestShowCrewConfig_FetchErrors(t *testing.T) {
	// Crew detail fails first.
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli5, clitest.ErrorResponse(500, "crew wedged"))
	client := newAPIClient()
	if err := showCrewConfig(client, covCrewIDCli5); err == nil || !strings.Contains(err.Error(), "crew wedged") {
		t.Errorf("expected crew error; got %v", err)
	}

	// Crew detail OK, provision status fails.
	stub2 := covSetupCli5(t)
	stub2.OnGet("/api/v1/crews/"+covCrewIDCli5, clitest.JSONResponse(200, map[string]any{
		"id": covCrewIDCli5, "name": "B", "slug": "b",
	}))
	stub2.OnGet("/api/v1/crews/"+covCrewIDCli5+"/provision", clitest.ErrorResponse(500, "provision wedged"))
	client2 := newAPIClient()
	if err := showCrewConfig(client2, covCrewIDCli5); err == nil || !strings.Contains(err.Error(), "provision wedged") {
		t.Errorf("expected provision error; got %v", err)
	}
}

func TestExportCrewConfig_FetchError(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli5, clitest.ErrorResponse(404, "Crew not found"))
	client := newAPIClient()

	if err := exportCrewConfig(client, covCrewIDCli5); err == nil || !strings.Contains(err.Error(), "Crew not found") {
		t.Errorf("expected fetch error; got %v", err)
	}
}

func TestSetCrewConfig_MiseFileMissing(t *testing.T) {
	covSetupCli5(t)
	client := newAPIClient()
	err := setCrewConfig(client, covCrewIDCli5, "", filepath.Join(t.TempDir(), "ghost-mise.json"), "")
	if err == nil || !strings.Contains(err.Error(), "file not found") {
		t.Errorf("expected mise file-not-found; got %v", err)
	}
}

func TestPatchCrew_TransportError(t *testing.T) {
	stub := covSetupCli5(t)
	client := newAPIClient()
	stub.Close()

	err := patchCrew(client, covCrewIDCli5, map[string]interface{}{"runtime_image": "x"}, "msg")
	if err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Errorf("expected transport error; got %v", err)
	}
}

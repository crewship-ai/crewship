package main

// `crewship agent ask-preview` and `agent update --ask-forms`, driven end to
// end through the BUILT binary against a stub server.
//
// Driving the binary rather than calling RunE is the point: the contract an
// author uses is flag parsing, @file reading, the HTTP call and what lands on
// stdout, and every one of those is outside the function body. It is also the
// only place the shared golden fixture is exercised through a real command —
// the renderer, the caps and the line-drop rule all run here exactly as they
// do in a chat.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// askAgentJSON is the agent detail the stub serves: one form with a money
// field, a select and a file, which between them cover every rendering rule
// worth checking from a command line.
const askAgentJSON = `{
	"id":"cag00000000000000000ask","slug":"lucy","name":"Lucy",
	"agent_role":"AGENT","status":"IDLE","cli_adapter":"CLAUDE_CODE",
	"tool_profile":"CODING","memory_enabled":false,"timeout_seconds":300,
	"created_at":"2026-08-13T00:00:00Z","schedule_enabled":false,
	"webhook_require_timestamp":false,"suggested_prompts":null,
	"ask_forms":"[{\"id\":\"receipt\",\"label\":\"Add a receipt\",\"attachment\":\"required\",\"template\":\"Please file this receipt.\\n\\nSupplier: {{supplier}}\\nAmount: {{amount}} {{amount_currency}}\\nCategory: {{category}}\\nDocument: {{document}}\",\"fields\":[{\"name\":\"supplier\",\"label\":\"Supplier\",\"type\":\"text\"},{\"name\":\"amount\",\"label\":\"Amount\",\"type\":\"money\",\"currency\":[\"CZK\"]},{\"name\":\"category\",\"label\":\"Category\",\"type\":\"select\",\"options\":[\"Telco\"]},{\"name\":\"document\",\"label\":\"Document\",\"type\":\"file\"}]}]",
	"_count":{"skills":0,"credentials":0}
}`

func askStubServer(t *testing.T, agentJSON string, onPatch func(body map[string]any)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch:
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("PATCH body is not JSON: %v (%s)", err, raw)
			}
			if onPatch != nil {
				onPatch(body)
			}
			_, _ = w.Write([]byte(agentJSON))
		case r.URL.Path == "/api/v1/agents":
			// resolveAgentID's slug scan.
			_, _ = w.Write([]byte("[" + agentJSON + "]"))
		default:
			_, _ = w.Write([]byte(agentJSON))
		}
	}))
}

func askCLIConfig(t *testing.T) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	if err := os.WriteFile(cfgPath,
		[]byte("token: test-token\nworkspace: c000000000000000000acc\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func TestAcceptance_AgentAskPreview(t *testing.T) {
	bin := buildCrewshipBinary(t)
	srv := askStubServer(t, askAgentJSON, nil)
	defer srv.Close()

	out, err := runCrewship(t, bin, askCLIConfig(t), srv.URL,
		"agent", "ask-preview", "lucy", "receipt",
		"--var", "supplier=Vodafone",
		"--var", "amount=1249",
		"--var", "amount_currency=CZK",
		"--var", "document=IMG_4821.heic",
		"--chat-id", "chat_7f3a")
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}

	want := "Please file this receipt.\n\nSupplier: Vodafone\nAmount: 1249 CZK\n" +
		"Document: attachments/chat_7f3a/IMG_4821.heic"
	if strings.TrimSpace(out) != want {
		t.Fatalf("rendered message =\n%q\nwant\n%q", strings.TrimSpace(out), want)
	}
	// The category line proves the one piece of magic end to end: no value
	// was given for it, so the whole line went — static label included.
	if strings.Contains(out, "Category") {
		t.Errorf("an unanswered optional field left its label behind:\n%s", out)
	}
}

func TestAcceptance_AgentAskPreview_RepeatedVarIsAList(t *testing.T) {
	bin := buildCrewshipBinary(t)
	srv := askStubServer(t, askAgentJSON, nil)
	defer srv.Close()

	out, err := runCrewship(t, bin, askCLIConfig(t), srv.URL,
		"agent", "ask-preview", "lucy", "receipt",
		"--var", "supplier=Vodafone",
		"--var", "document=page1.heic",
		"--var", "document=page2.heic",
		"--chat-id", "c9")
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	for _, want := range []string{"attachments/c9/page1.heic", "attachments/c9/page2.heic"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q — a repeated --var must accumulate, not "+
				"overwrite, or a photographed multi-page invoice loses pages:\n%s", want, out)
		}
	}
}

func TestAcceptance_AgentAskPreview_UnknownFormNamesWhatExists(t *testing.T) {
	bin := buildCrewshipBinary(t)
	srv := askStubServer(t, askAgentJSON, nil)
	defer srv.Close()

	out, err := runCrewship(t, bin, askCLIConfig(t), srv.URL,
		"agent", "ask-preview", "lucy", "invoice")
	if err == nil {
		t.Fatalf("expected a non-zero exit for an unknown form id; output: %s", out)
	}
	if !strings.Contains(out, "invoice") || !strings.Contains(out, "receipt") {
		t.Errorf("error should name both the id asked for and the ids that exist:\n%s", out)
	}
}

func TestAcceptance_AgentAskPreview_NoFormsConfigured(t *testing.T) {
	bin := buildCrewshipBinary(t)
	bare := strings.Replace(askAgentJSON,
		`"ask_forms":"[{\"id\":\"receipt\"`, `"ask_forms":null,"unused":"[{\"id\":\"receipt\"`, 1)
	srv := askStubServer(t, bare, nil)
	defer srv.Close()

	out, err := runCrewship(t, bin, askCLIConfig(t), srv.URL, "agent", "ask-preview", "lucy", "receipt")
	if err == nil {
		t.Fatalf("expected a non-zero exit when no forms are configured; output: %s", out)
	}
	if !strings.Contains(out, "--ask-forms") {
		t.Errorf("the error should say how to configure one:\n%s", out)
	}
}

// `agent update --ask-forms @file.json` — the shape anyone actually uses, and
// the reason @file support is not optional: a four-field form with a template
// is not something typed on a command line.
func TestAcceptance_AgentUpdateAskFormsFromFile(t *testing.T) {
	bin := buildCrewshipBinary(t)

	var sent map[string]any
	srv := askStubServer(t, askAgentJSON, func(body map[string]any) { sent = body })
	defer srv.Close()

	formsPath := filepath.Join(t.TempDir(), "forms.json")
	definition := `[{"id":"receipt","label":"Add a receipt","template":"Supplier: {{supplier}}",
		"fields":[{"name":"supplier","label":"Supplier","type":"text"}]}]`
	if err := os.WriteFile(formsPath, []byte(definition), 0o600); err != nil {
		t.Fatalf("write forms file: %v", err)
	}

	out, err := runCrewship(t, bin, askCLIConfig(t), srv.URL,
		"agent", "update", "lucy", "--ask-forms", "@"+formsPath)
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	if sent == nil {
		t.Fatal("no PATCH reached the server")
	}
	got, _ := sent["ask_forms"].(string)
	if !strings.Contains(got, `"id":"receipt"`) {
		t.Errorf("PATCH body ask_forms = %q, want the file's contents verbatim "+
			"(the server is what validates and canonicalises it)", got)
	}
}

func TestAcceptance_AgentUpdateAskFormsClears(t *testing.T) {
	bin := buildCrewshipBinary(t)

	var sent map[string]any
	srv := askStubServer(t, askAgentJSON, func(body map[string]any) { sent = body })
	defer srv.Close()

	out, err := runCrewship(t, bin, askCLIConfig(t), srv.URL,
		"agent", "update", "lucy", "--ask-forms", "")
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	if v, ok := sent["ask_forms"]; !ok || v != "" {
		t.Errorf("PATCH body ask_forms = %v (present=%v), want an explicit empty "+
			"string so the column is cleared rather than left alone", v, ok)
	}
}

// `agent get` has to show that forms exist at all, or the only way to find out
// is to read the column.
func TestAcceptance_AgentGetListsAskForms(t *testing.T) {
	bin := buildCrewshipBinary(t)
	srv := askStubServer(t, askAgentJSON, nil)
	defer srv.Close()

	out, err := runCrewship(t, bin, askCLIConfig(t), srv.URL, "agent", "get", "lucy", "--no-color")
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	for _, want := range []string{"Ask Forms", "Add a receipt", "receipt", "4 fields"} {
		if !strings.Contains(out, want) {
			t.Errorf("`agent get` output is missing %q:\n%s", want, out)
		}
	}
}

func runCrewship(t *testing.T, bin, cfgPath, serverURL string, args ...string) (string, error) {
	t.Helper()
	full := append(args, "--server", serverURL)
	cmd := exec.Command(bin, full...)
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfgPath)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

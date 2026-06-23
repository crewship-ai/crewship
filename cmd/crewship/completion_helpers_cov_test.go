package main

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// writeCLIConfig points $CREWSHIP_CONFIG at a temp YAML file so
// cli.LoadConfig() inside completeAgentSlug reads a known config without
// touching the developer's real ~/.crewship.
func writeCLIConfig(t *testing.T, yaml string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cli-config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("CREWSHIP_CONFIG", path)
}

func TestCompleteAgentSlug_SecondArgNotCompleted(t *testing.T) {
	saveCLIState(t)

	out, directive := completeAgentSlug(&cobra.Command{}, []string{"already-set"}, "vi")
	if out != nil {
		t.Errorf("suggestions: got %v want nil", out)
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive: got %v want NoFileComp", directive)
	}
}

func TestCompleteAgentSlug_NoToken(t *testing.T) {
	saveCLIState(t)
	// Config file does not exist → LoadConfig returns empty config → no token.
	t.Setenv("CREWSHIP_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))

	out, directive := completeAgentSlug(&cobra.Command{}, nil, "")
	if out != nil {
		t.Errorf("suggestions: got %v want nil without a token", out)
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive: got %v want NoFileComp", directive)
	}
}

func TestCompleteAgentSlug_FetchError(t *testing.T) {
	saveCLIState(t)
	flagServer = ""
	flagWorkspace = ""

	// Server that is already closed → Get fails.
	stub := clitest.NewStubServer()
	deadURL := stub.URL()
	stub.Close()

	writeCLIConfig(t, "token: tok\nserver: "+deadURL+"\nworkspace: cabcdefghijklmnopqrs\n")

	out, directive := completeAgentSlug(&cobra.Command{}, nil, "")
	if out != nil {
		t.Errorf("suggestions: got %v want nil on fetch error", out)
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive: got %v want NoFileComp", directive)
	}
}

func TestCompleteAgentSlug_BadJSON(t *testing.T) {
	saveCLIState(t)
	flagServer = ""
	flagWorkspace = ""

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/agents", clitest.TextResponse(http.StatusOK, "not json"))

	writeCLIConfig(t, "token: tok\nserver: "+stub.URL()+"\nworkspace: cabcdefghijklmnopqrs\n")

	out, directive := completeAgentSlug(&cobra.Command{}, nil, "")
	if out != nil {
		t.Errorf("suggestions: got %v want nil on decode error", out)
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive: got %v want NoFileComp", directive)
	}
}

func TestCompleteAgentSlug_PrefixFilterAndDescriptions(t *testing.T) {
	saveCLIState(t)
	flagServer = ""
	flagWorkspace = ""

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(http.StatusOK, []map[string]string{
		{"slug": "viktor", "name": "Viktor the Reviewer"},
		{"slug": "vera", "name": ""},
		{"slug": "eva", "name": "Eva"},
		{"slug": "", "name": "ghost without slug"},
	}))

	writeCLIConfig(t, "token: tok\nserver: "+stub.URL()+"\nworkspace: cabcdefghijklmnopqrs\n")

	// Prefix match is case-insensitive; named agents get a tab-separated
	// description, unnamed agents are plain slugs.
	out, directive := completeAgentSlug(&cobra.Command{}, nil, "V")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive: got %v want NoFileComp", directive)
	}
	want := []string{"viktor\tViktor the Reviewer", "vera"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("suggestions: got %v want %v", out, want)
	}

	// Empty prefix → every non-empty slug.
	out, _ = completeAgentSlug(&cobra.Command{}, nil, "")
	if len(out) != 3 {
		t.Errorf("empty prefix: got %v want 3 suggestions", out)
	}
}

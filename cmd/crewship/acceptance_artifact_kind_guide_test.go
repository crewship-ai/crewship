package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/artifacts/kindguide"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// The CLI binary reads the per-kind agent guides from a real router: the list,
// every kind's full text (byte-identical to the embedded registry agents get),
// an alias, and a refusal for a kind without a guide.
func TestAcceptance_ArtifactKindGuideBinary(t *testing.T) {
	copy := testutil.MigratedDB(t)
	path := copy.Path()
	if err := copy.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	db := opened.DB
	const workspace = "ckindguideworkspacexxxxxx"
	const token = "crewship_cli_kind_guide_binary_fixture"
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES('kind-guide-reader','kind-guide-reader@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('ckindguideworkspacexxxxxx','Kind guides','kind-guides')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('kind-guide-member','ckindguideworkspacexxxxxx','kind-guide-reader','MEMBER')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('kind-guide-token','kind-guide-reader','fixture',?,'STANDARD','["pages:read"]')`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "kind-guide-cli-synthetic-secret-0123456789", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	signedIn := write("cli.yaml", fmt.Sprintf("server: %s\ntoken: %s\nworkspace: %s\nformat: json\n", server.URL, token, workspace))
	signedOut := write("anon.yaml", fmt.Sprintf("server: %s\ntoken: crewship_cli_not_a_token\nworkspace: %s\nformat: json\n", server.URL, workspace))
	binary := buildCrewshipBinary(t)
	run := func(config string, args ...string) ([]byte, error) {
		t.Helper()
		command := exec.CommandContext(t.Context(), binary, append([]string{"artifacts", "kind-guide"}, args...)...)
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "CREWSHIP_") {
				command.Env = append(command.Env, value)
			}
		}
		command.Env = append(command.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		out, err := command.Output()
		return out, err
	}

	out, err := run(signedIn)
	if err != nil {
		t.Fatalf("list: %v %s", err, out)
	}
	var list struct {
		Guides []map[string]any `json:"guides"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		t.Fatalf("list output: %v %s", err, out)
	}
	registry := kindguide.List()
	if len(list.Guides) != len(registry) {
		t.Fatalf("CLI listed %d guides, registry has %d", len(list.Guides), len(registry))
	}
	for i, want := range registry {
		if list.Guides[i]["kind"] != want.Kind || list.Guides[i]["version"] != want.Version || list.Guides[i]["sha256"] != want.SHA256 {
			t.Fatalf("listed guide %d = %v, want %s %s", i, list.Guides[i], want.Kind, want.Version)
		}
	}

	for _, want := range registry {
		out, err := run(signedIn, want.Kind)
		if err != nil {
			t.Fatalf("%s: %v %s", want.Kind, err, out)
		}
		var got struct {
			Kind, Version, SHA256, Content string
			Modes                          []string
			Capabilities                   kindguide.Capabilities
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("%s output: %v", want.Kind, err)
		}
		if got.Kind != want.Kind || got.Version != want.Version || got.SHA256 != want.SHA256 || got.Content != want.Content ||
			!slices.Equal(got.Modes, want.Modes) || !slices.Equal(got.Capabilities.Supported, want.Capabilities.Supported) || !slices.Equal(got.Capabilities.Unsupported, want.Capabilities.Unsupported) {
			t.Fatalf("%s: CLI guide differs from the embedded registry", want.Kind)
		}
	}

	out, err = run(signedIn, "design-system")
	if err != nil || !strings.Contains(string(out), `"kind": "library"`) {
		t.Fatalf("alias: %v %s", err, out)
	}

	out, err = run(signedIn, "--format", "table", "docs")
	if err != nil || !strings.Contains(string(out), "## Agent modes") || !strings.Contains(string(out), "Comment is not Propose") {
		t.Fatalf("human guide: %v %s", err, out)
	}

	if out, err := run(signedIn, "word"); err == nil {
		t.Fatalf("Craft kind served a guide: %s", out)
	}
	if out, err := run(signedOut, "docs"); err == nil {
		t.Fatalf("invalid token read a guide: %s", out)
	}
}

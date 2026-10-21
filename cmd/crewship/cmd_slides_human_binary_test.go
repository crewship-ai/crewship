package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/slides"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Actual executable -> human auth -> current SQLite ACL -> fenced Slides Store.
// This is protocol evidence, not model/editor/Present/export acceptance.
func TestSlidesHumanBinary(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const ws = "chumanslidesworkspace00001"
	const token = "crewship_cli_human_slides_synthetic_token"
	for _, q := range []string{
		`INSERT INTO users(id,email) VALUES('slides-human','slides-human@example.invalid')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('chumanslidesworkspace00001','Slides','slides')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('slides-member','chumanslidesworkspace00001','slides-human','OWNER')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,private_audience,spec_json) VALUES('slides-binary-page','chumanslidesworkspace00001','binary-slides','Slides','slides-human',1,'{}')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES('slides-token','slides-human','human',?,'STANDARD')`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, strings.Repeat("slides-binary-secret-", 3), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	router.RegisterSlidesHuman(true)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	binary := buildCrewshipBinary(t)
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err = os.WriteFile(cfg, []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: %s\nformat: json\n", server.URL, token, ws)), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(input string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Stdin = strings.NewReader(input)
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "CREWSHIP_") {
				cmd.Env = append(cmd.Env, v)
			}
		}
		cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+cfg, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		return cmd.CombinedOutput()
	}
	require := func(input string, args ...string) []byte {
		t.Helper()
		out, e := run(input, args...)
		if e != nil {
			t.Fatalf("CLI %v: %v %s", args, e, out)
		}
		return out
	}
	raw, err := os.ReadFile("../../testdata/slides-schema-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Deck slides.Deck `json:"deck"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	fixture.Deck.DeckID = "slides-binary-page"
	body, err := json.Marshal(slides.InitializeRequest{PageID: fixture.Deck.DeckID, SchemaVersion: 1, MutationID: "binary-init", Deck: fixture.Deck})
	if err != nil {
		t.Fatal(err)
	}
	require(string(body), "slide", "initialize", "binary-slides", "--file", "-")
	replay := require(string(body), "slide", "initialize", "@slides-binary-page", "--file", "-")
	var receipt slides.Receipt
	if json.Unmarshal(replay, &receipt) != nil || receipt.Revision != 1 || !receipt.Duplicate {
		t.Fatalf("receipt %s", replay)
	}
	saved := require("", "slide", "read", "@slides-binary-page")
	var state slides.State
	if json.Unmarshal(saved, &state) != nil || state.Revision != 1 || state.Deck.DeckID != fixture.Deck.DeckID {
		t.Fatalf("state %s", saved)
	}
	pinned := require("", "slide", "at-rev", "binary-slides", "1")
	if string(pinned) != string(saved) {
		t.Fatalf("pinned differs %s", pinned)
	}
	if plural := require("", "slides", "read", "@slides-binary-page"); string(plural) != string(saved) {
		t.Fatalf("canonical slides command differs from singular alias: %s", plural)
	}
	hash, _ := slides.SlideHash(state.Deck.Slides[0])
	notes := "Saved through the real executable"
	patch := slides.PatchRequest{PageID: fixture.Deck.DeckID, SchemaVersion: 1, BaseRev: 1, MutationID: "binary-patch", Operations: []slides.Operation{{Op: "set_notes", SlideID: state.Deck.Slides[0].ID, IfHash: hash, Notes: &notes}}}
	patchBody, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	edited := require(string(patchBody), "slides", "patch", "@slides-binary-page", "--file", "-")
	var applied struct {
		Deck     slides.Deck `json:"deck"`
		Rev      int64       `json:"rev"`
		Order    int64       `json:"order_version"`
		Mutation string      `json:"mutation_id"`
	}
	if json.Unmarshal(edited, &applied) != nil || applied.Rev != 2 || applied.Order != 1 || applied.Mutation != "binary-patch" || applied.Deck.Slides[0].Notes != notes {
		t.Fatalf("patch response %s", edited)
	}
	if replay := require(string(patchBody), "slide", "patch", "@slides-binary-page", "--file", "-"); string(replay) != string(edited) {
		t.Fatalf("patch replay %s", replay)
	}
	saved = require("", "slides", "read", "@slides-binary-page")
	if json.Unmarshal(saved, &state) != nil || state.Revision != 2 || state.Deck.Slides[0].Notes != notes {
		t.Fatalf("saved patch %s", saved)
	}
	patch.MutationID = "binary-stale"
	stale, _ := json.Marshal(patch)
	out, conflictErr := run(string(stale), "slides", "patch", "@slides-binary-page", "--file", "-")
	var conflict struct {
		Rev  int64       `json:"rev"`
		Deck slides.Deck `json:"deck"`
	}
	if conflictErr == nil || json.NewDecoder(strings.NewReader(string(out))).Decode(&conflict) != nil || conflict.Rev != 2 || conflict.Deck.DeckID != fixture.Deck.DeckID {
		t.Fatalf("stale current snapshot %v %s", conflictErr, out)
	}
	var revisions int
	if err = db.QueryRow(`SELECT count(*) FROM slides_revisions WHERE page_id='slides-binary-page'`).Scan(&revisions); err != nil || revisions != 2 {
		t.Fatalf("revisions %d %v", revisions, err)
	}
	for _, op := range []string{"restore", "reorder", "history", "export"} {
		if out, e := run("", "slide", op, "binary-slides"); e == nil {
			t.Fatalf("unsupported %s: %s", op, out)
		}
	}
	if _, err = db.Exec(`UPDATE cli_tokens SET revoked_at='2000-01-01T00:00:00Z' WHERE id='slides-token'`); err != nil {
		t.Fatal(err)
	}
	if out, e := run(string(body), "slide", "initialize", "binary-slides", "--file", "-"); e == nil || strings.Contains(string(out), state.ContentHash) {
		t.Fatalf("revoked replay %v %s", e, out)
	}
}

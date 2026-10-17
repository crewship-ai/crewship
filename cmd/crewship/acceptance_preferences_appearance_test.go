package main

// Acceptance for the per-user appearance preferences (#3334): the
// dashboard keeps the legacy `appearance` key (theme/density, written by
// older clients) next to the newer `appearance.style` key (style + knobs).
// The compatibility guarantee is that the two are independent rows of
// user_preferences — writing, overwriting or deleting one never alters the
// other — so an older client that only knows `appearance` cannot clobber a
// style chosen by a newer one.
//
// A real CLI process against the real router on a migrated SQLite, not a
// stub: the values have to survive the server's key and body validation
// (internal/api/user_preferences.go) and come back through
// `preferences list`.

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestAcceptancePreferencesAppearance(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const ws = "cprefsappearancews00"
	ownerToken := "crewship_cli_prefsappearance0000000000000"
	otherToken := "crewship_cli_prefsappearance1111111111111"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('cprefsappearancews00','Prefs','prefs-cli')`,
		`INSERT INTO users(id,email,full_name) VALUES('prefs-cli-owner','prefs-owner@example.invalid','Prefs Owner')`,
		`INSERT INTO users(id,email,full_name) VALUES('prefs-cli-other','prefs-other@example.invalid','Prefs Other')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('prefs-cli-member','cprefsappearancews00','prefs-cli-owner','OWNER')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('prefs-cli-member2','cprefsappearancews00','prefs-cli-other','MEMBER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for id, tok := range map[string][2]string{
		"prefs-cli-token":  {"prefs-cli-owner", ownerToken},
		"prefs-cli-token2": {"prefs-cli-other", otherToken},
	} {
		if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES(?,?,'test',?,datetime('now'))`, id, tok[0], sha256HexToken(tok[1])); err != nil {
			t.Fatal(err)
		}
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()

	writeCfg := func(name, token string) string {
		cfg := filepath.Join(t.TempDir(), name+".yaml")
		if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: "+ws+"\ntoken: "+token+"\nformat: json\n"), 0600); err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	ownerCfg := writeCfg("owner", ownerToken)
	otherCfg := writeCfg("other", otherToken)

	binary := buildCrewshipBinary(t)
	runAs := func(cfg string, args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	run := func(args ...string) (string, error) { return runAs(ownerCfg, args...) }
	must := func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, out)
		}
		return out
	}
	listAs := func(cfg string) map[string]any {
		t.Helper()
		out, err := runAs(cfg, "preferences", "list")
		if err != nil {
			t.Fatalf("preferences list: %v\n%s", err, out)
		}
		got := map[string]any{}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("preferences list is not JSON: %v\n%s", err, out)
		}
		return got
	}
	list := func() map[string]any { return listAs(ownerCfg) }
	decode := func(s string) any {
		t.Helper()
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatalf("bad fixture %s: %v", s, err)
		}
		return v
	}
	expect := func(step string, want map[string]string) {
		t.Helper()
		got := list()
		wantDecoded := make(map[string]any, len(want))
		for k, v := range want {
			wantDecoded[k] = decode(v)
		}
		if !reflect.DeepEqual(got, wantDecoded) {
			t.Fatalf("%s: preferences list = %v, want %v", step, got, wantDecoded)
		}
	}

	if got := list(); len(got) != 0 {
		t.Fatalf("fresh user carries preferences: %v", got)
	}

	const appearanceNew = `{"theme":"dark","density":"compact","style":"harbor","styleVersion":1}`
	const style = `{"v":1,"style":"harbor","knobs":{"radius":"round"}}`
	const appearanceOld = `{"theme":"light","density":"comfortable"}`

	// 1. The legacy key round-trips.
	must("preferences", "set", "appearance", appearanceNew)
	expect("set appearance", map[string]string{"appearance": appearanceNew})

	// 2. Writing appearance.style leaves appearance byte-for-byte alone.
	must("preferences", "set", "appearance.style", style)
	expect("set appearance.style", map[string]string{"appearance": appearanceNew, "appearance.style": style})

	// The table view prints the stored raw JSON literal for both keys.
	table := must("preferences", "list", "--format", "table")
	for _, want := range []string{"appearance.style", `"knobs":{"radius":"round"}`, `"density":"compact"`} {
		if !strings.Contains(table, want) {
			t.Fatalf("table view is missing %q:\n%s", want, table)
		}
	}

	// 3. An older client overwrites appearance with a shape that has no
	// style fields; appearance.style must survive untouched.
	must("preferences", "set", "appearance", appearanceOld)
	expect("older-client overwrite", map[string]string{"appearance": appearanceOld, "appearance.style": style})

	stable := map[string]string{"appearance": appearanceOld, "appearance.style": style}

	// 4. Invalid JSON is refused locally with an example-bearing message,
	// before anything reaches the server.
	if out, err := run("preferences", "set", "appearance.style", `{"v":1,`); err == nil || !strings.Contains(out, "value must be valid JSON") {
		t.Fatalf("invalid JSON was accepted: %v\n%s", err, out)
	}
	// An unquoted string is the common mistake the hint is written for.
	if out, err := run("preferences", "set", "appearance", "dark"); err == nil || !strings.Contains(out, "quote strings") || !strings.Contains(out, "(got: dark)") {
		t.Fatalf("an unquoted string was accepted or the hint is missing: %v\n%s", err, out)
	}
	expect("after invalid JSON", stable)

	// 5. Body size: exactly 16 KB is accepted, one byte more is refused
	// and the server's own message is passed through.
	atLimit := `"` + strings.Repeat("a", 16*1024-2) + `"`
	must("preferences", "set", "size-probe", atLimit)
	if got := list()["size-probe"]; got != strings.Repeat("a", 16*1024-2) {
		s, _ := got.(string)
		t.Fatalf("16 KB value did not round-trip (got %T of len %d)", got, len(s))
	}
	must("preferences", "delete", "size-probe", "--yes")
	overLimit := `{"v":1,"style":"` + strings.Repeat("x", 16*1024) + `"}`
	if out, err := run("preferences", "set", "appearance.style", overLimit); err == nil ||
		!strings.Contains(out, "413") || !strings.Contains(out, "value too large (max 16 KB)") {
		t.Fatalf("oversized value was accepted or the server message was lost: %v\n%s", err, out)
	}
	expect("after oversized value", stable)

	// 6. Keys outside [A-Za-z0-9._-]{1,64} are refused by the server.
	for _, key := range []string{"bad key!", strings.Repeat("k", 65)} {
		if out, err := run("preferences", "set", key, `true`); err == nil || !strings.Contains(out, "invalid key") {
			t.Fatalf("key %q was accepted: %v\n%s", key, err, out)
		}
	}
	if out, err := run("preferences", "delete", "bad key!", "--yes"); err == nil || !strings.Contains(out, "invalid key") {
		t.Fatalf("delete with an invalid key was accepted: %v\n%s", err, out)
	}
	expect("after invalid keys", stable)

	// 7. Another user sees none of these keys, and their own writes do not
	// leak back into the first user's map.
	if got := listAs(otherCfg); len(got) != 0 {
		t.Fatalf("another user sees the first user's preferences: %v", got)
	}
	if out, err := runAs(otherCfg, "preferences", "set", "appearance.style", `{"v":1,"style":"classic"}`); err != nil {
		t.Fatalf("other user set: %v\n%s", err, out)
	}
	if out, err := runAs(otherCfg, "preferences", "delete", "appearance", "--yes"); err != nil {
		t.Fatalf("other user delete: %v\n%s", err, out)
	}
	if got := listAs(otherCfg); !reflect.DeepEqual(got, map[string]any{"appearance.style": decode(`{"v":1,"style":"classic"}`)}) {
		t.Fatalf("other user's own map: %v", got)
	}
	expect("after another user's writes", stable)

	// 8. Deleting appearance.style removes only that key.
	must("preferences", "delete", "appearance.style", "--yes")
	expect("delete appearance.style", map[string]string{"appearance": appearanceOld})
}

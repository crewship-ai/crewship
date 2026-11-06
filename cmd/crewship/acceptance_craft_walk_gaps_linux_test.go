//go:build linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil"
)

const craftGapsWorkspace = "ccraftcliwalkgapsaccept01"
const craftGapsPage = "craft-gaps-page"

// craftGapsFixture is the production Router over TCP with one native Word Page
// holding two versions. The engine is a stub, so only actions that never call
// it are in scope here: read, lease, restore and reconcile.
type craftGapsFixture struct {
	t      *testing.T
	db     *sql.DB
	url    string
	bearer string
	binary string
}

type craftGapsProcess struct {
	t      *testing.T
	input  io.WriteCloser
	rows   chan []byte
	done   chan struct{}
	stderr bytes.Buffer
	code   int
}

func newCraftGapsFixture(t *testing.T) *craftGapsFixture {
	t.Helper()
	copy := testutil.MigratedDB(t)
	path := copy.Path()
	if err := copy.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { opened.Close() })
	db := opened.DB
	const bearer = "crewship_cli_craft_walk_gaps_owner_0000000"
	executable := []byte("#!/bin/sh\nexit 9\n")
	engine := filepath.Join(t.TempDir(), "fixture-wordcraft")
	if err = os.WriteFile(engine, executable, 0700); err != nil {
		t.Fatal(err)
	}
	engineHash := fmt.Sprintf("%x", sha256.Sum256(executable))
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('` + craftGapsWorkspace + `','Craft gaps','craft-gaps')`,
		`INSERT INTO users(id,email,full_name) VALUES('craft-gaps-owner','owner@gaps.invalid','Owner')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('craft-gaps-member','` + craftGapsWorkspace + `','craft-gaps-owner','OWNER')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES('` + craftGapsPage + `','` + craftGapsWorkspace + `','native-gaps','Native gaps','craft-gaps-owner','{}',0)`,
		`INSERT INTO feature_flags(id,key,description,enabled,percentage) VALUES('ff-craft-gaps','` + api.CraftVersionHistoryFlag + `','',1,100)`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES(?,?,'wordcraft',?,?,?)`, craftGapsPage, craftGapsWorkspace, strings.Repeat("a", 40), engineHash, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,scopes,created_at) VALUES('craft-gaps-token','craft-gaps-owner','gaps',?,'["pages:*"]',datetime('now'))`, sha256HexToken(bearer)); err != nil {
		t.Fatal(err)
	}
	store := artifacts.NewVersionStore(db, artifacts.VersionOptions{Enabled: true, ContentType: "wordcraft"})
	for i := range 2 {
		content := []byte(fmt.Sprintf(`{"v":%d}`, i+1))
		if _, err = store.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: craftGapsWorkspace, PageID: craftGapsPage}, artifacts.VersionRequest{BaseVersion: int64(i), EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: content, MediaType: "application/json"}}}, func(context.Context, *sql.Tx) (string, error) { return "user:craft-gaps-owner", nil }); err != nil {
			t.Fatal(err)
		}
	}
	profile := api.CraftNativeDocumentProfile{Application: "wordcraft", EngineRevision: strings.Repeat("a", 40), CLIPath: engine, CLISHA256: engineHash, EditorBundle: strings.Repeat("c", 64), EditorURL: "/_editor/b/" + strings.Repeat("c", 64) + "/index.html"}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithCraftNativeHTTP(api.CraftNativeHTTPConfig{Enabled: true, Profiles: map[string]api.CraftNativeDocumentProfile{"wordcraft": profile}}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return &craftGapsFixture{t: t, db: db, url: server.URL, bearer: bearer, binary: buildCrewshipBinary(t)}
}

// connect starts one `craft connect` process and returns its session ID.
func (f *craftGapsFixture) connect() (*craftGapsProcess, string) {
	t := f.t
	t.Helper()
	home := t.TempDir()
	config := filepath.Join(home, "cli.yaml")
	if err := os.WriteFile(config, []byte("server: "+f.url+"\nworkspace: "+craftGapsWorkspace+"\ntoken: "+f.bearer+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	cmd := exec.CommandContext(ctx, f.binary, "craft", "connect", "@"+craftGapsPage)
	cmd.Env = append(os.Environ(), "HOME="+home, "CREWSHIP_CONFIG="+config, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1", "DATABASE_URL=")
	p := &craftGapsProcess{t: t, rows: make(chan []byte, 16), done: make(chan struct{})}
	var err error
	if p.input, err = cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = &p.stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024), 1<<20)
		for scanner.Scan() {
			p.rows <- append([]byte(nil), scanner.Bytes()...)
		}
		close(p.rows)
		var exit *exec.ExitError
		if err := cmd.Wait(); errors.As(err, &exit) {
			p.code = exit.ExitCode()
		}
		close(p.done)
	}()
	t.Cleanup(func() { p.input.Close(); cancel(); <-p.done })
	first := p.next()
	session, _ := first["session_id"].(string)
	if session == "" {
		t.Fatalf("no public session: %v", first)
	}
	return p, session
}

// next reads one NDJSON row, or nil once the process has ended.
func (p *craftGapsProcess) next() map[string]any {
	p.t.Helper()
	select {
	case raw, ok := <-p.rows:
		if !ok {
			<-p.done
			return nil
		}
		var row map[string]any
		if err := json.Unmarshal(raw, &row); err != nil {
			p.t.Fatalf("stdout row is not JSON: %v", err)
		}
		return row
	case <-time.After(60 * time.Second):
		p.t.Fatal("CLI response deadline")
		return nil
	}
}

func (p *craftGapsProcess) send(action map[string]any) map[string]any {
	p.t.Helper()
	raw, err := json.Marshal(action)
	if err != nil {
		p.t.Fatal(err)
	}
	if _, err = p.input.Write(append(raw, '\n')); err != nil {
		p.t.Fatal(err)
	}
	return p.next()
}

// acquire takes the lease and returns the generation and the bound snapshot.
func (p *craftGapsProcess) acquire(session string) (string, map[string]any) {
	p.t.Helper()
	lease := p.send(map[string]any{"action": "acquire"})
	generation, _ := lease["generation"].(string)
	if generation == "" {
		p.t.Fatalf("no generation: %v (stderr %s)", lease, p.stderr.String())
	}
	return generation, p.snapshot(session, generation)
}

func (p *craftGapsProcess) snapshot(session, generation string) map[string]any {
	p.t.Helper()
	doc := p.send(map[string]any{"action": "read"})
	if doc == nil {
		p.t.Fatalf("read refused: exit %d %s", p.code, p.stderr.String())
	}
	return map[string]any{
		"workspaceId": craftGapsWorkspace, "pageId": craftGapsPage, "applicationId": doc["applicationId"],
		"enginePin": doc["enginePin"], "sessionId": session, "generation": generation, "baseVersion": doc["version"],
	}
}

func (f *craftGapsFixture) versions() int {
	f.t.Helper()
	n := 0
	if err := f.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id=?`, craftGapsPage).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// A missing restore target reads as not found to the lease holder, and stays
// a lease refusal for anyone else: only the holder may probe version numbers.
func TestAcceptance_CraftRestoreMissingVersionIsNotFound(t *testing.T) {
	f := newCraftGapsFixture(t)
	stale, staleSession := f.connect()
	snapshot := stale.snapshot(staleSession, "1")
	snapshot["targetVersion"] = "99"
	if row := stale.send(map[string]any{"action": "restore", "snapshot": snapshot}); row != nil {
		t.Fatalf("restore without the lease answered: %v", row)
	}
	if stale.code == 0 || stale.code == 3 {
		t.Fatalf("restore without the lease: exit %d %s", stale.code, stale.stderr.String())
	}

	holder, session := f.connect()
	_, snapshot = holder.acquire(session)
	snapshot["targetVersion"] = "99"
	if row := holder.send(map[string]any{"action": "restore", "snapshot": snapshot}); row != nil {
		t.Fatalf("restore of a missing version answered: %v", row)
	}
	if holder.code != 3 || !strings.Contains(holder.stderr.String(), "version not found") {
		t.Fatalf("exit %d, stderr %s", holder.code, holder.stderr.String())
	}
	if n := f.versions(); n != 2 {
		t.Fatalf("a refused restore wrote a version: %d", n)
	}
}

// An attempt the server never committed answers 410 under the live lease of the
// session that made it: a definite "never will", exit 1.
func TestAcceptance_CraftReconcileAttemptNeverCommitted(t *testing.T) {
	f := newCraftGapsFixture(t)
	holder, session := f.connect()
	_, snapshot := holder.acquire(session)
	snapshot["editorRevision"] = "revision-never-sent"
	snapshot["contentHash"] = "sha256:" + strings.Repeat("e", 64)
	snapshot["attemptId"] = "attempt-never-sent"
	if row := holder.send(map[string]any{"action": "reconcile", "snapshot": snapshot}); row != nil {
		t.Fatalf("reconcile answered: %v", row)
	}
	if holder.code != 1 || !strings.Contains(holder.stderr.String(), "did not commit") {
		t.Fatalf("exit %d, stderr %s", holder.code, holder.stderr.String())
	}
	if n := f.versions(); n != 2 {
		t.Fatalf("reconcile wrote a version: %d", n)
	}
}

// oneShot runs a CLI command that ends by itself.
func (f *craftGapsFixture) oneShot(args ...string) (string, int) {
	f.t.Helper()
	home := f.t.TempDir()
	config := filepath.Join(home, "cli.yaml")
	if err := os.WriteFile(config, []byte("server: "+f.url+"\nworkspace: "+craftGapsWorkspace+"\ntoken: "+f.bearer+"\n"), 0600); err != nil {
		f.t.Fatal(err)
	}
	cmd := exec.Command(f.binary, args...)
	cmd.Env = append(os.Environ(), "HOME="+home, "CREWSHIP_CONFIG="+config, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1", "DATABASE_URL=")
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return string(out), exit.ExitCode()
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return string(out), 0
}

// The process that made a save is gone. Reconcile from a new process cannot
// confirm the save, because the snapshot binds the session that committed it;
// the documented recovery compares base+1 with the local candidate.
func TestAcceptance_CraftReconcileFromNewProcessIsUnconfirmed(t *testing.T) {
	f := newCraftGapsFixture(t)
	const committed = `{"v":2}`
	original, session := f.connect()
	generation, snapshot := original.acquire(session)
	snapshot["baseVersion"] = "1"
	snapshot["editorRevision"] = "revision-committed"
	snapshot["contentHash"] = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(committed)))

	// The original snapshot replayed as it stands is refused before a request:
	// it names the session of the lost process.
	replay, _ := f.connect()
	if row := replay.send(map[string]any{"action": "reconcile", "snapshot": snapshot}); row != nil {
		t.Fatalf("the replay answered: %v", row)
	}
	if replay.code != 1 || !strings.Contains(replay.stderr.String(), "invalid native snapshot binding") {
		t.Fatalf("replay: exit %d, stderr %s", replay.code, replay.stderr.String())
	}

	for _, attempt := range []string{"", "attempt-from-lost-process"} {
		other, otherSession := f.connect()
		late := map[string]any{}
		for k, v := range snapshot {
			late[k] = v
		}
		late["sessionId"] = otherSession
		if attempt != "" {
			late["attemptId"] = attempt
		}
		if row := other.send(map[string]any{"action": "reconcile", "snapshot": late}); row != nil {
			t.Fatalf("reconcile from a new process confirmed: %v", row)
		}
		if other.code != 5 {
			t.Fatalf("attempt %q: exit %d, want 5\n%s", attempt, other.code, other.stderr.String())
		}
	}

	// The documented recovery proves the commit without any reconcile.
	file := filepath.Join(t.TempDir(), "candidate.json")
	if out, code := f.oneShot("craft", "version", "@"+craftGapsPage, "2", "--output", file); code != 0 {
		t.Fatalf("craft version: exit %d\n%s", code, out)
	}
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != committed {
		t.Fatalf("base+1 bytes %q", got)
	}

	// A different session holding the live lease is told 410 even though the
	// old save did commit: 410 speaks only for that session's own attempts.
	if row := original.send(map[string]any{"action": "release", "generation": generation}); row == nil {
		t.Fatalf("release refused: %s", original.stderr.String())
	}
	next, nextSession := f.connect()
	nextGeneration, _ := next.acquire(nextSession)
	late := map[string]any{}
	for k, v := range snapshot {
		late[k] = v
	}
	late["sessionId"], late["generation"], late["attemptId"] = nextSession, nextGeneration, "attempt-from-lost-process"
	if row := next.send(map[string]any{"action": "reconcile", "snapshot": late}); row != nil {
		t.Fatalf("reconcile from the new holder answered: %v", row)
	}
	if next.code != 1 || !strings.Contains(next.stderr.String(), "did not commit") {
		t.Fatalf("exit %d, stderr %s", next.code, next.stderr.String())
	}
	if out, code := f.oneShot("craft", "version", "@"+craftGapsPage, "2", "--format", "json"); code != 0 {
		t.Fatalf("version 2 must still exist: exit %d\n%s", code, out)
	}
}

// The real binary and Router: `page grant --everyone` opens the shared version
// to a member and nothing newer, `page revoke --everyone` takes it away, and a
// member with Edit who holds the lease cannot move what readers see.
func TestAcceptance_CraftEveryoneReachesTheSharedVersionAndOnlyTheOwnerShares(t *testing.T) {
	f := newCraftGapsFixture(t)
	owner := f.bearer
	const member = "crewship_cli_craft_walk_gaps_member_00000"
	for _, q := range []string{
		`INSERT INTO users(id,email,full_name) VALUES('craft-gaps-mate','mate@gaps.invalid','Mate')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('craft-gaps-mate-m','` + craftGapsWorkspace + `','craft-gaps-mate','MEMBER')`,
	} {
		if _, err := f.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,scopes,created_at) VALUES('craft-gaps-mate-token','craft-gaps-mate','mate',?,'["pages:*"]',datetime('now'))`, sha256HexToken(member)); err != nil {
		t.Fatal(err)
	}
	as := func(token string, args ...string) (string, int) {
		t.Helper()
		f.bearer = token
		return f.oneShot(args...)
	}
	pin := func() (n int) {
		t.Helper()
		if err := f.db.QueryRow(`SELECT shared_version FROM artifact_version_heads WHERE page_id=?`, craftGapsPage).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// The owner shares version 2; version 3 then exists and is not shared.
	f.bearer = owner
	holder, session := f.connect()
	generation, snapshot := holder.acquire(session)
	snapshot["targetVersion"] = "2"
	if row := holder.send(map[string]any{"action": "share", "snapshot": snapshot}); row == nil || pin() != 2 {
		t.Fatalf("owner share: %v exit %d %s", row, holder.code, holder.stderr.String())
	}
	if row := holder.send(map[string]any{"action": "release", "generation": generation}); row == nil {
		t.Fatalf("release: exit %d %s", holder.code, holder.stderr.String())
	}
	store := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "wordcraft"})
	if _, err := store.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: craftGapsWorkspace, PageID: craftGapsPage}, artifacts.VersionRequest{BaseVersion: 2, EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: []byte(`{"v":"unshared"}`), MediaType: "application/json"}}}, func(context.Context, *sql.Tx) (string, error) { return "user:craft-gaps-owner", nil }); err != nil {
		t.Fatal(err)
	}
	unshared := base64.StdEncoding.EncodeToString([]byte(`{"v":"unshared"}`))
	preview := []string{"craft", "preview", "@" + craftGapsPage}
	if out, code := as(member, preview...); code != 4 {
		t.Fatalf("preview before the grant: exit %d %s", code, out)
	}
	if out, code := as(owner, "page", "grant", "native-gaps", "--everyone", "--level", "read"); code != 0 {
		t.Fatalf("grant: exit %d %s", code, out)
	}
	out, code := as(member, preview...)
	if code != 0 || !strings.Contains(out, base64.StdEncoding.EncodeToString([]byte(`{"v":2}`))) || strings.Contains(out, unshared) {
		t.Fatalf("preview after the grant: exit %d %s", code, out)
	}
	if out, code := as(member, "craft", "version", "@"+craftGapsPage, "3"); code != 4 || strings.Contains(out, unshared) {
		t.Fatalf("unshared version after the grant: exit %d %s", code, out)
	}
	if out, code := as(owner, "page", "revoke", "native-gaps", "--everyone"); code != 0 {
		t.Fatalf("revoke: exit %d %s", code, out)
	}
	if out, code := as(member, preview...); code != 4 {
		t.Fatalf("preview after removal: exit %d %s", code, out)
	}

	if out, code := as(owner, "page", "grant", "native-gaps", "--everyone", "--level", "write"); code != 0 {
		t.Fatalf("edit grant: exit %d %s", code, out)
	}
	f.bearer = member
	editor, session := f.connect()
	_, snapshot = editor.acquire(session)
	snapshot["targetVersion"] = "3"
	if row := editor.send(map[string]any{"action": "share", "snapshot": snapshot}); row != nil {
		t.Fatalf("editor share answered: %v", row)
	}
	if editor.code != 4 || !strings.Contains(editor.stderr.String(), "only the page owner") || pin() != 2 {
		t.Fatalf("editor share: exit %d, pin %d, stderr %s", editor.code, pin(), editor.stderr.String())
	}
}

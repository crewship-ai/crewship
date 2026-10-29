//go:build linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Real CLI processes talk to the production Router over TCP. SQL seeds only an
// explicit native Page's metadata and human credentials, as in the API fixture;
// this is session/lease transport acceptance, not engine or document qualification.
func TestAcceptance_CraftLeaseTwoHumans(t *testing.T) {
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
	const workspace = "ccraftclileaseaccept00001"
	const ownerBearer = "crewship_cli_craft_accept_owner_0000000000"
	const otherBearer = "crewship_cli_craft_accept_other_0000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('` + workspace + `','Craft CLI acceptance','craft-cli')`,
		`INSERT INTO users(id,email,full_name) VALUES('craft-cli-pavel','pavel@craft.invalid','Pavel'),('craft-cli-jana','jana@craft.invalid','Jana')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('craft-cli-member-a','` + workspace + `','craft-cli-pavel','OWNER'),('craft-cli-member-b','` + workspace + `','craft-cli-jana','MEMBER')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES('craft-cli-page','` + workspace + `','native','Native CLI','craft-cli-pavel','{}',0)`,
		`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('craft-cli-page','user','craft-cli-jana','write','craft-cli-pavel')`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES('craft-cli-page',?,'wordcraft',?,?,?)`, workspace, strings.Repeat("a", 40), strings.Repeat("b", 64), strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct{ id, token string }{{"craft-cli-pavel", ownerBearer}, {"craft-cli-jana", otherBearer}} {
		if _, err = db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,scopes,created_at) VALUES(?,?,?,?,'["pages:*"]',datetime('now'))`, u.id+"-token", u.id, "native acceptance", sha256HexToken(u.token)); err != nil {
			t.Fatal(err)
		}
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithCraftNativeHTTP(api.CraftNativeHTTPConfig{Enabled: true}))
	if err != nil {
		t.Fatal(err)
	}
	defer router.Shutdown()
	var mu sync.Mutex
	secrets := map[string]bool{}
	calls := map[string]int{}
	// Observe headers without replacing any production authentication or handler.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls[r.Method+" "+r.URL.Path]++
		if token := r.Header.Get("X-Craft-Session"); token != "" {
			secrets[token] = true
		}
		mu.Unlock()
		router.ServeHTTP(w, r)
	}))
	defer server.Close()
	binary := buildCrewshipBinary(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	type process struct {
		input   io.WriteCloser
		rows    chan []byte
		done    chan struct{}
		stderr  bytes.Buffer
		output  bytes.Buffer
		err     error
		scanErr error
		home    string
	}
	start := func(bearer string) *process {
		t.Helper()
		p := &process{rows: make(chan []byte, 16), done: make(chan struct{}), home: t.TempDir()}
		config := filepath.Join(p.home, "cli.yaml")
		if err := os.WriteFile(config, []byte("server: "+server.URL+"\nworkspace: "+workspace+"\ntoken: "+bearer+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, binary, "craft", "connect", "@craft-cli-page")
		cmd.Env = append(os.Environ(), "HOME="+p.home, "CREWSHIP_CONFIG="+config, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1", "DATABASE_URL=")
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
			scanner.Buffer(make([]byte, 1024), 64<<10)
			for scanner.Scan() {
				line := append([]byte(nil), scanner.Bytes()...)
				p.output.Write(line)
				p.output.WriteByte('\n')
				p.rows <- line
			}
			p.scanErr = scanner.Err()
			close(p.rows)
			p.err = cmd.Wait()
			close(p.done)
		}()
		t.Cleanup(func() { p.input.Close(); cancel(); <-p.done })
		return p
	}
	next := func(p *process) map[string]any {
		t.Helper()
		select {
		case raw, ok := <-p.rows:
			if !ok {
				<-p.done
				diagnostic := p.stderr.String()
				mu.Lock()
				for secret := range secrets {
					diagnostic = strings.ReplaceAll(diagnostic, secret, "[redacted]")
				}
				mu.Unlock()
				diagnostic = strings.ReplaceAll(strings.ReplaceAll(diagnostic, ownerBearer, "[redacted]"), otherBearer, "[redacted]")
				t.Fatalf("real CLI ended before expected NDJSON row: %v; %s", p.err, diagnostic)
			}
			var value map[string]any
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatalf("CLI stdout is not one JSON object per line: %v", err)
			}
			if _, exists := value["session_token"]; exists {
				t.Fatal("private session token printed on stdout")
			}
			return value
		case <-ctx.Done():
			t.Fatal("real CLI NDJSON response deadline")
			return nil
		}
	}
	action := func(p *process, a map[string]string) {
		t.Helper()
		raw, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = p.input.Write(append(raw, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	owner, other := start(ownerBearer), start(otherBearer)
	a := next(owner)
	b := next(other)
	ownerID, _ := a["session_id"].(string)
	otherID, _ := b["session_id"].(string)
	if ownerID == "" || otherID == "" || ownerID == otherID {
		t.Fatal("CLI did not emit two distinct public session IDs")
	}
	action(owner, map[string]string{"action": "acquire"})
	first := next(owner)
	generation, ok := first["generation"].(string)
	if !ok || generation == "" {
		t.Fatal("acquisition generation must remain a decimal string")
	}
	if n, err := strconv.ParseInt(generation, 10, 64); err != nil || n < 1 || strconv.FormatInt(n, 10) != generation {
		t.Fatal("noncanonical generation")
	}
	action(other, map[string]string{"action": "status"})
	observer := next(other)
	holder, ok := observer["holder"].(map[string]any)
	if !ok || holder["display_name"] != "Pavel" || holder["kind"] != "human" || observer["mine"] != false || observer["can_write"] != false {
		t.Fatal("second human did not see Pavel as the named exclusive holder")
	}
	action(other, map[string]string{"action": "request-handoff"})
	request := next(other)
	requestID, _ := request["request_id"].(string)
	if requestID == "" {
		t.Fatal("real request-handoff did not return an opaque request ID")
	}
	action(owner, map[string]string{"action": "status"})
	view := next(owner)
	requests, ok := view["handoff_requested_by"].([]any)
	if !ok || len(requests) != 1 {
		t.Fatal("owner did not receive the second human's handoff request")
	}
	target, ok := requests[0].(map[string]any)
	if !ok || target["display_name"] != "Jana" || target["request_id"] != requestID {
		t.Fatal("handoff request identity did not match Jana")
	}
	action(owner, map[string]string{"action": "handoff", "generation": generation, "request_id": requestID})
	transferred := next(owner)
	newGeneration, ok := transferred["generation"].(string)
	if !ok || newGeneration == generation || transferred["mine"] != false {
		t.Fatal("handoff did not advance custody away from the old holder")
	}
	action(other, map[string]string{"action": "renew", "generation": newGeneration})
	renewed := next(other)
	if renewed["mine"] != true || renewed["can_write"] != true || renewed["generation"] != newGeneration {
		t.Fatal("new human holder could not renew the transferred lease")
	}
	action(owner, map[string]string{"action": "renew", "generation": generation})
	owner.input.Close()
	<-owner.done
	if owner.err == nil || !strings.Contains(owner.stderr.String(), "native HTTP 409") {
		t.Fatal("old generation was not rejected by the real server and CLI")
	}
	other.input.Close()
	<-other.done
	if other.err != nil || other.scanErr != nil || owner.scanErr != nil {
		t.Fatal("real CLI transport failed")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(secrets) != 2 {
		t.Fatal("expected two private process-local session capabilities in HTTP headers")
	}
	const base = "/api/v1/pages/@craft-cli-page/craft/"
	// Escaping the @ in the CLI URL is decoded by net/http before Path matching.
	if calls["POST "+base+"sessions"] != 2 || calls["POST "+base+"lease/request-handoff"] != 1 || calls["POST "+base+"lease/handoff"] != 1 || calls["POST "+base+"lease/renew"] != 2 {
		t.Fatal("real CLI did not execute each expected session/handoff action exactly once")
	}
	for secret := range secrets {
		if len(secret) != 64 || secret == ownerID || secret == otherID {
			t.Fatal("invalid private/public admission separation")
		}
		for _, p := range []*process{owner, other} {
			if bytes.Contains(p.output.Bytes(), []byte(secret)) || bytes.Contains(p.stderr.Bytes(), []byte(secret)) {
				t.Fatal("private capability leaked to CLI output")
			}
			if err := filepath.WalkDir(p.home, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return nil
				}
				content, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if bytes.Contains(content, []byte(secret)) {
					t.Error("process-local native session capability persisted to disk")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	var actor, session string
	var finalGeneration int64
	if err = db.QueryRow(`SELECT actor_id,session_id,generation FROM mooring_edit_leases WHERE page_id='craft-cli-page'`).Scan(&actor, &session, &finalGeneration); err != nil {
		t.Fatal(err)
	}
	if actor != "user:craft-cli-jana" || session != otherID || strconv.FormatInt(finalGeneration, 10) != newGeneration {
		t.Fatal("rejected old-generation command changed the transferred durable lease")
	}
}

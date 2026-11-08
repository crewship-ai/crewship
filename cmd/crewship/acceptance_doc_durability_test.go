//go:build linux && (docs_foundation || conformance)

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// TestAcceptance_DocumentDurabilityRealEngine (#5130) drives the actual CLI
// binary through the production Router and Go SQLite authority into the real
// Node docs-service, which the test spawns, kills, pauses and replaces.
//
// The only test harness between the processes is a pass-through front socket
// (the supervisor's routing to the current engine) and a pass-through
// authority socket per engine that can drop or hold one reply. Neither
// changes a request body. Run with:
//
//	TEST_DOCUMENT_DURABILITY=1 go test -tags docs_foundation ./cmd/crewship \
//	  -run '^TestAcceptance_DocumentDurabilityRealEngine$' -count=1
//
// TEST_DOCUMENT_DURABILITY_EVIDENCE names the measurements JSON file.
func TestAcceptance_DocumentDurabilityRealEngine(t *testing.T) {
	if os.Getenv("TEST_DOCUMENT_DURABILITY") != "1" {
		t.Fatal("requires explicit Node24 plus frozen tools/docs-service installation opt-in")
	}
	nodeVersion, err := exec.Command("node", "--version").Output()
	if err != nil || !strings.HasPrefix(string(nodeVersion), "v24.") {
		t.Fatalf("Node24 required: %s %v", nodeVersion, err)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	serviceScript := filepath.Join(root, "tools/docs-service/service.mjs")

	db := testutil.MigratedDB(t).DB
	const workspace = "cdurabilitydocworkspace001"
	tokens := map[string]string{"one": "crewship_cli_durability_client_one_fixture", "two": "crewship_cli_durability_client_two_fixture"}
	for _, q := range []string{
		`INSERT INTO users(id,email) VALUES('durable-doc-owner','durable-doc@example.invalid')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('cdurabilitydocworkspace001','Durable docs','durable-docs')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('durable-doc-member','cdurabilitydocworkspace001','durable-doc-owner','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for name, token := range tokens {
		if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier) VALUES(?,'durable-doc-owner',?,?,'STANDARD')`, "durable-"+name, name, sha256HexToken(token)); err != nil {
			t.Fatal(err)
		}
	}
	// sockaddr_un is limited to 108 bytes; keep the private paths short.
	dir, err := os.MkdirTemp("", "doc-durable-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	authoritySocket, frontSocket, engineSocket := filepath.Join(dir, "a.sock"), filepath.Join(dir, "f.sock"), filepath.Join(dir, "e.sock")
	secret := strings.Repeat("durability-private-secret-", 2)
	secretFile := filepath.Join(dir, "secret")
	if err := os.WriteFile(secretFile, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	audit := journal.NewWriter(db, slog.Default(), journal.WriterOptions{ChainKey: bytes.Repeat([]byte{9}, 32)})
	t.Cleanup(func() { _ = audit.Close() })
	router, err := api.NewRouter(db, strings.Repeat("durability-jwt-secret-", 2), slog.Default(), api.WithJournal(audit), api.WithDocumentAuthority(authoritySocket, secretFile), api.WithDocumentService(frontSocket))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	front := newDurabilityFront()
	stopFront, err := documents.ServePrivateUnix(frontSocket, front)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stopFront(context.Background()) })

	// Client hop: the Router commits, then the connection to the CLI drops
	// before any response byte is written (scenario 1a).
	var dropNextPatch atomic.Bool
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/document/patch") || !dropNextPatch.CompareAndSwap(true, false) {
			router.ServeHTTP(w, r)
			return
		}
		committed := httptest.NewRecorder()
		router.ServeHTTP(committed, r)
		if committed.Code != http.StatusOK {
			t.Errorf("injected lost ack must follow a real commit: HTTP%d %s", committed.Code, committed.Body)
		}
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = connection.Close()
	}))
	t.Cleanup(host.Close)

	engines := map[string]*durabilityEngine{}
	t.Cleanup(func() {
		for _, engine := range engines {
			engine.kill()
		}
	})
	spawn := func(name, socket string) (*durabilityEngine, time.Duration) {
		t.Helper()
		proxy := newDurabilityAuthorityProxy(authoritySocket)
		proxySocket := filepath.Join(dir, name+".p.sock")
		stopProxy, err := documents.ServePrivateUnix(proxySocket, proxy)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { proxy.release(); _ = stopProxy(context.Background()) })
		logPath := filepath.Join(dir, name+".log")
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = logFile.Close() })
		cmd := exec.Command("node", serviceScript)
		cmd.Env = append(os.Environ(), "CREWSHIP_DOCS_SOCKET="+socket, "CREWSHIP_DOCS_AUTHORITY_SOCKET="+proxySocket, "CREWSHIP_DOCS_SERVICE_TOKEN_FILE="+secretFile)
		cmd.Stdout, cmd.Stderr = logFile, logFile
		started := time.Now()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		engine := &durabilityEngine{name: name, socket: socket, cmd: cmd, proxy: proxy, log: logPath, exited: make(chan struct{})}
		go func() { _ = cmd.Wait(); close(engine.exited) }()
		engines[name] = engine
		if err := engine.waitReady(secret, 20*time.Second); err != nil {
			logged, _ := os.ReadFile(logPath)
			t.Fatalf("engine %s did not become ready: %v\n%s", name, err, logged)
		}
		return engine, time.Since(started)
	}

	binary := buildCrewshipBinary(t)
	config := func(token string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "cli.yaml")
		if err := os.WriteFile(path, []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: %s\nformat: json\n", host.URL, token, workspace)), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	clients := map[string]string{"one": config(tokens["one"]), "two": config(tokens["two"])}
	run := func(client, input string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Dir = dir
		cmd.Stdin = strings.NewReader(input)
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "CREWSHIP_") {
				cmd.Env = append(cmd.Env, v)
			}
		}
		cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+clients[client], "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		return cmd.CombinedOutput()
	}
	require := func(client, input string, args ...string) []byte {
		t.Helper()
		out, err := run(client, input, args...)
		if err != nil {
			t.Fatalf("CLI %s %s: %v\n%s", client, strings.Join(args, " "), err, out)
		}
		return out
	}

	engineA, firstReady := spawn("a", engineSocket)
	front.route(engineSocket)

	type docRef struct {
		slug, id string
		blocks   int
	}
	createDoc := func(slug, prefix string, blocks int) docRef {
		t.Helper()
		created := require("one", strings.Replace(emptyDocPage, "human-document", slug, 1), "page", "create", "--allow-empty", "--file", "-")
		var page struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(created, &page) != nil || page.ID == "" {
			t.Fatalf("Page create response: %s", created)
		}
		var content []map[string]any
		for i := range blocks {
			content = append(content, map[string]any{"type": "paragraph", "attrs": map[string]any{"id": fmt.Sprintf("%s%02d", prefix, i)}, "content": []any{map[string]any{"type": "text", "text": fmt.Sprintf("Initial paragraph %02d with enough confirmed text to give the measured document a realistic body.", i)}}})
		}
		initialize, _ := json.Marshal(map[string]any{"id": slug + "-init", "schema": documents.LatestSchema, "page_id": page.ID, "body": map[string]any{"type": "doc", "content": content}})
		require("one", string(initialize), "doc", "initialize", slug, "--file", "-")
		return docRef{slug, page.ID, blocks}
	}
	const blocks = 24
	primary := createDoc("durable-document", "p", blocks)
	// Client two's in-flight work in scenario 2 targets a second document so
	// the engine admits it concurrently rather than queueing it behind client
	// one's commit; the authority proxy observes that admission explicitly.
	second := createDoc("durable-second", "q", 4)

	type outlineBlock struct{ ID, Hash, Text string }
	type state struct {
		Rev     int64          `json:"rev"`
		Epoch   string         `json:"epoch"`
		Outline []outlineBlock `json:"outline"`
	}
	readDoc := func(client string, d docRef) state {
		t.Helper()
		raw := require(client, "", "doc", "read", d.slug)
		var s state
		if err := json.Unmarshal(raw, &s); err != nil || s.Epoch == "" || len(s.Outline) != d.blocks {
			t.Fatalf("read: %v %s", err, raw)
		}
		return s
	}
	read := func(client string) state { t.Helper(); return readDoc(client, primary) }
	blockOf := func(s state, id string) outlineBlock {
		t.Helper()
		for _, block := range s.Outline {
			if block.ID == id {
				return block
			}
		}
		t.Fatalf("block %s missing", id)
		return outlineBlock{}
	}
	patchDoc := func(d docRef, s state, id, target, text string) string {
		raw, _ := json.Marshal(map[string]any{"id": id, "schema": documents.LatestSchema, "page_id": d.id, "epoch": s.Epoch, "ifRev": s.Rev, "target": target, "ifHash": blockOf(s, target).Hash, "text": text})
		return string(raw)
	}
	patchFor := func(s state, id, target, text string) string { return patchDoc(primary, s, id, target, text) }
	type receipt struct {
		ID        string `json:"id"`
		Rev       int64  `json:"rev"`
		Epoch     string `json:"epoch"`
		Duplicate bool   `json:"duplicate"`
	}
	decodeReceipt := func(raw []byte) receipt {
		t.Helper()
		var r receipt
		if err := json.Unmarshal(raw, &r); err != nil || r.ID == "" {
			t.Fatalf("receipt: %v %s", err, raw)
		}
		return r
	}
	countsDoc := func(d docRef) (head, updates, receipts int64) {
		t.Helper()
		if err := db.QueryRow(`SELECT head_rev FROM documents WHERE workspace_id=? AND page_id=?`, workspace, d.id).Scan(&head); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM document_updates WHERE workspace_id=? AND page_id=?`, workspace, d.id).Scan(&updates); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM document_receipts WHERE workspace_id=? AND page_id=?`, workspace, d.id).Scan(&receipts); err != nil {
			t.Fatal(err)
		}
		return
	}
	counts := func() (int64, int64, int64) { t.Helper(); return countsDoc(primary) }
	hasReceipt := func(id string) bool {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM document_receipts WHERE workspace_id=? AND page_id=? AND id=?`, workspace, primary.id, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	occurrences := func(s state, text string) int {
		n := 0
		for _, block := range s.Outline {
			n += strings.Count(block.Text, text)
		}
		return n
	}
	// confirmed holds every text the server acknowledged with a receipt.
	confirmed := map[string]string{}
	expectConfirmed := func(s state) {
		t.Helper()
		for target, text := range confirmed {
			if got := blockOf(s, target).Text; got != text {
				t.Fatalf("confirmed edit lost: block %s = %q, want %q", target, got, text)
			}
		}
	}
	commitNew := func(client, id, target, text string) receipt {
		t.Helper()
		r := decodeReceipt(require(client, patchFor(read(client), id, target, text), "doc", "patch", primary.slug, "--file", "-"))
		if r.ID != id || r.Duplicate {
			t.Fatalf("new commit receipt: %+v", r)
		}
		confirmed[target] = text
		return r
	}

	// ── Measured burst: two CLI clients alternate confirmed edits. ──
	const burst = 20
	burstStart := time.Now()
	for i := range burst {
		client := []string{"one", "two"}[i%2]
		commitNew(client, fmt.Sprintf("burst-%02d", i), fmt.Sprintf("p%02d", i%blocks), fmt.Sprintf("Burst edit %02d confirmed by client %s.", i, client))
	}
	burstSeconds := time.Since(burstStart).Seconds()
	rssBurst, hwmBurst := engineA.memory()

	// ── Scenario 1a: lost ack between Router and CLI, retry same key. ──
	func() {
		t.Log("scenario: lost-ack-client-hop")
		s := read("one")
		request := patchFor(s, "lost-ack-client", "p20", "Lost client acknowledgement, applied exactly once.")
		headBefore, updatesBefore, receiptsBefore := counts()
		dropNextPatch.Store(true)
		if out, err := run("one", request, "doc", "patch", primary.slug, "--file", "-"); err == nil {
			t.Fatalf("first attempt must lose its acknowledgement: %s", out)
		}
		if head, updates, receipts := counts(); head != headBefore+1 || updates != updatesBefore+1 || receipts != receiptsBefore+1 {
			t.Fatalf("lost ack must follow one durable commit: head %d→%d updates %d→%d receipts %d→%d", headBefore, head, updatesBefore, updates, receiptsBefore, receipts)
		}
		retry := decodeReceipt(require("one", request, "doc", "patch", primary.slug, "--file", "-"))
		again := decodeReceipt(require("one", request, "doc", "patch", primary.slug, "--file", "-"))
		if !retry.Duplicate || retry.Rev != headBefore+1 || again.Rev != retry.Rev || again.Epoch != retry.Epoch {
			t.Fatalf("retry must return the original receipt: %+v %+v", retry, again)
		}
		if head, updates, receipts := counts(); head != headBefore+1 || updates != updatesBefore+1 || receipts != receiptsBefore+1 {
			t.Fatal("exact retries created another effect")
		}
		confirmed["p20"] = "Lost client acknowledgement, applied exactly once."
		if after := read("two"); occurrences(after, "Lost client acknowledgement") != 1 {
			t.Fatalf("duplicate content: %+v", after.Outline)
		}
	}()

	// ── Scenario 1b: Go commits, the reply to the engine is lost. ──
	func() {
		t.Log("scenario: lost-ack-engine-hop")
		s := read("two")
		request := patchFor(s, "lost-ack-engine", "p21", "Lost engine acknowledgement, applied exactly once.")
		headBefore, updatesBefore, _ := counts()
		engineA.proxy.arm("commit", proxyDropReply)
		out, err := run("two", request, "doc", "patch", primary.slug, "--file", "-")
		if err == nil || !bytes.Contains(out, []byte("503")) {
			t.Fatalf("dropped authority reply must stay unacknowledged: %v %s", err, out)
		}
		if head, updates, _ := counts(); head != headBefore+1 || updates != updatesBefore+1 {
			t.Fatal("the authority did not commit before the dropped reply")
		}
		retry := decodeReceipt(require("two", request, "doc", "patch", primary.slug, "--file", "-"))
		if !retry.Duplicate || retry.Rev != headBefore+1 {
			t.Fatalf("retry after lost engine reply: %+v", retry)
		}
		if head, updates, _ := counts(); head != headBefore+1 || updates != updatesBefore+1 {
			t.Fatal("retry after a lost engine reply created another effect")
		}
		confirmed["p21"] = "Lost engine acknowledgement, applied exactly once."
	}()

	// ── Scenario 2: kill the engine while two clients have work in flight:
	// client one's commit is durable but unacknowledged, and client two's
	// commit was admitted by the engine but never reached the authority. ──
	var engineB *durabilityEngine
	var recoverReady, recoverRead time.Duration
	var rssBeforeKill, hwmBeforeKill, rssReplacement, hwmReplacement int64
	func() {
		t.Log("scenario: engine-crash-mid-session")
		s := read("one")
		commitNew2 := func(id, target, text string) {
			t.Helper()
			r := decodeReceipt(require("two", patchDoc(second, readDoc("two", second), id, target, text), "doc", "patch", second.slug, "--file", "-"))
			if r.ID != id || r.Duplicate {
				t.Fatalf("second document receipt: %+v", r)
			}
		}
		commitNew2("second-confirmed", "q00", "Confirmed on the second document before the crash.")
		s2 := readDoc("two", second)
		committedRequest := patchFor(s, "crash-committed", "p22", "Committed just before the engine crashed.")
		uncommittedRequest := patchDoc(second, s2, "crash-uncommitted", "q01", "Never committed: the engine crashed first.")
		headBefore, updatesBefore, _ := counts()
		head2Before, updates2Before, _ := countsDoc(second)
		type result struct {
			out []byte
			err error
		}
		first, other := make(chan result, 1), make(chan result, 1)
		committedHeld := engineA.proxy.arm("commit", proxyHoldAfterForward)
		go func() {
			out, err := run("one", committedRequest, "doc", "patch", primary.slug, "--file", "-")
			first <- result{out, err}
		}()
		select {
		case <-committedHeld:
		case <-time.After(20 * time.Second):
			t.Fatal("client one's commit never reached the authority")
		}
		admittedHeld := engineA.proxy.arm("commit", proxyHoldBeforeForward)
		go func() {
			out, err := run("two", uncommittedRequest, "doc", "patch", second.slug, "--file", "-")
			other <- result{out, err}
		}()
		select {
		case <-admittedHeld:
		case <-time.After(20 * time.Second):
			t.Fatal("client two's commit was never sent by the engine")
		}
		if held := engineA.proxy.captured(); held == nil || held.field("id") != "crash-uncommitted" {
			t.Fatal("the engine-side barrier did not observe client two's commit")
		}
		rssBeforeKill, hwmBeforeKill = engineA.memory()
		engineA.kill()
		for name, ch := range map[string]chan result{"one": first, "two": other} {
			select {
			case r := <-ch:
				if r.err == nil || !bytes.Contains(r.out, []byte("503")) {
					t.Fatalf("client %s must see an unacknowledged 503 after the crash: %v %s", name, r.err, r.out)
				}
			case <-time.After(30 * time.Second):
				t.Fatalf("client %s hung after the engine crash", name)
			}
		}
		if head, updates, _ := counts(); head != headBefore+1 || updates != updatesBefore+1 || !hasReceipt("crash-committed") {
			t.Fatalf("crash state: head %d→%d updates %d→%d", headBefore, head, updatesBefore, updates)
		}
		if head, updates, _ := countsDoc(second); head != head2Before || updates != updates2Before {
			t.Fatal("client two's unconfirmed commit reached durable state")
		}
		// While no engine runs, a retained draft is refused, never acknowledged.
		if out, err := run("two", uncommittedRequest, "doc", "patch", second.slug, "--file", "-"); err == nil || !bytes.Contains(out, []byte("503")) {
			t.Fatalf("send during outage: %v %s", err, out)
		}
		// The supervisor restarts the engine at its configured socket path.
		// SIGKILL left that path behind.
		if _, err := os.Lstat(engineSocket); err != nil {
			t.Fatalf("crashed engine socket path: %v", err)
		}
		restarted := time.Now()
		engineB, recoverReady = spawn("b", engineSocket)
		after := read("two")
		recoverRead = time.Since(restarted)
		if after.Epoch == s.Epoch {
			t.Fatal("replacement engine must present a new epoch")
		}
		confirmed["p22"] = "Committed just before the engine crashed."
		expectConfirmed(after)
		after2 := readDoc("two", second)
		if blockOf(after2, "q00").Text != "Confirmed on the second document before the crash." || occurrences(after2, "Never committed") != 0 {
			t.Fatalf("second document after restart: %+v", after2.Outline)
		}
		// Client one retries its unacknowledged request after the restart and
		// receives the original receipt; nothing is applied twice.
		retry := decodeReceipt(require("one", committedRequest, "doc", "patch", primary.slug, "--file", "-"))
		if !retry.Duplicate || retry.Rev != headBefore+1 {
			t.Fatalf("committed request retry after restart: %+v", retry)
		}
		if head, updates, _ := counts(); head != headBefore+1 || updates != updatesBefore+1 {
			t.Fatal("committed request retry after restart created another effect")
		}
		// Client two's retained draft carries the crashed engine's epoch. It is
		// refused with no effect and must be offered for recovery.
		if out, err := run("two", uncommittedRequest, "doc", "patch", second.slug, "--file", "-"); err == nil || !bytes.Contains(out, []byte("409")) {
			t.Fatalf("stale retained draft after restart: %v %s", err, out)
		}
		if head, updates, receipts := countsDoc(second); head != head2Before || updates != updates2Before || receipts != head2Before+1 {
			t.Fatalf("stale retained draft changed the second document: head %d updates %d receipts %d", head, updates, receipts)
		}
		// Recovering the draft against the new epoch saves it exactly once.
		commitNew2("crash-recovered", "q01", "Never committed: the engine crashed first.")
		if final := readDoc("one", second); occurrences(final, "Never committed") != 1 {
			t.Fatal("recovered draft must appear exactly once")
		}
	}()
	if engineB == nil {
		t.FailNow()
	}

	// ── Scenario 3: a replacement engine fences the previous boot, including
	// when the paused old process resumes and receives traffic again. ──
	func() {
		t.Log("scenario: stale-boot-epoch-generation")
		commitNew("one", "fence-before", "p00", "Committed by engine b before it was paused.")
		oldLease := engineB.proxy.last("lease")
		oldCommit := engineB.proxy.last("commit")
		if oldLease == nil || oldCommit == nil {
			t.Fatal("engine b lease and commit were not observed")
		}
		stale := read("two")
		staleRequest := patchFor(stale, "fence-stale", "p01", "Must never persist from a fenced boot.")
		rssReplacement, hwmReplacement = engineB.memory()
		engineB.signal(syscall.SIGSTOP)
		// A paused engine still owns its socket: a second engine at that path
		// must refuse before recovering, so it cannot fence engine b.
		boots := func() string {
			var boot string
			if err := db.QueryRow(`SELECT boot_id FROM document_writer_fence WHERE id=1`).Scan(&boot); err != nil {
				t.Fatal(err)
			}
			return boot
		}
		bootBefore := boots()
		// Bounded: a regression that steals the socket keeps running.
		intruderCtx, cancelIntruder := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancelIntruder()
		intruder := exec.CommandContext(intruderCtx, "node", serviceScript)
		intruder.Env = append(os.Environ(), "CREWSHIP_DOCS_SOCKET="+engineB.socket, "CREWSHIP_DOCS_AUTHORITY_SOCKET="+authoritySocket, "CREWSHIP_DOCS_SERVICE_TOKEN_FILE="+secretFile)
		if out, err := intruder.CombinedOutput(); err == nil || !bytes.Contains(out, []byte("service socket already exists")) {
			t.Fatalf("second engine at a paused engine's socket: %v %s", err, out)
		}
		if info, err := os.Lstat(engineB.socket); err != nil || info.Mode()&os.ModeSocket == 0 || boots() != bootBefore {
			t.Fatalf("paused engine lost custody: %v", err)
		}
		engineC, _ := spawn("c", filepath.Join(dir, "c.sock"))
		front.route(engineC.socket)
		head, updates, receipts := counts()
		refusedWithoutEffect := func(label string, out []byte, err error) {
			t.Helper()
			if err == nil || !bytes.Contains(out, []byte("409")) {
				t.Fatalf("%s must be refused with 409: %v %s", label, err, out)
			}
			if h, u, r := counts(); h != head || u != updates || r != receipts {
				t.Fatalf("%s changed durable state", label)
			}
		}
		out, err := run("two", staleRequest, "doc", "patch", primary.slug, "--file", "-")
		refusedWithoutEffect("old epoch through the replacement engine", out, err)

		// The paused engine resumes and the supervisor routes to it again.
		engineB.signal(syscall.SIGCONT)
		front.route(engineB.socket)
		out, err = run("two", staleRequest, "doc", "patch", primary.slug, "--file", "-")
		refusedWithoutEffect("old epoch through the resumed old engine", out, err)

		// Capture an otherwise valid commit from engine c, unforwarded. Each
		// probe below changes only one fence field of that exact request.
		front.route(engineC.socket)
		freshText := "Committed by engine c after fencing engine b."
		freshRequest := patchFor(read("one"), "fence-fresh", "p02", freshText)
		capturedSignal := engineC.proxy.arm("commit", proxyCaptureDrop)
		if out, err := run("one", freshRequest, "doc", "patch", primary.slug, "--file", "-"); err == nil || !bytes.Contains(out, []byte("503")) {
			t.Fatalf("captured commit must stay unacknowledged: %v %s", err, out)
		}
		select {
		case <-capturedSignal:
		case <-time.After(20 * time.Second):
			t.Fatal("engine c never sent the fresh commit")
		}
		valid, currentLease := engineC.proxy.captured(), engineC.proxy.last("lease")
		if valid == nil || currentLease == nil || valid.field("id") != "fence-fresh" {
			t.Fatal("engine c's fresh commit and lease were not observed")
		}
		if h, u, r := counts(); h != head || u != updates || r != receipts {
			t.Fatal("captured commit reached durable state")
		}
		send := func(op string, body map[string]any) (int, []byte) { return engineC.proxy.direct(valid, op, body) }
		variant := func(mutate func(map[string]any)) map[string]any {
			body := map[string]any{}
			if err := json.Unmarshal(valid.body, &body); err != nil {
				t.Fatal(err)
			}
			mutate(body)
			return body
		}
		refused := func(label string, status int, raw []byte) {
			t.Helper()
			if status != http.StatusConflict {
				t.Fatalf("%s: HTTP%d %s", label, status, raw)
			}
			if h, u, r := counts(); h != head || u != updates || r != receipts {
				t.Fatalf("%s changed durable state", label)
			}
		}
		for _, probe := range []struct {
			label  string
			mutate func(map[string]any)
		}{
			{"lease, owner and epoch of fenced engine b", func(body map[string]any) {
				body["leaseToken"], body["leaseOwner"], body["epoch"] = oldLease.response["token"], oldLease.response["owner"], oldLease.response["epoch"]
			}},
			{"lease and owner of fenced engine b at the current epoch", func(body map[string]any) {
				body["leaseToken"], body["leaseOwner"] = oldLease.response["token"], oldLease.response["owner"]
			}},
			{"old epoch", func(body map[string]any) { body["epoch"] = oldLease.response["epoch"] }},
			{"old generation", func(body map[string]any) { body["generation"] = oldCommit.field("generation") }},
		} {
			status, raw := send("commit", variant(probe.mutate))
			refused(probe.label, status, raw)
		}
		oldLeaseBody := map[string]any{"owner": oldLease.response["owner"], "token": oldLease.response["token"], "epoch": oldLease.response["epoch"]}
		for _, op := range []string{"renew", "release"} {
			status, raw := send(op, oldLeaseBody)
			refused("fenced engine b "+op, status, raw)
		}
		// Positive controls: the current lease renews, and the unchanged
		// captured request commits exactly once.
		if status, raw := send("renew", map[string]any{"owner": currentLease.response["owner"], "token": currentLease.response["token"], "epoch": currentLease.response["epoch"]}); status != http.StatusOK {
			t.Fatalf("current lease renew control: HTTP%d %s", status, raw)
		}
		if status, raw := send("commit", variant(func(map[string]any) {})); status != http.StatusOK {
			t.Fatalf("unchanged captured commit control: HTTP%d %s", status, raw)
		}
		if h, u, r := counts(); h != head+1 || u != updates+1 || r != receipts+1 {
			t.Fatal("positive control did not commit exactly once")
		}
		retry := decodeReceipt(require("one", freshRequest, "doc", "patch", primary.slug, "--file", "-"))
		if !retry.Duplicate || retry.Rev != head+1 {
			t.Fatalf("CLI retry of the control commit: %+v", retry)
		}
		confirmed["p02"] = freshText
		final := read("one")
		expectConfirmed(final)
		if occurrences(final, "Must never persist") != 0 {
			t.Fatal("fenced write persisted")
		}
		engineB.kill()
	}()

	head, updates, receipts := counts()
	var snapshotBytes, logBytes int64
	if err := db.QueryRow(`SELECT length(snapshot) FROM documents WHERE workspace_id=? AND page_id=?`, workspace, primary.id).Scan(&snapshotBytes); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COALESCE(SUM(length(payload)),0) FROM document_updates WHERE workspace_id=? AND page_id=?`, workspace, primary.id).Scan(&logBytes); err != nil {
		t.Fatal(err)
	}
	measurements := map[string]any{
		"issue":                        5130,
		"node_version":                 strings.TrimSpace(string(nodeVersion)),
		"go_version":                   runtime.Version(),
		"clients":                      len(clients),
		"document_blocks":              blocks,
		"document_head_rev":            head,
		"document_snapshot_bytes":      snapshotBytes,
		"document_update_log_bytes":    logBytes,
		"document_update_rows":         updates,
		"document_receipts":            receipts,
		"burst_confirmed_updates":      burst,
		"burst_seconds":                burstSeconds,
		"burst_updates_per_second":     float64(burst) / burstSeconds,
		"burst_note":                   "sequential CLI read+patch per update, each a separate binary invocation",
		"engine_rss_kib_after_burst":   rssBurst,
		"engine_hwm_kib_after_burst":   hwmBurst,
		"engine_rss_kib_before_kill":   rssBeforeKill,
		"engine_hwm_kib_before_kill":   hwmBeforeKill,
		"engine_first_start_ready_ms":  firstReady.Milliseconds(),
		"recover_spawn_to_ready_ms":    recoverReady.Milliseconds(),
		"recover_spawn_to_cli_read_ms": recoverRead.Milliseconds(),
	}
	measurements["engine_rss_kib_replacement"], measurements["engine_hwm_kib_replacement"] = rssReplacement, hwmReplacement
	raw, _ := json.MarshalIndent(measurements, "", "  ")
	evidence := os.Getenv("TEST_DOCUMENT_DURABILITY_EVIDENCE")
	if evidence == "" {
		evidence = filepath.Join(t.TempDir(), "durability-measurements.json")
	}
	if err := os.WriteFile(evidence, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("durability measurements (%s):\n%s", evidence, raw)
}

// durabilityFront is the supervisor's routing: it forwards each Router
// connection unchanged to the current engine socket. A dead engine closes the
// connection, so the Router reports its normal unacknowledged 503.
type durabilityFront struct {
	current atomic.Value
	patches atomic.Int64
}

func newDurabilityFront() *durabilityFront     { return &durabilityFront{} }
func (f *durabilityFront) route(socket string) { f.current.Store(socket) }
func (f *durabilityFront) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		durabilityDrop(w)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/patch") {
		f.patches.Add(1)
	}
	socket, _ := f.current.Load().(string)
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(r.Context(), "unix", socket)
	if err != nil {
		durabilityDrop(w)
		return
	}
	defer conn.Close()
	out, err := http.NewRequestWithContext(r.Context(), r.Method, "http://docs"+r.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		durabilityDrop(w)
		return
	}
	out.Header = r.Header.Clone()
	out.Close = true
	if err := out.Write(conn); err != nil {
		durabilityDrop(w)
		return
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), out)
	if err != nil {
		durabilityDrop(w)
		return
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		durabilityDrop(w)
		return
	}
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(data)
}

func durabilityDrop(w http.ResponseWriter) {
	if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
		_ = conn.Close()
	}
}

type proxyMode int

const (
	proxyForward proxyMode = iota
	proxyDropReply
	proxyHoldAfterForward
	// proxyHoldBeforeForward never forwards: the engine waits until it dies.
	proxyHoldBeforeForward
	// proxyCaptureDrop records a valid request and drops it unforwarded.
	proxyCaptureDrop
)

type durabilityExchange struct {
	op       string
	path     string
	header   http.Header
	body     []byte
	response map[string]any
}

func (e *durabilityExchange) field(name string) any {
	var body map[string]any
	_ = json.Unmarshal(e.body, &body)
	return body[name]
}

// durabilityAuthorityProxy sits between one engine and the Go authority. It
// forwards bytes unchanged, records exchanges, and can drop or hold one reply.
type durabilityAuthorityProxy struct {
	client  *http.Client
	mu      sync.Mutex
	armedOp string
	mode    proxyMode
	signal  chan struct{}
	hold    chan struct{}
	seen    map[string]*durabilityExchange
	held    *durabilityExchange
	once    sync.Once
}

func newDurabilityAuthorityProxy(authority string) *durabilityAuthorityProxy {
	return &durabilityAuthorityProxy{hold: make(chan struct{}), seen: map[string]*durabilityExchange{}, client: &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", authority)
	}}}}
}
func (p *durabilityAuthorityProxy) arm(op string, mode proxyMode) <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.armedOp, p.mode, p.signal = op, mode, make(chan struct{})
	return p.signal
}
func (p *durabilityAuthorityProxy) captured() *durabilityExchange {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.held
}
func (p *durabilityAuthorityProxy) release() { p.once.Do(func() { close(p.hold) }) }
func (p *durabilityAuthorityProxy) last(op string) *durabilityExchange {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen[op]
}

// direct sends a request to the authority with the engine's original headers.
func (p *durabilityAuthorityProxy) direct(template *durabilityExchange, op string, body map[string]any) (int, []byte) {
	raw, _ := json.Marshal(body)
	path := template.path[:strings.LastIndex(template.path, "/")+1] + op
	req, _ := http.NewRequest("POST", "http://authority"+path, bytes.NewReader(raw))
	req.Header = template.header.Clone()
	req.Header.Del("Content-Length")
	response, err := p.client.Do(req)
	if err != nil {
		return 0, []byte(err.Error())
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return response.StatusCode, data
}
func (p *durabilityAuthorityProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		durabilityDrop(w)
		return
	}
	op := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	p.mu.Lock()
	mode, signal := proxyForward, p.signal
	if p.armedOp == op {
		mode = p.mode
		p.armedOp = ""
	}
	p.mu.Unlock()
	if mode == proxyHoldBeforeForward || mode == proxyCaptureDrop {
		p.mu.Lock()
		p.held = &durabilityExchange{op: op, path: r.URL.Path, header: r.Header.Clone(), body: body}
		p.mu.Unlock()
		close(signal)
		if mode == proxyHoldBeforeForward {
			select {
			case <-r.Context().Done():
			case <-p.hold:
			}
		}
		durabilityDrop(w)
		return
	}
	req, _ := http.NewRequestWithContext(context.WithoutCancel(r.Context()), r.Method, "http://authority"+r.URL.RequestURI(), bytes.NewReader(body))
	req.Header = r.Header.Clone()
	response, err := p.client.Do(req)
	if err != nil {
		durabilityDrop(w)
		return
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	exchange := &durabilityExchange{op: op, path: r.URL.Path, header: r.Header.Clone(), body: body}
	_ = json.Unmarshal(data, &exchange.response)
	if response.StatusCode < 400 {
		p.mu.Lock()
		p.seen[op] = exchange
		p.mu.Unlock()
	}
	switch mode {
	case proxyDropReply:
		close(signal)
		durabilityDrop(w)
		return
	case proxyHoldAfterForward:
		close(signal)
		// The engine is killed while it waits for this committed reply.
		select {
		case <-r.Context().Done():
		case <-p.hold:
		}
		durabilityDrop(w)
		return
	}
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(data)
}

type durabilityEngine struct {
	name   string
	socket string
	cmd    *exec.Cmd
	proxy  *durabilityAuthorityProxy
	log    string
	exited chan struct{}
}

func (e *durabilityEngine) waitReady(secret string, timeout time.Duration) error {
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", e.socket)
	}}}
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-e.exited:
			return errors.New("engine process exited")
		default:
		}
		req, _ := http.NewRequest("GET", "http://docs/health/ready", nil)
		req.Header.Set("X-Crewship-Docs-Service", secret)
		if response, err := client.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("readiness timeout")
}
func (e *durabilityEngine) signal(sig syscall.Signal) {
	if e.cmd.Process != nil {
		_ = e.cmd.Process.Signal(sig)
	}
}
func (e *durabilityEngine) kill() {
	select {
	case <-e.exited:
		return
	default:
	}
	_ = e.cmd.Process.Signal(syscall.SIGCONT)
	_ = e.cmd.Process.Kill()
	<-e.exited
}

// memory returns VmRSS and VmHWM in KiB from /proc, or zeros once exited.
func (e *durabilityEngine) memory() (rss, hwm int64) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", e.cmd.Process.Pid))
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, _ := strconv.ParseInt(fields[1], 10, 64)
		switch fields[0] {
		case "VmRSS:":
			rss = value
		case "VmHWM:":
			hwm = value
		}
	}
	return
}

//go:build linux

package restricteddispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func nativeFixture(t *testing.T) Authority {
	t.Helper()
	a := providerFixture(t)
	if _, e := a.Store.DB.ExecContext(t.Context(), `UPDATE agents SET llm_model='gpt-5-mini',restricted_execution_profile='native_api_key' WHERE id='a'`); e != nil {
		t.Fatal(e)
	}
	return a
}

func nativeRaw(t *testing.T, history ...json.RawMessage) []byte {
	t.Helper()
	input := []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":[{"type":"input_text","text":"ambient"}]}`)}
	input = append(input, history...)
	raw, e := json.Marshal(map[string]any{"model": "gpt-5-mini", "instructions": "ambient", "input": input, "tools": []any{}, "tool_choice": "auto", "parallel_tool_calls": true, "reasoning": map[string]any{"summary": "auto"}, "include": []string{"reasoning.encrypted_content"}, "store": false, "stream": true})
	if e != nil {
		t.Fatal(e)
	}
	return raw
}

func prepareNativeFixture(t *testing.T, a Authority, user, chat string) (string, access.Attempt) {
	t.Helper()
	h, attempt, e := a.PrepareNative(t.Context(), user, "w", "a", chat, "", nil, 128, func(context.Context, access.Attempt) (NativePrompt, error) {
		return NativePrompt{Instructions: "frozen " + user, Input: "input " + user}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	return h, attempt
}

func TestNativeFrozenContextAndCrashLease(t *testing.T) {
	a := nativeFixture(t)
	h, attempt := prepareNativeFixture(t, a, "h1", "c1")
	plan, e := a.Resolve(t.Context(), h)
	if e != nil || plan.Network.Grants[0].Native == nil || plan.Network.Grants[0].Responses != nil || plan.NativeSandbox == "" {
		t.Fatalf("missing separate native profile: %+v %v", plan, e)
	}
	if _, e = a.Store.DB.ExecContext(t.Context(), `UPDATE restricted_native_sessions SET instructions='foreign' WHERE attempt_id=?`, attempt.ID); e == nil {
		t.Fatal("rewrote frozen instructions")
	}
	body, ticket, e := a.NativeRequest(t.Context(), h, "key", nativeRaw(t))
	if e != nil || ticket == "" {
		t.Fatal(e)
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil || payload["instructions"] != "frozen h1" {
		t.Fatalf("unfrozen request: %s", body)
	}
	// A reconstructed authority cannot reset the outstanding request after a crash.
	a = Authority{Store: a.Store}
	if _, _, e = a.NativeRequest(t.Context(), h, "key", nativeRaw(t)); e == nil {
		t.Fatal("recycled unknown in-flight request")
	}
	if e = a.NativeComplete(t.Context(), h, "foreign-ticket", []json.RawMessage{json.RawMessage(`{"type":"message","id":"msg_own"}`)}); e == nil {
		t.Fatal("settled foreign lease")
	}
	setRights(t, a, "h1", nil)
	if e = a.NativeComplete(t.Context(), h, ticket, []json.RawMessage{json.RawMessage(`{"type":"message","id":"msg_own"}`)}); e == nil {
		t.Fatal("revoked runtime recorded issued state")
	}
}

func TestNativeRequestSerializesAcrossConnectionsAndBindsOutput(t *testing.T) {
	a := nativeFixture(t)
	h1, _ := prepareNativeFixture(t, a, "h1", "c1")
	h2, _ := prepareNativeFixture(t, a, "h2", "c2")
	var seq int
	var name, path string
	rows, e := a.Store.DB.QueryContext(t.Context(), "PRAGMA database_list")
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		if e = rows.Scan(&seq, &name, &path); e != nil {
			t.Fatal(e)
		}
		if name == "main" {
			break
		}
	}
	rows.Close()
	otherDB, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	defer otherDB.Close()
	otherDB.SetMaxOpenConns(1)
	if _, e = otherDB.ExecContext(t.Context(), "PRAGMA busy_timeout=5000"); e != nil {
		t.Fatal(e)
	}
	other := Authority{Store: access.Store{DB: otherDB}}
	raw := nativeRaw(t)
	start := make(chan struct{})
	results := make(chan string, 2)
	var wg sync.WaitGroup
	for _, authority := range []Authority{a, other} {
		wg.Add(1)
		go func(authority Authority) {
			defer wg.Done()
			<-start
			_, ticket, e := authority.NativeRequest(t.Context(), h1, "key", raw)
			if e != nil {
				ticket = ""
			}
			results <- ticket
		}(authority)
	}
	close(start)
	wg.Wait()
	close(results)
	var ticket string
	wins := 0
	for value := range results {
		if value != "" {
			wins++
			ticket = value
		}
	}
	if wins != 1 {
		t.Fatalf("concurrent history forks: %d", wins)
	}
	call := json.RawMessage(`{"type":"function_call","id":"fc_h1","call_id":"call_h1","name":"exec_command","arguments":"{\"cmd\":\"true\"}"}`)
	if e = a.NativeComplete(t.Context(), h2, ticket, []json.RawMessage{call}); e == nil {
		t.Fatal("other human completed lease")
	}
	if e = a.NativeComplete(t.Context(), h1, ticket, []json.RawMessage{call}); e != nil {
		t.Fatal(e)
	}
	if e = a.NativeComplete(t.Context(), h1, ticket, []json.RawMessage{call}); e == nil {
		t.Fatal("duplicate completion")
	}
	output := json.RawMessage(`{"type":"function_call_output","call_id":"call_h1","output":"own result"}`)
	if _, _, e = other.NativeRequest(t.Context(), h1, "key", nativeRaw(t, call, output)); e != nil {
		t.Fatal("durable issued provenance lost", e)
	}
	if _, _, e = other.NativeRequest(t.Context(), h2, "key", nativeRaw(t, call, output)); e == nil {
		t.Fatal("foreign human reused opaque state")
	}
}

func TestNativeRequiresExplicitProfileAndOperation(t *testing.T) {
	a := providerFixture(t)
	called := false
	if _, _, e := a.PrepareNative(t.Context(), "h1", "w", "a", "c1", "", nil, 128, func(context.Context, access.Attempt) (NativePrompt, error) {
		called = true
		return NativePrompt{Input: "own"}, nil
	}); e == nil || called {
		t.Fatal("disabled native adapter built prompt")
	}
	a = nativeFixture(t)
	setRights(t, a, "h1", []access.Right{{Kind: "agent", ID: "a", Operation: "chat"}})
	if _, _, e := a.PrepareNative(t.Context(), "h1", "w", "a", "c1", "", nil, 128, func(context.Context, access.Attempt) (NativePrompt, error) { return NativePrompt{Input: "own"}, nil }); e == nil {
		t.Fatal("chat right launched run")
	}
	h, attempt, e := a.PrepareChatNative(t.Context(), "h1", "w", "a", "c1", "", nil, 128, func(context.Context, access.Attempt) (NativePrompt, error) { return NativePrompt{Input: "own"}, nil })
	if e != nil || attempt.AdmissionOperation != "chat" {
		t.Fatal(e)
	}
	if _, e = a.Resolve(t.Context(), h); e != nil {
		t.Fatal(e)
	}
}

func TestNativeRunnerIngestsClassifiedArtifactsBeforeCompletion(t *testing.T) {
	a := nativeFixture(t)
	setRights(t, a, "h1", []access.Right{{Kind: "agent", ID: "a", Operation: "run"}, {Kind: "agent", ID: "a", Operation: "chat"}})
	var handle string
	r := NativeRunner{Authority: a, MaxOutputTokens: 128}
	r.StartSession = func(ctx context.Context, h string) (TextSession, error) {
		handle = h
		done := make(chan struct{})
		close(done)
		return &textFixtureSession{output: "{\"type\":\"text\",\"text\":\"own answer\"}\n{\"type\":\"artifact\",\"name\":\"result.txt\",\"content\":\"b3duIGZpbGU=\"}\n{\"type\":\"done\"}\n", done: done}, nil
	}
	if err := r.Execute(t.Context(), "h1", "w", "c1", "own question", func(string, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	var principal, scope, content string
	if err := a.Store.DB.QueryRowContext(t.Context(), `SELECT principal_id,scope,content FROM access_files WHERE name='result.txt'`).Scan(&principal, &scope, &content); err != nil || principal != "h1" || content != "own file" {
		t.Fatalf("classified file=%q %q %q %v", principal, scope, content, err)
	}
	if _, err := a.Store.Resolve(t.Context(), handle); err == nil {
		t.Fatal("completed attempt still executable")
	}
	// Artifact failure must never mark a turn complete or ingest host paths.
	r.StartSession = func(context.Context, string) (TextSession, error) {
		done := make(chan struct{})
		close(done)
		return &textFixtureSession{output: "{\"type\":\"artifact\",\"name\":\"../foreign\",\"content\":\"eA==\"}\n{\"type\":\"done\"}\n", done: done}, nil
	}
	if err := r.Execute(t.Context(), "h1", "w", "c1", "next", func(string, string) error { return nil }); err == nil {
		t.Fatal("foreign artifact admitted")
	}
}

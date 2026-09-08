package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/groupchat"
	"github.com/crewship-ai/crewship/internal/groupchatnotify"
	"github.com/crewship-ai/crewship/internal/testutil"
)

type conversationLoadMetric struct {
	Requests int     `json:"requests"`
	P50MS    float64 `json:"p50_ms"`
	P95MS    float64 `json:"p95_ms"`
	P99MS    float64 `json:"p99_ms"`
	MaxMS    float64 `json:"max_ms"`
}
type conversationLoadMeasurements struct {
	mu        sync.Mutex
	durations map[string][]time.Duration
	failures  []string
}

func (m *conversationLoadMeasurements) record(kind string, start time.Time, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.durations[kind] = append(m.durations[kind], time.Since(start))
	if err != nil {
		m.failures = append(m.failures, err.Error())
	}
}
func (m *conversationLoadMeasurements) failed() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.failures)
}
func (m *conversationLoadMeasurements) summary() map[string]conversationLoadMetric {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]conversationLoadMetric{}
	for name, ds := range m.durations {
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		n := len(ds)
		if n == 0 {
			continue
		}
		ms := func(i int) float64 { return float64(ds[i]) / float64(time.Millisecond) }
		out[name] = conversationLoadMetric{n, ms((n - 1) * 50 / 100), ms((n - 1) * 95 / 100), ms((n - 1) * 99 / 100), ms(n - 1)}
	}
	return out
}

// Acceptance, not a benchmark/SLA: exercise real authenticated HTTP on
// an isolated migrated SQLite WAL file, with the production five-connection pool.
// go test ./internal/api -run '^TestWorkspaceConversationsHTTP100Active$' -count=1 -v -timeout=5m
func TestWorkspaceConversationsHTTP100Active(t *testing.T) {
	const users = 100
	const rounds = 2
	started := time.Now()
	metrics := &conversationLoadMeasurements{durations: map[string][]time.Duration{}}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	db := testutil.MigratedSQLDB(t)
	var journal string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil || journal != "wal" {
		t.Fatalf("journal=%q err=%v", journal, err)
	}
	if db.Stats().MaxOpenConnections != 5 {
		t.Fatalf("pool=%d, expected production5", db.Stats().MaxOpenConnections)
	}
	defer func() {
		metrics.mu.Lock()
		failureExamples := append([]string{}, metrics.failures[:min(10, len(metrics.failures))]...)
		metrics.mu.Unlock()
		report := map[string]any{"scenario": "100 authenticated active humans; 10 private groups of 10; one 100-member channel", "at_utc": started.UTC().Format(time.RFC3339), "go_version": runtime.Version(), "logical_cpus": runtime.NumCPU(), "sqlite_journal": journal, "pool_max_open": db.Stats().MaxOpenConnections, "elapsed_seconds": time.Since(started).Seconds(), "unique_messages_expected": users * rounds * 2, "identical_retries_expected": users * rounds * 2, "failures": metrics.failed(), "failure_examples": failureExamples, "test_failed": t.Failed(), "operations": metrics.summary(), "scope": "Loopback HTTP, production JWT/session+workspace middleware and conversation handlers, SQLite and inbox projection. No browser, websocket delivery, TLS/reverse proxy, model/container execution or mixed production workloads; no SLA guarantee."}
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Error(err)
			return
		}
		t.Logf("LOAD_ACCEPTANCE_REPORT\n%s", encoded)
		if output := os.Getenv("CREWSHIP_CHAT_LOAD_REPORT"); output != "" {
			if err := os.WriteFile(output, append(encoded, '\n'), 0600); err != nil {
				t.Errorf("write load report: %v", err)
			}
		}
	}()
	const workspace = "chat-load-workspace"
	if _, err := db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES(?,'Load acceptance','chat-load'),('chat-load-other','Other','chat-load-other')`, workspace); err != nil {
		t.Fatal(err)
	}
	validator, err := auth.NewJWTValidator("isolated-chat-load-secret-not-production-2026")
	if err != nil {
		t.Fatal(err)
	}
	sessionStore := sessions.NewDBStore(db)
	tokens := make([]string, users)
	ids := make([]string, users)
	for i := 0; i < users; i++ {
		id := fmt.Sprintf("chat-load-user-%03d", i)
		ids[i] = id
		email := id + "@example.invalid"
		if _, err := db.Exec(`INSERT INTO users(id,email,full_name) VALUES(?,?,?)`, id, email, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, "wm-"+id, workspace, id); err != nil {
			t.Fatal(err)
		}
		session, err := sessionStore.Create(ctx, id, "load acceptance", "127.0.0.1", auth.RefreshTokenTTL)
		if err != nil {
			t.Fatal(err)
		}
		tokens[i], err = validator.IssueAccessToken(id, session.ID, id, email)
		if err != nil {
			t.Fatal(err)
		}
	}
	store := groupchat.New(db)
	handler := NewWorkspaceConversationsHandler(store, newTestLogger())
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/conversations", handler.Create)
	mux.HandleFunc("GET /api/v1/conversations/{conversationId}", handler.Get)
	mux.HandleFunc("GET /api/v1/conversations/{conversationId}/messages", handler.Messages)
	mux.HandleFunc("POST /api/v1/conversations/{conversationId}/messages", handler.Send)
	mux.HandleFunc("POST /api/v1/conversations/{conversationId}/read", handler.Read)
	mux.HandleFunc("GET /api/v1/conversations/{conversationId}/participants", handler.Members)
	middleware := NewAuthMiddleware(validator, sessionStore, db, newTestLogger())
	server := httptest.NewServer(middleware.RequireAuth(middleware.RequireWorkspace(mux)))
	defer server.Close()
	transport := &http.Transport{MaxIdleConns: 200, MaxIdleConnsPerHost: 200, MaxConnsPerHost: 200}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 60 * time.Second}
	request := func(kind, method, path, token, ws string, body any, status int, out any) (requestErr error) {
		start := time.Now()
		defer func() { metrics.record(kind, start, requestErr) }()
		var payload []byte
		if body != nil {
			var err error
			payload, err = json.Marshal(body)
			if err != nil {
				return err
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("X-Workspace-ID", ws)
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("%s transport: %w", kind, err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		if err != nil {
			return err
		}
		if response.StatusCode != status {
			return fmt.Errorf("%s expected%d got%d: %s", kind, status, response.StatusCode, data)
		}
		if out != nil {
			if err = json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("%s decode: %w", kind, err)
			}
		}
		return nil
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(request("auth_control", "POST", "/api/v1/conversations", "", workspace, groupchat.CreateInput{Title: "denied", Kind: "channel"}, 401, nil))
	must(request("auth_control", "POST", "/api/v1/conversations", tokens[0], "chat-load-other", groupchat.CreateInput{Title: "denied", Kind: "channel"}, 403, nil))
	var channel groupchat.Conversation
	must(request("create", "POST", "/api/v1/conversations", tokens[0], workspace, groupchat.CreateInput{Title: "100 humans", Kind: "channel"}, 201, &channel))
	groups := make([]groupchat.Conversation, 10)
	for g := range groups {
		must(request("create", "POST", "/api/v1/conversations", tokens[g*10], workspace, groupchat.CreateInput{Title: fmt.Sprintf("Private group %d", g), Kind: "group", MemberIDs: ids[g*10+1 : g*10+10]}, 201, &groups[g]))
	}
	base := func(id string) string { return "/api/v1/conversations/" + id }
	must(request("acl_control", "GET", base(groups[0].ID)+"/messages", tokens[10], workspace, nil, 404, nil))
	for g := range groups {
		var roster struct {
			Participants []groupchat.Member `json:"participants"`
		}
		must(request("roster", "GET", base(groups[g].ID)+"/participants", tokens[g*10], workspace, nil, 200, &roster))
		if len(roster.Participants) != 10 {
			t.Fatalf("group%d roster=%d", g, len(roster.Participants))
		}
	}
	var roster struct {
		Participants []groupchat.Member `json:"participants"`
	}
	must(request("roster", "GET", base(channel.ID)+"/participants", tokens[0], workspace, nil, 200, &roster))
	if len(roster.Participants) != 100 {
		t.Fatalf("channel roster=%d", len(roster.Participants))
	}
	dispatcher := groupchatnotify.New(db, nil, newTestLogger())
	projectionCtx, stopProjection := context.WithCancel(ctx)
	projectionDone := make(chan struct{})
	projectionErrors := make(chan error, 1)
	go func() {
		defer close(projectionDone)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			if err := dispatcher.Drain(projectionCtx); err != nil {
				if projectionCtx.Err() == nil {
					projectionErrors <- err
				}
				return
			}
			select {
			case <-projectionCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	defer func() { stopProjection(); <-projectionDone }()
	runHumans := func(fn func(int) error) {
		var wg sync.WaitGroup
		barrier := make(chan struct{})
		for i := 0; i < users; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-barrier
				if err := fn(i); err != nil {
					metrics.record("acceptance_failure", time.Now(), err)
				}
			}(i)
		}
		close(barrier)
		wg.Wait()
		if metrics.failed() > 0 {
			t.Fatalf("load recorded%d failures; first=%s", metrics.failed(), metrics.failures[0])
		}
	}
	runHumans(func(i int) error {
		for _, conv := range []string{channel.ID, groups[i/10].ID} {
			for n := 0; n < rounds; n++ {
				input := groupchat.SendInput{ClientID: fmt.Sprintf("user-%d-message-%d", i, n), Content: fmt.Sprintf("Human%d message%d, deterministic acceptance payload", i, n)}
				var sent, retry groupchat.Message
				if err := request("send", "POST", base(conv)+"/messages", tokens[i], workspace, input, 201, &sent); err != nil {
					return err
				}
				if err := request("identical_retry", "POST", base(conv)+"/messages", tokens[i], workspace, input, 200, &retry); err != nil {
					return err
				}
				if sent.ID != retry.ID || sent.Sequence != retry.Sequence || sent.AuthorUserID != ids[i] || sent.Content != input.Content {
					return fmt.Errorf("user%d message identity/content mismatch", i)
				}
			}
		}
		return nil
	})
	stopProjection()
	<-projectionDone
	select {
	case err := <-projectionErrors:
		t.Fatal(err)
	default:
	}
	drainAll := func() {
		for {
			pending, err := store.PendingEvents(ctx, 1)
			must(err)
			if len(pending) == 0 {
				return
			}
			before := time.Now()
			err = dispatcher.Drain(ctx)
			metrics.record("outbox_batch", before, err)
			must(err)
		}
	}
	drainAll()
	var count int
	must(db.QueryRow(`SELECT COUNT(*) FROM workspace_conversation_messages`).Scan(&count))
	if count != 400 {
		t.Fatalf("messages=%d expected400", count)
	}
	must(db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE kind='message' AND state='unread'`).Scan(&count))
	if count != 200 {
		t.Fatalf("inbox aggregates=%d expected200", count)
	}
	runHumans(func(i int) error {
		for _, room := range []struct {
			id    string
			total int64
			page  int
		}{{channel.ID, 200, 37}, {groups[i/10].ID, 20, 7}} {
			var after int64
			seen := map[string]bool{}
			for {
				var page struct {
					Messages []groupchat.Message `json:"messages"`
					HasMore  bool                `json:"has_more"`
				}
				path := fmt.Sprintf("%s/messages?after_sequence=%d&limit=%d", base(room.id), after, room.page)
				if err := request("history_page", "GET", path, tokens[i], workspace, nil, 200, &page); err != nil {
					return err
				}
				for _, m := range page.Messages {
					if m.Sequence != after+1 || seen[m.ID] {
						return fmt.Errorf("user%d missing/duplicate message at%d", i, after)
					}
					seen[m.ID] = true
					after = m.Sequence
				}
				if !page.HasMore {
					break
				}
			}
			if after != room.total {
				return fmt.Errorf("user%d history total%d expected%d", i, after, room.total)
			}
			if err := request("mark_read", "POST", base(room.id)+"/read", tokens[i], workspace, map[string]int64{"last_read_sequence": after}, 204, nil); err != nil {
				return err
			}
			if err := request("stale_read", "POST", base(room.id)+"/read", tokens[i], workspace, map[string]int64{"last_read_sequence": 0}, 204, nil); err != nil {
				return err
			}
			var conv groupchat.Conversation
			if err := request("read_state", "GET", base(room.id), tokens[i], workspace, nil, 200, &conv); err != nil {
				return err
			}
			if conv.LastReadSequence != room.total || conv.UnreadCount != 0 {
				return fmt.Errorf("user%d cursor regressed %+v", i, conv)
			}
		}
		return nil
	})
	// Crash after projection but before acknowledgment: replay cannot resurrect read inbox.
	must(func() error {
		_, err := db.Exec(`UPDATE workspace_conversation_outbox SET delivered_at=NULL`)
		return err
	}())
	drainAll()
	must(db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE kind='message' AND state='unread'`).Scan(&count))
	if count != 0 {
		t.Fatalf("read inbox resurrected=%d", count)
	}
	must(db.QueryRow(`SELECT COUNT(*) FROM workspace_conversation_agent_jobs`).Scan(&count))
	if count != 0 {
		t.Fatalf("human transport unexpectedly queued%d agent jobs", count)
	}
}

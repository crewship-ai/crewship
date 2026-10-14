package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"sort"
	"sync"
	"testing"
	"time"
)

// Synthetic production-handler profile, not deployed real-agent acceptance or
// a capacity threshold. Setup is excluded; the migrated SQLite store is real.
func TestChatEntityRefsSyntheticProfile500Refs50Objects10Viewers(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('profile-crew',?,'Profile','profile')`, workspace)
	seedAgentRow(t, db, "profile-agent", workspace, "profile-crew", "Jamie", "jamie", "AGENT")
	execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('profile-chat',?,'profile-agent',?,'private')`, workspace, owner)
	viewers := []string{owner}
	for i := 1; i < 10; i++ {
		user := fmt.Sprintf("profile-user-%d", i)
		execOrFatal(t, db, `INSERT INTO users(id,email) VALUES(?,?)`, user, user+"@example.test")
		execOrFatal(t, db, `INSERT INTO workspace_members(id,user_id,workspace_id,role) VALUES(?,?,?,'OWNER')`, "member-"+user, user, workspace)
		viewers = append(viewers, user)
	}
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("profile-issue-%d", i)
		execOrFatal(t, db, `INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,identifier) VALUES(?,?,'profile-crew','profile-agent',?,'Synthetic',?)`, id, workspace, id, fmt.Sprintf("PROFILE-%d", i))
	}
	for i := 0; i < 500; i++ {
		op := fmt.Sprintf("profile-op-%d", i)
		issue := fmt.Sprintf("profile-issue-%d", i%50)
		execOrFatal(t, db, `INSERT INTO agent_operations(id,workspace_id,operation_key,request_hash,actor_type,actor_id,run_id,provenance,kind,verb,entity_id,entity_revision,entity_aggregate_revision,status,result,created_at) VALUES(?,?,?,'hash','agent','profile-agent','profile-run','verified','issue','updated',?,1,1,'completed','{}','2026-10-14T10:00:00Z')`, op, workspace, op, issue)
		execOrFatal(t, db, `INSERT INTO chat_entity_refs(event_id,chat_id,operation_id,run_id,trigger_message_id,created_at) VALUES(?,'profile-chat',?,'profile-run','profile-trigger','2026-10-14T10:00:00Z')`, fmt.Sprintf("profile-event-%d", i), op)
	}
	router := &Router{db: db, logger: newTestLogger()}
	request := func(user, cursor string) (EntityRefPage, int, time.Duration, error) {
		req := httptest.NewRequest("GET", "/api/v1/chats/profile-chat/entity-refs?limit=50&cursor="+url.QueryEscape(cursor), nil)
		req.SetPathValue("chatId", "profile-chat")
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), workspace, "OWNER"))
		rr := httptest.NewRecorder()
		start := time.Now()
		router.chatEntityRefs(rr, req)
		elapsed := time.Since(start)
		var page EntityRefPage
		if rr.Code != 200 {
			return page, rr.Code, elapsed, fmt.Errorf("status %d: %s", rr.Code, rr.Body.String())
		}
		err := json.Unmarshal(rr.Body.Bytes(), &page)
		return page, rr.Code, elapsed, err
	}
	var mu sync.Mutex
	samples := make([]time.Duration, 0, 100)
	errors := make(chan error, 10)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, viewer := range viewers {
		wg.Add(1)
		go func(user string) {
			defer wg.Done()
			<-start
			cursor := ""
			total := 0
			for pages := 0; pages < 11; pages++ {
				page, _, latency, err := request(user, cursor)
				if err != nil {
					errors <- err
					return
				}
				mu.Lock()
				samples = append(samples, latency)
				mu.Unlock()
				if page.Reset != (pages == 0) {
					errors <- fmt.Errorf("viewer %s unexpected reset at page%d", user, pages)
					return
				}
				total += len(page.Refs)
				if !page.HasMore {
					if total != 500 || !page.SnapshotComplete {
						errors <- fmt.Errorf("viewer %s incomplete snapshot total%d", user, total)
					}
					return
				}
				cursor = page.NextCursor
			}
			errors <- fmt.Errorf("viewer %s pagination did not terminate", user)
		}(viewer)
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	t.Logf("synthetic EntityRefs handler: refs=500 objects=50 concurrent_viewers=10 page_size=50 requests=%d p50=%s p95=%s max=%s; setup excluded, no deployed capacity claim", len(samples), samples[(len(samples)-1)*50/100], samples[(len(samples)-1)*95/100], samples[len(samples)-1])
	first, _, _, err := request(owner, "")
	if err != nil || !first.Reset || !first.HasMore {
		t.Fatalf("initial replacement: %+v %v", first, err)
	}
	execOrFatal(t, db, `UPDATE workspace_members SET access_revision=access_revision+1 WHERE workspace_id=? AND user_id=?`, workspace, owner)
	reset, _, latency, err := request(owner, first.NextCursor)
	if err != nil || !reset.Reset || reset.SnapshotID == first.SnapshotID || len(reset.Refs) != 50 {
		t.Fatalf("permission-change reset: %+v %v", reset, err)
	}
	t.Logf("permission-change replacement latency=%s", latency)
	execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER',access_mode='restricted' WHERE workspace_id=? AND user_id=?`, workspace, owner)
	if _, status, _, _ := request(owner, reset.NextCursor); status != 404 {
		t.Fatalf("revoked viewer status=%d want404", status)
	}
}

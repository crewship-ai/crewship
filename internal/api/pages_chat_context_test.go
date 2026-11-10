package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
)

func TestResolvePageChatContextRechecksBothReadersAndLiveGrant(t *testing.T) {
	h, wsID, ownerID := pagesAccessFixture(t)
	ctx := context.Background()
	resolve := func(user, agent, slug string) error {
		_, err := h.ResolvePageChatContext(ctx, wsID, user, agent, slug)
		return err
	}
	page, err := h.ResolvePageChatContext(ctx, wsID, "alice", "agent-watcher", "fleet-201")
	if err != nil || page.Name != "Reach fleet-201" || page.Slug != "fleet-201" || page.PageID == "" || page.WorkspaceID != wsID || page.SnapshotAt == "" {
		t.Fatalf("authorized context = %+v, %v", page, err)
	}
	for _, tc := range []struct{ name, user, agent, slug string }{
		{"human cannot read", "dave", "agent-watcher", "ops-board"},
		{"agent has no grant", "alice", "agent-watcher", "ops-board"},
		{"agent missing", "alice", "nonexistent", "fleet-201"},
		{"human missing", "nonexistent", "agent-watcher", "fleet-201"},
		{"page missing", "alice", "agent-watcher", "nonexistent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := resolve(tc.user, tc.agent, tc.slug); !errors.Is(err, chatbridge.ErrPageChatAccess) {
				t.Fatalf("error = %v, want indistinguishable refusal", err)
			}
		})
	}
	if _, err := h.db.Exec(`DELETE FROM workspace_members WHERE workspace_id=? AND user_id='alice'`, wsID); err != nil {
		t.Fatal(err)
	}
	if err := resolve("alice", "agent-watcher", "fleet-201"); !errors.Is(err, chatbridge.ErrPageChatAccess) {
		t.Fatalf("revoked human membership: %v", err)
	}
	if _, err := h.db.Exec(`UPDATE agents SET expired_at='2026-09-23T00:00:00Z' WHERE id='agent-watcher'`); err != nil {
		t.Fatal(err)
	}
	if err := resolve("bob", "agent-watcher", "fleet-201"); !errors.Is(err, chatbridge.ErrPageChatAccess) {
		t.Fatalf("retired agent: %v", err)
	}
	if _, err := h.db.Exec(`UPDATE agents SET expired_at=NULL WHERE id='agent-watcher'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, wsID, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`DELETE FROM crew_members WHERE user_id=?`, ownerID); err != nil {
		t.Fatal(err)
	}
	if err := resolve("bob", "agent-watcher", "fleet-201"); !errors.Is(err, chatbridge.ErrPageChatAccess) {
		t.Fatalf("grant with revoked issuer: %v", err)
	}
}

func TestPageChatKindUsesSelectedCatalogMetadata(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		document, slides       bool
		storage, profile, want string
	}{
		{name: "document", document: true, want: "docs"},
		{name: "legacy slides", slides: true, want: "slides"},
		{name: "project", storage: "project", want: "app"},
		{name: "library", storage: "project", profile: artifactProfileDesignSystem, want: "library"},
		{name: "dashboard", storage: "dashboard", want: "dashboard"},
		{name: "interactive dashboard", storage: "dashboard", profile: artifactProfileInteractive, want: "app"},
		{name: "library dashboard", storage: "dashboard", profile: artifactProfileDesignSystem, want: "library"},
		{name: "selected slides win", document: true, storage: "slides", want: "slides"},
		{name: "selected design wins", document: true, storage: "design", want: "design"},
		{name: "selected project wins", document: true, storage: "project", want: "app"},
		{name: "native word", document: true, storage: "word"},
		{name: "native sheet", storage: "sheet"},
		{name: "panel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pageChatKind(tc.document, tc.slides, tc.storage, tc.profile); got != tc.want {
				t.Errorf("%+v = %q", tc, got)
			}
		})
	}
}

func TestResolvePageChatContextNamesNoGuideForAPanelPage(t *testing.T) {
	h, wsID, _ := pagesAccessFixture(t)
	page, err := h.ResolvePageChatContext(context.Background(), wsID, "alice", "agent-watcher", "fleet-201")
	if err != nil || page.Kind != "" || page.GuideVersion != "" {
		t.Fatalf("panel Page context = %+v, %v", page, err)
	}
}

// Guide selection uses the same admitted human's code pin as the catalog.
// The private latest profile must not leak through an otherwise live agent.
func TestResolvePageChatContextGuideUsesSharedVersionAndLiveGrant(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	initial := projectWithFiles(t, 0, map[string]string{"dashboard.json": `{"schema_version":1,"collections":["sales"]}`})
	if w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Profiled",`+strings.TrimPrefix(initial, "{")); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	const reader, agent = "guide-reader", "guide-agent"
	pagesSeedUser(t, h, ws, reader, "guide-reader@example.test", "MEMBER")
	pagesSeedAgent(t, h, ws, agent, "guide-agent", "crew-lookout")
	pagesGrant(t, h, ws, owner, "three-screens", `{"subject_type":"user","subject":"guide-reader@example.test","level":"read"}`)
	pagesGrant(t, h, ws, owner, "three-screens", `{"subject_type":"agent","subject":"guide-agent","level":"read"}`)
	next := projectWithFiles(t, 1, map[string]string{"dashboard.json": `{"schema_version":1,"collections":["sales"]}`, "design-system.json": `{"schema_version":1,"name":"Private tokens","tokens":{}}`})
	if w := call(owner, "POST", "three-screens/artifact/versions", next); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	assert := func(user, kind string) {
		t.Helper()
		got, err := h.ResolvePageChatContext(context.Background(), ws, user, agent, "three-screens")
		if err != nil || got.Kind != kind || got.GuideVersion == "" {
			t.Fatalf("%s context=%+v err=%v want=%s", user, got, err, kind)
		}
	}
	assert(owner, "library")
	assert(reader, "dashboard")
	if w := call(owner, "POST", "three-screens/artifact/share", `{"base_version":2,"version":2}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	assert(reader, "library")
	mismatch := context.WithValue(context.Background(), ctxUser, &AuthUser{ID: reader})
	if got, err := h.ResolvePageChatContext(mismatch, ws, owner, agent, "three-screens"); !errors.Is(err, chatbridge.ErrPageChatAccess) || got.Kind != "" {
		t.Fatalf("mismatched principal leaked guide: %+v %v", got, err)
	}
	mustExec(t, h.db, `DELETE FROM page_grants WHERE subject_type='agent' AND subject_id=?`, agent)
	if got, err := h.ResolvePageChatContext(context.Background(), ws, reader, agent, "three-screens"); !errors.Is(err, chatbridge.ErrPageChatAccess) || got.Kind != "" || got.GuideVersion != "" {
		t.Fatalf("revoked guide: %+v %v", got, err)
	}
}

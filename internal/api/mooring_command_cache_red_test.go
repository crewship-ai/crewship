package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// Expected RED on8789ef07f: closed failed entries are never evicted;len=16
// blocks a genuinely empty17th Page. Seeded entries are lifecycle fixtures.
func TestC1RetiredDocumentEntriesDoNotExhaustSixteenCacheSlots(t *testing.T) {
	f, m, r := commandProductTestManager(t)
	mustExec(t, f.r.db, `DELETE FROM mooring_collab_documents WHERE page_id='admission-page'`)
	factory, err := newCommandProductFactory(true, m, func(_ context.Context, ref string) (commandProductPort, error) {
		return &commandProductFixture{commandSQLFixturePort: commandSQLFixturePort{done: make(chan struct{})}, ref: ref}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(factory.Close)
	for i := 0; i < 16; i++ {
		d := &commandProductDocument{ready: make(chan struct{}), err: errors.New("previous definitive open failure")}
		close(d.ready)
		d.once.Do(func() {}) // already retired; no live process/owner
		factory.docs[mooring.SessionKey{WorkspaceID: f.r.wsID, ArtifactID: fmt.Sprintf("retired-%d", i), ApplicationID: "wordcraft"}] = d
	}
	if _, err := factory.Open(r, true); err != nil {
		t.Fatalf("17th document refused after16 retired entries: %v; cached=%d", err, len(factory.docs))
	}
}

// Expected RED: a definitive failure before an engine exists remains cached
// even after cleanup and provider recovery. This is not retrying a mutation.
func TestC1DefinitiveOpenFailureIsRecoverableAfterRuntimeCleanup(t *testing.T) {
	f, m, r := commandProductTestManager(t)
	mustExec(t, f.r.db, `DELETE FROM mooring_collab_documents WHERE page_id='admission-page'`)
	attempts := 0
	factory, err := newCommandProductFactory(true, m, func(_ context.Context, ref string) (commandProductPort, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("fixture definitive start failure before any engine")
		}
		return &commandProductFixture{commandSQLFixturePort: commandSQLFixturePort{done: make(chan struct{})}, ref: ref}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(factory.Close)
	if _, err := factory.Open(r, true); err == nil {
		t.Fatal("control: first open must fail")
	}
	key := mooring.SessionKey{WorkspaceID: f.r.wsID, ArtifactID: "admission-page", ApplicationID: "wordcraft"}
	if d := factory.docs[key]; d != nil {
		d.close(m.owners)
	} // wait for existing retirement
	if _, err := factory.Open(r, true); err != nil {
		t.Fatalf("recovered provider never retried: %v; starts=%d cached=%d", err, attempts, len(factory.docs))
	}
}

// Expected RED:1024 retired views still count against future admissions.
// This is actual HTTP/current-policy handling with a native-port fixture.
func TestC1RetiredViewsDoNotExhaust1024AdmissionSlots(t *testing.T) {
	f, m, r := commandProductTestManager(t)
	m.config.PublicOrigin = "https://studio.example"
	mustExec(t, f.r.db, `DELETE FROM mooring_collab_documents WHERE page_id='admission-page'`)
	p := &commandProjectionFixture{commandProductFixture: commandProductFixture{commandSQLFixturePort: commandSQLFixturePort{done: make(chan struct{})}}, projection: commandProductProjection{Generation: "generation", IDs: []string{"block"}, NativeJSON: `{"body":[]}`}}
	factory, err := newCommandProductFactory(true, m, func(_ context.Context, ref string) (commandProductPort, error) { p.ref = ref; return p, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(factory.Close)
	retired := &commandProductDocument{ready: make(chan struct{}), err: errors.New("retired view")}
	close(retired.ready)
	retired.once.Do(func() {})
	for i := 0; i < 1024; i++ {
		factory.views[fmt.Sprintf("retired-view-%04d", i)] = commandProductView{document: retired}
	}
	request := httptest.NewRequest("POST", "https://studio.example/command-test", strings.NewReader(`{"action":"open","create":true,"write":true}`)).WithContext(r.Context())
	request.SetPathValue("slug", "@admission-page")
	request.Header.Set("Origin", "https://studio.example")
	w := &collaborationSessionIOWriter{ResponseRecorder: httptest.NewRecorder(), fence: m.fence}
	m.fence.Wrap(http.HandlerFunc(factory.serve)).ServeHTTP(w, request)
	if w.Code != http.StatusOK {
		t.Fatalf("new view refused after1024 retired views: HTTP%d cached=%d error=%s", w.Code, len(factory.views), w.Body.String())
	}
}

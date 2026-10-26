package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Lifecycle fixture only: actual native engine qualification is separate.
type commandProductFixture struct {
	commandSQLFixturePort
	ref                string
	closeOnce          sync.Once
	initials, restores int
	wait               bool
	closeBlock         chan struct{}
	initialEntered     chan struct{}
}

func (p *commandProductFixture) RuntimeRef() string { return p.ref }
func (p *commandProductFixture) Projection(context.Context) (commandProductProjection, error) {
	return commandProductProjection{}, errCommandStore // lifecycle fixture has no native renderer
}
func (p *commandProductFixture) Close() {
	p.closeOnce.Do(func() {
		if p.closeBlock != nil {
			<-p.closeBlock
		}
		close(p.done)
	})
}
func (p *commandProductFixture) InitialSnapshot(ctx context.Context, document, generation string) (commandSQLSnapshot, error) {
	p.initials++
	if p.initialEntered != nil {
		close(p.initialEntered)
	}
	if p.wait {
		<-ctx.Done()
		return commandSQLSnapshot{}, ctx.Err()
	}
	raw, _ := json.Marshal(map[string]any{"format": commandFormat, "document": document, "generation": generation, "generations": []string{generation}, "revision": 0, "p": map[string]any{}, "history": []any{}, "seen": map[string]any{}, "engine_identity": map[string]any{"revision": commandEngineRevision, "patch_sha256": commandClockPatch, "catalog": "wordcraft-c1-10-v1"}})
	return commandSQLSnapshot{Checkpoint: raw}, nil
}

func TestCommandProductShutdownCancelsOpening(t *testing.T) {
	for _, preCanceled := range []bool{true, false} {
		t.Run(map[bool]string{true: "parent-already-canceled", false: "close-during-init"}[preCanceled], func(t *testing.T) {
			f, m, r := commandProductTestManager(t)
			mustExec(t, f.r.db, `DELETE FROM mooring_collab_documents WHERE page_id='admission-page'`)
			parent, cancel := context.WithCancel(m.ctx)
			defer cancel()
			m.ctx = parent
			entered := make(chan struct{})
			starts := 0
			factory, _ := newCommandProductFactory(true, m, func(_ context.Context, ref string) (commandProductPort, error) {
				starts++
				return &commandProductFixture{commandSQLFixturePort: commandSQLFixturePort{done: make(chan struct{})}, ref: ref, wait: true, initialEntered: entered}, nil
			})
			t.Cleanup(factory.Close)
			if preCanceled {
				cancel()
				if d, err := factory.Open(r, true); err == nil || d != nil || starts != 0 {
					t.Fatal("canceled parent started runtime", err)
				}
				return
			}
			result := make(chan error, 1)
			go func() {
				d, err := factory.Open(r, true)
				if d != nil {
					t.Error("closed factory published runtime")
				}
				result <- err
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("initializer not entered")
			}
			closed := make(chan struct{})
			go func() { factory.Close(); close(closed) }()
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("Close did not cancel Open")
				}
			case <-time.After(time.Second):
				t.Fatal("Open not canceled by Close")
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("Close did not drain initializer")
			}
			var count int
			if err := f.r.db.QueryRow(`SELECT COUNT(*) FROM mooring_collab_documents WHERE page_id='admission-page'`).Scan(&count); err != nil || count != 0 {
				t.Fatal("shutdown initialization changed SQL", err)
			}
		})
	}
}
func (p *commandProductFixture) RestoreConfirmed(ctx context.Context, s commandSQLSnapshot) error {
	p.restores++
	return p.commandSQLFixturePort.RestoreConfirmed(ctx, s)
}
func commandProductTestManager(t *testing.T) (*continuityRig, *collaborationHTTPManager, *http.Request) {
	t.Helper()
	f := newContinuityRig(t, true)
	if err := f.s.owners.Release(f.ctx, f.owner); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := &collaborationHTTPManager{pages: f.s.pages, owners: f.s.owners, fence: f.s.fence, clock: f.s.clock, ctx: ctx}
	r := httptest.NewRequest("POST", "/not-a-public-route", nil).WithContext(context.WithValue(f.ctx, ctxWorkspaceID, f.r.wsID))
	r.SetPathValue("slug", "@admission-page")
	return f, m, r
}
func TestCommandProductLifecycleCreateRestoreAndCurrentRights(t *testing.T) {
	f, m, r := commandProductTestManager(t)
	mustExec(t, f.r.db, `DELETE FROM mooring_collab_documents WHERE page_id='admission-page'`)
	var ports []*commandProductFixture
	start := func(_ context.Context, ref string) (commandProductPort, error) {
		p := &commandProductFixture{commandSQLFixturePort: commandSQLFixturePort{done: make(chan struct{})}, ref: ref}
		ports = append(ports, p)
		return p, nil
	}
	if _, err := newCommandProductFactory(false, m, start); err == nil {
		t.Fatal("default-off bypassed")
	}
	factory, err := newCommandProductFactory(true, m, start)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(factory.Close)
	d, err := factory.Open(r, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 1 || ports[0].initials != 1 {
		t.Fatal("not exactly one initial engine")
	}
	if cached, err := factory.Open(r, false); err != nil || cached != d || len(ports) != 1 {
		t.Fatal("cached open created another runtime", err)
	}
	factory.Close()
	select {
	case <-ports[0].Done():
	default:
		t.Fatal("runtime not stopped before release")
	}
	reopened, err := newCommandProductFactory(true, m, start)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	if _, err = reopened.Open(r, false); err != nil {
		t.Fatal("canonical reopen", err)
	}
	if len(ports) != 2 || ports[1].initials != 0 || ports[1].restores != 1 {
		t.Fatal("reopen executed initialization")
	}
	mustExec(t, f.r.db, `UPDATE workspace_members SET role='VIEWER',access_mode='restricted' WHERE workspace_id=? AND user_id=?`, f.r.wsID, f.r.userID)
	if _, err = reopened.Open(r, false); err == nil {
		t.Fatal("cached runtime bypassed invalidated original credential")
	}
}
func TestCommandProductRejectsLegacyAndExhaustedOpen(t *testing.T) {
	t.Run("legacy-profile", func(t *testing.T) {
		_, m, r := commandProductTestManager(t)
		var p *commandProductFixture
		factory, _ := newCommandProductFactory(true, m, func(_ context.Context, ref string) (commandProductPort, error) {
			p = &commandProductFixture{commandSQLFixturePort: commandSQLFixturePort{done: make(chan struct{})}, ref: ref}
			return p, nil
		})
		t.Cleanup(factory.Close)
		if _, err := factory.Open(r, false); err == nil {
			t.Fatal("legacy converted")
		}
		if p != nil && (p.initials != 0 || p.restores != 0) {
			t.Fatal("legacy bytes sent to native engine")
		}
	})
	t.Run("one-open-budget", func(t *testing.T) {
		f, m, r := commandProductTestManager(t)
		mustExec(t, f.r.db, `DELETE FROM mooring_collab_documents WHERE page_id='admission-page'`)
		var p *commandProductFixture
		closeBlock := make(chan struct{})
		factory, _ := newCommandProductFactory(true, m, func(_ context.Context, ref string) (commandProductPort, error) {
			p = &commandProductFixture{commandSQLFixturePort: commandSQLFixturePort{done: make(chan struct{})}, ref: ref, wait: true, closeBlock: closeBlock}
			return p, nil
		})
		t.Cleanup(factory.Close)
		t.Cleanup(func() { close(closeBlock) })
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Millisecond)
		defer cancel()
		result := make(chan error, 1)
		go func() { _, err := factory.Open(r.WithContext(ctx), true); result <- err }()
		select {
		case err := <-result:
			if err == nil {
				t.Fatal("expired Open succeeded")
			}
		case <-time.After(time.Second):
			t.Fatal("exhausted Open waits for process cleanup")
		}
		var count int
		if err := f.r.db.QueryRow(`SELECT COUNT(*) FROM mooring_collab_documents WHERE page_id='admission-page'`).Scan(&count); err != nil || count != 0 {
			t.Fatal("expired initialization changed SQL", err)
		}
	})
}

//go:build linux

package restrictedworkflow

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestPageFingerprintIsReadOnlyAndTracksLiveAuthority(t *testing.T) {
	s, _ := fixture(t)
	action := seedPage(t, s)
	before := map[string]int{}
	for _, table := range []string{"chats", "access_attempts", "restricted_workflow_jobs", "work_items"} {
		var count int
		if err := s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		before[table] = count
	}
	fingerprint, err := s.PageActionFingerprint(t.Context(), "h1", "w", action)
	if err != nil || fingerprint == "" {
		t.Fatalf("authorized action unavailable: %q %v", fingerprint, err)
	}
	if err = s.PageActionAvailable(t.Context(), "h1", "w", action); err != nil {
		t.Fatal(err)
	}
	again, err := s.PageActionFingerprint(t.Context(), "h1", "w", action)
	if err != nil || again != fingerprint {
		t.Fatalf("unchanged intent not stable: %q %v", again, err)
	}
	for table, want := range before {
		var count int
		if err = s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != want {
			t.Fatalf("catalog created %s authority: %d vs %d %v", table, count, want, err)
		}
	}
	for _, user := range []string{"h2", "owner", "missing"} {
		if got, err := s.PageActionFingerprint(t.Context(), user, "w", action); got != "" || !errors.Is(err, ErrDenied) {
			t.Fatalf("unauthorized user %s saw fingerprint %q %v", user, got, err)
		}
	}
	changed := action
	changed.ActionDigest = "changed"
	if err = s.PageActionAvailable(t.Context(), "h1", "w", changed); !errors.Is(err, ErrDenied) {
		t.Fatalf("changed action accepted: %v", err)
	}
	store := access.Store{DB: s.db}
	member, err := store.Membership(t.Context(), "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), "owner", "h1", "w", "restricted", member, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PageActionFingerprint(t.Context(), "h1", "w", action); got != "" || err == nil {
		t.Fatalf("revoked run grant exposed intent %q %v", got, err)
	}
}

func TestPageFingerprintRefusesUnavailableGraphAndUntypedExecutor(t *testing.T) {
	s, _ := fixture(t)
	action := seedPage(t, s)
	original := s.executor
	s.executor = plainWorkflowExecutor{}
	if _, err := s.PageActionFingerprint(t.Context(), "h1", "w", action); !errors.Is(err, ErrDenied) {
		t.Fatalf("untyped executor accepted: %v", err)
	}
	if _, err := s.Catalog(t.Context(), "h1", "w"); !errors.Is(err, ErrDenied) {
		t.Fatalf("untyped catalog accepted: %v", err)
	}
	if s.supportsProfile("responses_text") {
		t.Fatal("untyped executor advertises typed profile")
	}
	s.executor = original
	if _, err := s.db.ExecContext(t.Context(), `UPDATE agents SET restricted_execution_profile='disabled' WHERE id='agent'`); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PageActionFingerprint(t.Context(), "h1", "w", action); got != "" || err == nil {
		t.Fatalf("unsupported graph accepted: %q %v", got, err)
	}
}

type plainWorkflowExecutor struct{}

func (plainWorkflowExecutor) ExecuteRun(context.Context, string, string, string, string, func(string, string) error) error {
	return nil
}

func TestPreparedCapsuleRejectsForeignOwnerAndMalformedRights(t *testing.T) {
	s, _ := fixture(t)
	var empty *PreparedInvocation
	if got := empty.Metadata(); got != (PreparedMetadata{}) {
		t.Fatalf("nil metadata: %+v", got)
	}
	for _, p := range []*PreparedInvocation{nil, {}, {owner: &Service{}, handle: "foreign"}, {owner: s}} {
		if err := s.CancelPrepared(t.Context(), p); !errors.Is(err, ErrDenied) {
			t.Fatalf("invalid capsule cancelled: %v", err)
		}
		if _, err := s.EnqueuePrepared(t.Context(), nil, p); !errors.Is(err, ErrDenied) {
			t.Fatalf("invalid capsule enqueued: %v", err)
		}
	}
	for _, tc := range []struct {
		facet  string
		rights []access.Right
	}{
		{"page", []access.Right{{Kind: "project", ID: "project", Operation: "read"}}},
		{"issue", nil},
		{"issue", []access.Right{{Kind: "agent", ID: "agent", Operation: "run"}}},
		{"issue", []access.Right{{Kind: "project", ID: "", Operation: "read"}}},
	} {
		if p, err := s.PrepareManualWithRights(t.Context(), "h1", "w", "private-work", nil, "", tc.rights, tc.facet); p != nil || !errors.Is(err, ErrDenied) {
			t.Fatalf("invalid source rights prepared: %v", err)
		}
	}
	s.executor = plainWorkflowExecutor{}
	if _, err := s.PrepareManualWithRights(t.Context(), "h1", "w", "private-work", nil, "", []access.Right{{Kind: "project", ID: "project", Operation: "read"}}, "issue"); !errors.Is(err, ErrDenied) {
		t.Fatalf("executor without source rights accepted: %v", err)
	}
}

func TestWorkflowRuntimeStopRequiresObservableCompletion(t *testing.T) {
	s, runner := fixture(t)
	r := newWorkflowRuntime(s)
	if stopped, err := r.Stop(t.Context(), "unknown"); stopped || err == nil {
		t.Fatalf("unknown stop confirmed: %v %v", stopped, err)
	}
	done := make(chan struct{})
	cancelled := false
	r.attempts["active"] = &workflowAttempt{done: done, cancel: func() { cancelled = true }, workflowID: "job", executionStarted: true}
	r.Forget("active")
	if alive, err := r.Alive(t.Context(), "active"); !alive || err != nil {
		t.Fatalf("live attempt forgotten: %v %v", alive, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if stopped, err := r.Stop(ctx, "active"); stopped || !errors.Is(err, context.Canceled) || !cancelled {
		t.Fatalf("unfinished cancellation confirmed: %v %v", stopped, err)
	}
	close(done)
	s.executor = unconfirmedGraphStop{runner}
	if stopped, err := r.Stop(t.Context(), "active"); stopped || err == nil {
		t.Fatalf("physical cleanup uncertainty ignored: %v %v", stopped, err)
	}
	confirmed := &confirmedGraphStop{TextRunner: runner}
	s.executor = confirmed
	if stopped, err := r.Stop(t.Context(), "active"); !stopped || err != nil || confirmed.checks != 1 {
		t.Fatalf("confirmed completion: %v %v checks=%d", stopped, err, confirmed.checks)
	}
	r.Forget("active")
	if alive, err := r.Alive(t.Context(), "active"); alive || err == nil {
		t.Fatalf("forgotten attempt treated as confirmed: %v %v", alive, err)
	}
	s.Wake()
	s.Wake()
}

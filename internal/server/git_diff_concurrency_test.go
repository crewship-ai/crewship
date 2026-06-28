package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/provider"
)

// blockingDiffMock holds each Exec briefly and records the peak concurrency,
// so the test can prove gitDiffSem actually bounds simultaneous execs.
type blockingDiffMock struct {
	*mockContainer
	cur  int32
	peak int32
}

func (m *blockingDiffMock) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	n := atomic.AddInt32(&m.cur, 1)
	for {
		p := atomic.LoadInt32(&m.peak)
		if n <= p || atomic.CompareAndSwapInt32(&m.peak, p, n) {
			break
		}
	}
	time.Sleep(40 * time.Millisecond)
	atomic.AddInt32(&m.cur, -1)
	nonce := ""
	for _, e := range cfg.Env {
		if strings.HasPrefix(e, "CRW_NONCE=") {
			nonce = strings.TrimPrefix(e, "CRW_NONCE=")
		}
	}
	return &provider.ExecResult{Reader: io.NopCloser(strings.NewReader(nonce + "NOTREPO\n"))}, nil
}

// TestHandleContainerGitDiff_ConcurrencyBounded fires many simultaneous
// requests and asserts (a) every one completes (no deadlock / no dropped
// request) and (b) the semaphore caps simultaneous execs at cap(gitDiffSem).
func TestHandleContainerGitDiff_ConcurrencyBounded(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "gdc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(context.Background(), db.DB, newSilentLogger()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug, created_at, updated_at) VALUES ('w1','W','w',?,?)`, now, now)
	mustExec(t, db.DB, `INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at) VALUES ('c1','w1','C','crew-x',?,?)`, now, now)

	cfg := config.Default()
	cfg.Auth.JWTSecret = "test-secret-for-server-tests-32-chars"
	mock := &blockingDiffMock{mockContainer: &mockContainer{}}
	s := New(cfg, logging.New("error", "json", nil), &Deps{Container: mock, DB: db.DB})
	s.startedAt = time.Now()
	t.Cleanup(func() {
		s.StopBackground()
		if s.fileWatcher != nil {
			s.fileWatcher.Close()
		}
	})

	const N = 16
	var ok int32
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			s.ipcMux.ServeHTTP(rec, httptest.NewRequest("GET", "/crews/c1/git-diff", nil))
			if rec.Code == http.StatusOK {
				atomic.AddInt32(&ok, 1)
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("deadlock: not all %d requests finished within 5s", N)
	}

	if ok != N {
		t.Fatalf("completed %d/%d requests OK", ok, N)
	}
	if peak := atomic.LoadInt32(&mock.peak); int(peak) > cap(gitDiffSem) {
		t.Fatalf("peak concurrent execs = %d, want <= %d (semaphore not bounding)", peak, cap(gitDiffSem))
	}
}

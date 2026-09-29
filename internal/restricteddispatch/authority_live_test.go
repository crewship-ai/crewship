//go:build linux && restrictedruntime_live

package restricteddispatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

// The real migrated application store replaces the runtime harness's synthetic
// authority. No live application database, other instance, or provider is used.
func TestLiveApplicationAuthorityStopsOnlyRevokedHuman(t *testing.T) {
	if os.Getenv("CREWSHIP_RESTRICTED_LIVE") != "1" || os.Getenv("CREWSHIP_RESTRICTED_IMAGE") == "" {
		t.Fatal("run scripts/restricted-runtime-probe/run.sh with owned Docker fixtures")
	}
	a := fixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	d := restrictedruntime.Docker{Image: os.Getenv("CREWSHIP_RESTRICTED_IMAGE")}
	m, err := restrictedruntime.New(filepath.Join(t.TempDir(), "runtime"), d, a, a, restrictedruntime.Limits{MemoryBytes: 64 << 20, NanoCPUs: 500000000, PIDs: 32})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	h1, _, err := a.Prepare(ctx, "h1", "w", "a", "c1", "", nil, command("/bin/sh", "-c", `test "$(id -u)" = 1001 || exit 1; printf 'H1_PRIVATE_CANARY' > /tmp/h1-private; printf 'H1_READY\n'; sleep 50`))
	if err != nil {
		t.Fatal(err)
	}
	s1, err := m.Start(ctx, h1)
	if err != nil {
		t.Fatal(err)
	}
	awaitOutput(t, ctx, s1, "H1_READY")
	h2, _, err := a.Prepare(ctx, "h2", "w", "a", "c2", "", nil, command("/bin/sh", "-c", `test "$(id -u)" = 1001 || exit 1; test ! -e /tmp/h1-private || exit 1; test ! -e /var/run/docker.sock || exit 1; test ! -e /data || exit 1; printf 'H2_ISOLATED\n'; sleep 50`))
	if err != nil {
		t.Fatal(err)
	}
	s2, err := m.Start(ctx, h2)
	if err != nil {
		t.Fatal(err)
	}
	awaitOutput(t, ctx, s2, "H2_ISOLATED")
	if s1.ID() == s2.ID() {
		t.Fatal("same-agent humans share a container")
	}
	setRights(t, a, "h1", nil)
	revoked := time.Now()
	if _, err := s1.Output(ctx); err == nil {
		t.Fatal("output remained readable after committed revocation")
	}
	if _, err := m.Start(ctx, h1); err == nil {
		t.Fatal("revoked admission relaunched")
	}
	select {
	case <-s1.Done():
	case <-time.After(17 * time.Second):
		t.Fatal("application revocation did not stop runtime within 17 seconds")
	}
	if s1.Record().Status != "terminated" {
		t.Fatalf("termination not confirmed: %+v", s1.Record())
	}
	check, err := exec.CommandContext(ctx, "docker", "ps", "-q", "--filter", "id="+s1.ID()).Output()
	if err != nil || len(strings.TrimSpace(string(check))) != 0 {
		t.Fatalf("revoked container still running: err=%v", err)
	}
	select {
	case <-s2.Done():
		t.Fatal("revoking H1 stopped H2")
	default:
	}
	awaitOutput(t, ctx, s2, "H2_ISOLATED")
	t.Logf("real application grants: two UID-1001 containers, separate /tmp, revoked output denied, whole-container termination %s, H2 remains authorized", time.Since(revoked))
}

func awaitOutput(t *testing.T, ctx context.Context, s *restrictedruntime.Session, marker string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		out, err := s.Output(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, marker) {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("missing positive execution marker %s", marker)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
}

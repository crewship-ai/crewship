//go:build linux && restrictedruntime_live

package restricteddispatch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

func TestLiveApplicationProviderRevocation(t *testing.T) {
	if os.Getenv("CREWSHIP_RESTRICTED_LIVE") != "1" || os.Getenv("CREWSHIP_RESTRICTED_IMAGE") == "" {
		t.Fatal("run scripts/restricted-runtime-probe/run.sh with owned Docker fixtures")
	}
	a := providerFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	m, err := restrictedruntime.New(filepath.Join(t.TempDir(), "runtime"), restrictedruntime.Docker{Image: os.Getenv("CREWSHIP_RESTRICTED_IMAGE")}, a, a, restrictedruntime.Limits{MemoryBytes: 64 << 20, NanoCPUs: 500000000, PIDs: 32})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	build := command("sh", "-c", `set -eu; test "$(id -u)" = 1001; test ! -e /var/run/docker.sock; test "$(stat -c %u /broker)" = 1002; if ls /broker >/dev/null 2>&1; then exit 91; fi; needle=$(printf 'synthetic-pinned-%s' key); if grep -Raql "$needle" /home/agent /secrets /tmp 2>/dev/null; then exit 92; fi; for p in /proc/[0-9]*; do if cat "$p/environ" "$p/cmdline" 2>/dev/null | grep -q "$needle"; then exit 93; fi; done; printf 'PINNED_PROVIDER_READY\n'; sleep 45`)
	type running struct {
		handle  string
		session *restrictedruntime.Session
	}
	var attempts []running
	for _, human := range []struct{ user, chat string }{{"h1", "c1"}, {"h2", "c2"}} {
		h, _, err := a.PrepareResponses(ctx, human.user, "w", "a", human.chat, "", nil, 128, build)
		if err != nil {
			t.Fatal(err)
		}
		secret, err := a.BrokerSecret(ctx, h, "key")
		if err != nil || secret.Value != "synthetic-pinned-key" {
			t.Fatal("host positive credential control failed", err)
		}
		s, err := m.Start(ctx, h)
		if err != nil {
			t.Fatal(err)
		}
		attempts = append(attempts, running{h, s})
		awaitOutput(t, ctx, s, "PINNED_PROVIDER_READY")
	}
	if attempts[0].session.ID() == attempts[1].session.ID() {
		t.Fatal("two humans share runtime")
	}
	// Revoke and restore before the next watchdog poll. Both old attempts must
	// stay dead although the current credential again reads ACTIVE.
	if _, err := a.Store.DB.ExecContext(ctx, `UPDATE credentials SET status='EXPIRED' WHERE id='key'; UPDATE credentials SET status='ACTIVE' WHERE id='key'`); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	for _, r := range attempts {
		if _, err := r.session.Output(ctx); err == nil {
			t.Fatal("old provider output survived revoke/regrant")
		}
		if _, err := a.BrokerSecret(ctx, r.handle, "key"); err == nil {
			t.Fatal("old key authority revived")
		}
	}
	for _, r := range attempts {
		select {
		case <-r.session.Done():
		case <-time.After(17 * time.Second):
			t.Fatal("provider revocation did not stop runtime")
		}
		if r.session.Record().Status != "terminated" {
			t.Fatal("provider termination unconfirmed")
		}
	}
	h, _, err := a.PrepareResponses(ctx, "h1", "w", "a", "c1", "", nil, 128, build)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := m.Start(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	awaitOutput(t, ctx, fresh, "PINNED_PROVIDER_READY")
	t.Logf("real application credential binding: two private UID-1001 brokered runtimes; host-only synthetic key; revoke/regrant terminated both old attempts in %s; fresh admission succeeded; no external model request", time.Since(started))
}

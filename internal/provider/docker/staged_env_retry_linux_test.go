//go:build linux

package docker

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"golang.org/x/sys/unix"

	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
)

func cleanupFixture(t *testing.T, handler http.HandlerFunc) (*Provider, container.InspectResponse, string, string) {
	t.Helper()
	storage, c, unknown := privateEnvFixture(t)
	p, close := newFakeDockerProvider(t, handler)
	t.Cleanup(close)
	p.cfg = storage.cfg
	c.ID = strings.Repeat("a", 64)
	if err := p.saveStagedEnv(c, []string{"TOKEN=synthetic"}); err != nil {
		t.Fatal(err)
	}
	target, err := p.stagedEnvPath(c)
	if err != nil {
		t.Fatal(err)
	}
	// The original fixture deliberately has no pending journal record.
	t.Cleanup(func() {
		if _, err := os.Stat(unknown); err != nil {
			t.Errorf("unknown env material changed: %v", err)
		}
	})
	journal := filepath.Join(p.cfg.OutputBasePath, runtimestage.DirName, "staged-env-cleanup", c.ID+".json")
	return p, c, target, journal
}

func TestStagedCleanupRetriesAfterDeleteAndRestart(t *testing.T) {
	for _, mode := range []string{"same-process", "restart", "docker-failure", "still-present", "inspect-failure"} {
		t.Run(mode, func(t *testing.T) {
			deletes, inspects := 0, 0
			var journal string
			p, c, target, journal := cleanupFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.URL.Path, "/containers/"+strings.Repeat("a", 64)) {
					t.Errorf("wrong scope request: %s", r.URL.Path)
				}
				if r.Method == http.MethodDelete {
					deletes++
					// Intent must exist and be free of env values BEFORE DELETE.
					if raw, err := os.ReadFile(journal); err != nil || strings.Contains(string(raw), "TOKEN") {
						t.Errorf("delete preceded private intent publication: %v", err)
					}
					if mode == "docker-failure" {
						w.WriteHeader(http.StatusConflict)
					} else if deletes == 1 {
						w.WriteHeader(http.StatusNoContent)
					} else {
						w.WriteHeader(http.StatusNotFound)
					}
				} else if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json") {
					inspects++
					if mode == "still-present" {
						w.Write([]byte(`{"Id":"still-present"}`))
					} else if mode == "inspect-failure" {
						w.WriteHeader(http.StatusInternalServerError)
					} else {
						w.WriteHeader(http.StatusNotFound)
					}
				} else {
					t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
				}
			})
			if err := os.Chmod(target, 0644); err != nil {
				t.Fatal(err)
			}
			if err := p.RemoveCrewRuntime(t.Context(), c.ID); err == nil {
				t.Fatal("cleanup/delete failure silently succeeded")
			}
			raw, err := os.ReadFile(journal)
			if err != nil || strings.Contains(string(raw), "TOKEN") || strings.Contains(string(raw), "synthetic") {
				t.Fatalf("metadata intent missing or secret-bearing: %s %v", raw, err)
			}
			if err := os.Chmod(target, 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "same-process" {
				err = p.RemoveCrewRuntime(t.Context(), c.ID)
			} else {
				// A new provider has no in-memory authenticated scopes.
				p = &Provider{cfg: p.cfg, client: p.client}
				err = p.reconcileStagedEnvCleanup(t.Context(), 64)
			}
			retained := mode == "still-present" || mode == "inspect-failure"
			if (err != nil) != retained {
				t.Fatalf("retry outcome: %v", err)
			}
			for _, path := range []string{target, journal} {
				_, e := os.Stat(path)
				if retained && e != nil || !retained && !os.IsNotExist(e) {
					t.Fatalf("retention=%v path=%s err=%v", retained, path, e)
				}
			}
			if mode == "same-process" && (deletes != 2 || inspects != 0) || mode != "same-process" && (deletes != 1 || inspects != 1) {
				t.Fatalf("unexpected Docker requests: delete=%d inspect=%d", deletes, inspects)
			}
		})
	}
}

func TestStagedCleanupReconciliationIsBounded(t *testing.T) {
	calls := 0
	p, c, _, _ := cleanupFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
	})
	for _, id := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)} {
		c.ID = id
		if err := p.publishStagedCleanup(c); err != nil {
			t.Fatal(err)
		}
	}
	for _, limit := range []int{0, 65} {
		if err := p.reconcileStagedEnvCleanup(t.Context(), limit); err == nil || calls != 0 {
			t.Fatalf("invalid bound admitted: %v calls=%d", err, calls)
		}
	}
	if err := p.reconcileStagedEnvCleanup(t.Context(), 1); err == nil || calls != 1 {
		t.Fatalf("batch bound not enforced/reported: %v calls=%d", err, calls)
	}
	if err := p.reconcileStagedEnvCleanup(t.Context(), 64); err != nil || calls != 3 {
		t.Fatalf("remaining exact records not reconciled: %v calls=%d", err, calls)
	}
	if err := p.reconcileStagedEnvCleanup(t.Context(), 64); err != nil || calls != 3 {
		t.Fatalf("empty journal not idempotent: %v calls=%d", err, calls)
	}
}

func TestStagedCleanupJournalRejectsUntrustedMaterial(t *testing.T) {
	for _, mode := range []string{"corrupt", "foreign", "file-symlink", "fifo", "wrong-mode", "directory-symlink", "binding"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			p, c, target, journal := cleanupFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(http.StatusNotFound)
			})
			if err := p.publishStagedCleanup(c); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch mode {
			case "corrupt":
				err = os.WriteFile(journal, []byte("invalid"), 0600)
			case "foreign", "binding":
				intent := cleanupIntent(c)
				if mode == "foreign" {
					intent.Installation = "foreign"
				} else {
					intent.Container = strings.Repeat("b", 64)
				}
				raw, e := json.Marshal(intent)
				if e != nil {
					t.Fatal(e)
				}
				err = os.WriteFile(journal, raw, 0600)
			case "file-symlink", "fifo":
				if err = os.Remove(journal); err == nil {
					if mode == "fifo" {
						err = unix.Mkfifo(journal, 0600)
					} else {
						err = os.Symlink(outside, journal)
					}
				}
			case "wrong-mode":
				err = os.Chmod(journal, 0644)
			case "directory-symlink":
				dir := filepath.Dir(journal)
				if err = os.Rename(dir, dir+"-kept"); err == nil {
					err = os.Symlink(dir+"-kept", dir)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := p.reconcileStagedEnvCleanup(t.Context(), 64); err == nil || calls != 0 {
				t.Fatalf("untrusted journal used: %v calls=%d", err, calls)
			}
			if err := p.RemoveCrewRuntime(t.Context(), c.ID); err == nil || calls != 0 {
				t.Fatalf("delete allowed without authentic durable intent: %v calls=%d", err, calls)
			}
			if _, err := os.Stat(target); err != nil {
				t.Fatal("untrusted scope lost env")
			}
			if raw, err := os.ReadFile(outside); err != nil || string(raw) != "preserve" {
				t.Fatal("outside material changed")
			}
		})
	}
}

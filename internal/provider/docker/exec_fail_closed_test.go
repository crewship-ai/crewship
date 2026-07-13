package docker

// Tests for Exec / ExecInteractive's user-resolution default — #1158.
//
// Provider.Exec and Provider.ExecInteractive used to default an EMPTY
// cfg.User to a hardcoded "1001:1001" (see the historic default block this
// replaces). That directly contradicted the fail-closed philosophy keeper's
// /execute path adopted in #1060/PR #1135: instead of resolving the
// container's actual configured run-as user and refusing to run if it can't
// prove that user is safe, the generic Exec path silently ran as a constant
// — so a future call site (or a custom base image whose agent user isn't
// 1001) would silently run as the wrong uid instead of erroring.
//
// The fix mirrors #1060: when cfg.User is empty, resolve the container's
// real configured user via ContainerInspect (the same source of truth
// Provider.ContainerUser already exposes to keeper) and fail closed —
// return an error, never exec — if that user is undeterminable, empty, or
// privileged (root uid/gid, using the same strict numeric uid[:gid]
// validation #1135 introduced, now shared via provider.IsPrivilegedExecUser).

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// TestExec_EmptyUser_ResolvesConfiguredContainerUser proves that when the
// caller omits User, Exec resolves the container's ACTUAL configured user
// (e.g. "2000:2000" for a custom base image) rather than defaulting to a
// hardcoded "1001:1001".
func TestExec_EmptyUser_ResolvesConfiguredContainerUser(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var execCreateReached bool
	var seenExecUser string
	p := newCovProviderTCP(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/containers/cid/json") && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":     "cid",
				"Config": map[string]any{"User": "2000:2000"},
			})
		case strings.HasSuffix(path, "/containers/cid/exec") && r.Method == http.MethodPost:
			body, _ := decodeExecBody(r)
			mu.Lock()
			execCreateReached = true
			seenExecUser = body.User
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"e1"}`))
		case strings.Contains(path, "/exec/e1/start"):
			covHijackUpgrade(t, w, r, covStdcopyFrame(1, "ok"))
		default:
			t.Errorf("unexpected request %s %s", r.Method, path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	res, err := p.Exec(context.Background(), provider.ExecConfig{
		ContainerID: "cid",
		Cmd:         []string{"echo", "hi"},
		// User intentionally empty.
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	_, _ = res.Reader.Read(make([]byte, 0))

	mu.Lock()
	defer mu.Unlock()
	if !execCreateReached {
		t.Fatal("exec create was never reached")
	}
	if seenExecUser != "2000:2000" {
		t.Errorf("exec user = %q, want 2000:2000 (resolved from container config, not a hardcoded default)", seenExecUser)
	}
}

// TestExec_EmptyUser_FailsClosed_WhenUnresolvableOrPrivileged proves Exec
// refuses to run — never reaching exec create — when the container's
// configured user can't be resolved, is empty, or is privileged (root uid,
// root gid, or a non-numeric alias for either). This is the core #1158 red
// case: on unpatched code, Exec ignores the container's real configured user
// entirely and defaults straight to a hardcoded "1001:1001", so it reaches
// exec create instead of failing closed.
func TestExec_EmptyUser_FailsClosed_WhenUnresolvableOrPrivileged(t *testing.T) {
	cases := []struct {
		name          string
		inspectStatus int
		configUser    string
	}{
		{"inspect error", http.StatusInternalServerError, ""},
		{"empty configured user", http.StatusOK, ""},
		{"root user", http.StatusOK, "0:0"},
		{"root group", http.StatusOK, "1001:0"},
		{"root uid non-root group", http.StatusOK, "0:1001"},
		{"non-numeric root alias", http.StatusOK, "toor"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var mu sync.Mutex
			var execCreateReached bool
			p := newCovProviderTCP(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
				path := r.URL.Path
				switch {
				case strings.HasSuffix(path, "/containers/cid/json") && r.Method == http.MethodGet:
					if tc.inspectStatus != http.StatusOK {
						http.Error(w, `{"message":"inspect failed"}`, tc.inspectStatus)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"Id":     "cid",
						"Config": map[string]any{"User": tc.configUser},
					})
				case strings.HasSuffix(path, "/containers/cid/exec") && r.Method == http.MethodPost:
					mu.Lock()
					execCreateReached = true
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"Id":"e1"}`))
				default:
					t.Errorf("unexpected request %s %s", r.Method, path)
					w.WriteHeader(http.StatusInternalServerError)
				}
			})

			_, err := p.Exec(context.Background(), provider.ExecConfig{
				ContainerID: "cid",
				Cmd:         []string{"echo", "hi"},
				// User intentionally empty — this is the fail-open default path.
			})
			if err == nil {
				t.Fatal("expected Exec to fail closed (return an error), got nil")
			}

			mu.Lock()
			defer mu.Unlock()
			if execCreateReached {
				t.Error("exec create was reached — command ran despite an unresolvable/privileged user; must fail closed BEFORE exec")
			}
		})
	}
}

// TestExecInteractive_EmptyUser_FailsClosed_WhenPrivileged mirrors the
// non-interactive case for ExecInteractive (the second default-user block
// #1158 covers, ~line 855-856 pre-fix).
func TestExecInteractive_EmptyUser_FailsClosed_WhenPrivileged(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var execCreateReached bool
	p := newCovProviderTCP(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/containers/cid/json") && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":     "cid",
				"Config": map[string]any{"User": "0:0"},
			})
		case strings.HasSuffix(path, "/containers/cid/exec") && r.Method == http.MethodPost:
			mu.Lock()
			execCreateReached = true
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"e1"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	_, err := p.ExecInteractive(context.Background(), provider.InteractiveExecConfig{
		ContainerID: "cid",
		Cmd:         []string{"bash"},
		// User intentionally empty.
	})
	if err == nil {
		t.Fatal("expected ExecInteractive to fail closed (return an error), got nil")
	}

	mu.Lock()
	defer mu.Unlock()
	if execCreateReached {
		t.Error("exec create was reached — interactive session started despite a privileged resolved user")
	}
}

// decodeExecBody reads the ContainerExecCreate JSON body's "User" field.
func decodeExecBody(r *http.Request) (struct {
	User string `json:"User"`
}, error) {
	var body struct {
		User string `json:"User"`
	}
	err := json.NewDecoder(r.Body).Decode(&body)
	return body, err
}

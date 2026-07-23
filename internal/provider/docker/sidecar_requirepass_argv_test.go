package docker

// Angle 1: argv vs shell injection at the provider boundary.
//
// expandAutoCredentialsInCrewSpec splices the generated redis secret
// into svc.Command as a SEPARATE argv element. This test traces that
// Command all the way to the Docker container-create request and proves
// the value stays a DISCRETE argv token — the daemon receives Cmd as a
// JSON array (exec form), never a shell string. If any layer joined the
// argv into a "sh -c" string, a value containing shell metacharacters
// could inject; we use a metacharacter-laden value to make that failure
// mode observable even though real values are hex.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestSidecarRequirepass_ValueIsDiscreteArgvToken(t *testing.T) {
	// Deliberately hostile value: if any layer shell-joins the argv, these
	// metacharacters would let it break out. Real generated values are hex,
	// so this is a defense-in-depth assertion on the transport shape.
	const hostileValue = "abc; rm -rf / #$(whoami)`id`"
	command := []string{"redis-server", "--requirepass", hostileValue}

	var createBody map[string]any
	captured := false

	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			// No existing sidecar — force the create path.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
			// ImageInspect: report the image is present locally.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "sha256:redisimg"})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/images/create"):
			// ImagePull: empty successful stream.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/create"):
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &createBody)
			captured = true
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "cid-redis"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start"):
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	defer cleanup()

	id, err := p.ensureSidecar(context.Background(), "crew1", &provider.CrewService{
		Name:    "redis",
		Image:   "redis:7-alpine",
		Command: command,
	})
	if err != nil {
		t.Fatalf("ensureSidecar: %v", err)
	}
	if id != "cid-redis" {
		t.Fatalf("unexpected container id %q", id)
	}
	if !captured {
		t.Fatal("container create was never called")
	}

	// The Docker create body must carry Cmd as a JSON array of the exact
	// three tokens — exec form, NOT a shell string.
	rawCmd, ok := createBody["Cmd"]
	if !ok {
		t.Fatalf("create body missing Cmd; body=%v", createBody)
	}
	arr, ok := rawCmd.([]any)
	if !ok {
		t.Fatalf("Cmd is not a JSON array (exec form) — got %T (%v); a shell string here would be an injection surface", rawCmd, rawCmd)
	}
	if len(arr) != 3 {
		t.Fatalf("Cmd length = %d, want 3 discrete tokens: %v", len(arr), arr)
	}
	got := make([]string, len(arr))
	for i, e := range arr {
		s, ok := e.(string)
		if !ok {
			t.Fatalf("Cmd[%d] is not a string: %T", i, e)
		}
		got[i] = s
	}
	if got[0] != "redis-server" || got[1] != "--requirepass" {
		t.Fatalf("Cmd prefix wrong: %v", got)
	}
	// The hostile value must be its OWN element, byte-for-byte, never
	// merged with a neighbour.
	if got[2] != hostileValue {
		t.Fatalf("requirepass arg mangled: %q != %q", got[2], hostileValue)
	}

	// Belt-and-braces: the metacharacters must never appear glued to the
	// flag in a single token anywhere in Cmd (which would signal a join).
	for _, tok := range got {
		if strings.Contains(tok, "--requirepass ") || tok == "redis-server --requirepass "+hostileValue {
			t.Fatalf("argv was shell-joined into a single token: %q", tok)
		}
	}
}

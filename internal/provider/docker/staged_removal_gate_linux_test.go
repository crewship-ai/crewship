//go:build linux

package docker

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/stagedstart"
)

func TestFailedRemovalRetainsStagedExecGateAfterSelectorRemoval(t *testing.T) {
	for _, teardown := range []bool{false, true} {
		for _, entry := range []string{"normal", "interactive", "external"} {
			name := entry
			if teardown {
				name += "/teardown"
			}
			t.Run(name, func(t *testing.T) {
				const id = "retained-runtime"
				rawCreates := 0
				p, closeProvider := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.Method == http.MethodDelete:
						w.WriteHeader(http.StatusConflict)
						_, _ = w.Write([]byte(`{"message":"removal refused"}`))
					case strings.HasSuffix(r.URL.Path, "/stop"):
						w.WriteHeader(http.StatusNoContent)
					case strings.HasSuffix(r.URL.Path, "/json"):
						_ = json.NewEncoder(w).Encode(map[string]any{
							"Id": id, "Image": "image", "State": map[string]any{"Running": true, "Status": "running", "StartedAt": "fresh-unsealed-boot"},
							"Config": map[string]any{"Labels": map[string]string{crewCrewIDLabel: "crew", stagedstart.Label: stagedstart.Version}},
						})
					case strings.HasSuffix(r.URL.Path, "/exec"):
						rawCreates++
						w.WriteHeader(http.StatusInternalServerError)
					default:
						t.Errorf("unexpected Docker call: %s %s", r.Method, r.URL.Path)
						w.WriteHeader(http.StatusInternalServerError)
					}
				})
				defer closeProvider()
				// Startup discovery is complete and the selector is now empty.
				// Remembered staged membership must continue to govern this object.
				p.cfg.EgressFenceCrews = nil
				p.stagedDiscoveryPending.Store(false)
				p.fencedCrew.Store(id, "crew")
				p.fenced.Store(id, "previous-boot")
				p.stagedVerified.Store(id, stagedVerification{nonce: "old", startedAt: "previous-boot"})
				if teardown {
					p.forceTeardown(t.Context(), id, "crew")
				} else if err := p.RemoveCrewRuntime(t.Context(), id); err == nil {
					t.Fatal("failed removal reported success")
				}
				var err error
				switch entry {
				case "normal":
					_, err = p.Exec(t.Context(), provider.ExecConfig{ContainerID: id, Cmd: []string{"/image-marker"}, User: "1001:1001"})
				case "interactive":
					_, err = p.ExecInteractive(t.Context(), provider.InteractiveExecConfig{ContainerID: id, Cmd: []string{"/image-marker"}, User: "1001:1001"})
				case "external":
					_, _, _, _, err = p.PrepareExternalExec(t.Context(), id, []string{"/image-marker"}, nil)
				}
				if err == nil || rawCreates != 0 {
					t.Errorf("retained unsealed runtime admitted image exec: error=%v raw-create=%d", err, rawCreates)
				}
				if _, present := p.fencedCrew.Load(id); !present {
					t.Error("failed removal discarded mandatory routing identity")
				}
				if _, present := p.fenced.Load(id); present {
					t.Error("failed removal retained obsolete fence proof")
				}
				if _, present := p.stagedVerified.Load(id); present {
					t.Error("failed removal retained obsolete staged proof")
				}
			})
		}
	}
}

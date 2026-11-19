package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/moby/moby/client"
)

func TestRemoteMiseResolverRejectsUnownedImagesBeforeBuilder(t *testing.T) {
	imageID := "sha256:" + strings.Repeat("a", 64)
	for _, mode := range []string{"foreign", "unlabelled", "feature", "mutable", "missing-instance", "wrong-image", "owned"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			creates := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if strings.Contains(r.URL.Path, "/images/") {
					id := imageID
					owner := strings.Repeat("1", 64)
					kind := devcontainer.ImageKindCrew
					switch mode {
					case "foreign":
						owner = strings.Repeat("2", 64)
					case "unlabelled":
						owner = ""
					case "feature":
						kind = devcontainer.ImageKindFeature
					case "wrong-image":
						id = "sha256:" + strings.Repeat("b", 64)
					}
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]any{"Id": id, "Os": "linux", "Architecture": "amd64", "Config": map[string]any{"Labels": map[string]string{devcontainer.ImageInstanceLabel: owner, devcontainer.ImageKindLabel: kind}}})
					return
				}
				creates++
				w.WriteHeader(500)
			}))
			defer srv.Close()
			cli, err := client.New(client.WithHost(srv.URL), client.WithAPIVersion("1.43"))
			if err != nil {
				t.Fatal(err)
			}
			defer cli.Close()
			p := &Provider{client: cli, cfg: Config{InstanceID: strings.Repeat("1", 64)}}
			selected := imageID
			if mode == "mutable" {
				selected = "crewship-cache:alias"
			}
			if mode == "missing-instance" {
				p.cfg.InstanceID = ""
			}
			result, err := p.ResolveCrewMiseLock(context.Background(), selected, &devcontainer.MiseConfig{Tools: map[string]string{"node": "22"}}, false)
			if err == nil || result != nil {
				t.Fatal("unexpected successful proposal")
			}
			if mode == "owned" {
				if creates != 1 || calls != 3 {
					t.Fatalf("owned image did not reach existing resolver: calls=%d creates=%d", calls, creates)
				}
			} else if creates != 0 {
				t.Fatal("untrusted image reached builder")
			}
			if (mode == "mutable" || mode == "missing-instance") && calls != 0 {
				t.Fatal("invalid identity contacted Docker")
			}
		})
	}
}

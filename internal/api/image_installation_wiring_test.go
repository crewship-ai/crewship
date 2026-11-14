package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

type installationWiringBuilder struct{ tag, dockerfile string }

func (*installationWiringBuilder) Available() bool { return true }
func (b *installationWiringBuilder) Build(_ context.Context, dir, tag string, _ func(string)) error {
	raw, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	b.tag, b.dockerfile = tag, string(raw)
	return err
}

// Exercise RouterOption -> routes -> handler -> provisioner -> build output,
// so dropping any link does not silently produce unowned images.
func TestRouterWiresImageInstallationIdentity(t *testing.T) {
	for _, id := range []string{"", strings.Repeat("a", 64)} {
		t.Run("identity-"+id, func(t *testing.T) {
			builder := &installationWiringBuilder{}
			r, err := NewRouter(setupTestDB(t), "image-installation-wiring-test-secret", newTestLogger(), WithInstallationID(id), WithImageBuilder(builder))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(r.provisioning.Stop)
			cfg := &devcontainer.Config{Image: "synthetic/base:1", ContainerEnv: map[string]string{"FOO": "bar"}}
			if _, err := r.provisioning.provisioner.Provision(t.Context(), cfg.Image, cfg, ""); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(builder.dockerfile, `LABEL "crewship.image.instance-id"="`+id+`"`) {
				t.Fatalf("identity lost in wiring: %s", builder.dockerfile)
			}
			if id != "" && !strings.HasPrefix(builder.tag, "crewship-cache:"+id+"-") {
				t.Fatalf("wrong installation namespace: %s", builder.tag)
			}
		})
	}
}

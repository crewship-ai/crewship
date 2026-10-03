//go:build integration

package devcontainer

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

func TestMiseResolveRealExactAndFloatingSelectors(t *testing.T) {
	image := os.Getenv("CREWSHIP_MISE_TEST_IMAGE")
	if image == "" {
		t.Fatal("set CREWSHIP_MISE_TEST_IMAGE for this explicit live gate")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	inspected, err := docker.ImageInspect(ctx, image)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &MiseConfig{Tools: map[string]string{"node": "22.0.0"}, Env: map[string]string{"PRIVATE_TOKEN": "SYNTHETIC_DO_NOT_DELIVER"}}
	exact, err := ResolveMiseLock(ctx, docker, inspected.ID, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if exact.ImageID != inspected.ID || exact.MiseVersion == "" || exact.Platform == "" || !strings.Contains(exact.Lock.Files["mise.lock"], `version = "22.0.0"`) {
		t.Fatalf("missing resolver evidence: %+v", exact)
	}
	cfg.Lock = exact.Lock
	pinned, err := ResolveMiseLock(ctx, docker, inspected.ID, cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pinned.Lock.Files["mise.lock"], `version = "22.0.0"`) {
		t.Fatal("explicit pin floated")
	}
	cfg.Tools["node"] = "22"
	floating, err := ResolveMiseLock(ctx, docker, inspected.ID, cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	lock := floating.Lock.Files["mise.lock"]
	if !strings.Contains(lock, `specifiers = ["22"]`) || strings.Contains(lock, `version = "22.0.0"`) || !strings.Contains(lock, "checksum = ") {
		t.Fatalf("floating update did not preserve selector/integrity: %s", lock)
	}
	for _, res := range []*MiseResolution{exact, pinned, floating} {
		for _, s := range res.Lock.Files {
			if strings.Contains(s, "SYNTHETIC_DO_NOT_DELIVER") {
				t.Fatal("environment leaked to lock")
			}
		}
	}
	t.Logf("resolver image=%s mise=%s platform=%s; exact pin kept, floating selector updated", exact.ImageID, exact.MiseVersion, exact.Platform)
}

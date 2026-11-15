//go:build integration

package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// Called by the existing mandatory real-Docker retention conformance fixture.
// No skips, pulls, global prune, application processes or credentials.
func testImageReferencesRealDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	d, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	var entropy [64]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		t.Fatal(err)
	}
	owner := hex.EncodeToString(entropy[:32])
	borrower := hex.EncodeToString(entropy[32:])
	base, err := d.ImageInspect(ctx, "alpine:3")
	if err != nil {
		t.Fatalf("integration requires prepared alpine:3: %v", err)
	}
	source, err := d.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: base.ID}, HostConfig: &container.HostConfig{NetworkMode: "none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var artifact string
	refsA := NewImageReferences(d, owner)
	refsB := NewImageReferences(d, borrower)
	empty := func(context.Context) ([]resourcelifecycle.ImageReference, error) { return nil, nil }
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		_ = refsA.Reconcile(cleanup, &resourcelifecycle.ImageUse{}, empty)
		_ = refsB.Reconcile(cleanup, &resourcelifecycle.ImageUse{}, empty)
		_, _ = d.ContainerRemove(cleanup, source.ID, client.ContainerRemoveOptions{})
		if artifact != "" {
			_, _ = d.ImageRemove(cleanup, artifact, client.ImageRemoveOptions{Force: false, PruneChildren: false})
		}
	})
	committed, err := d.ContainerCommit(ctx, source.ID, client.ContainerCommitOptions{
		Reference: "crewship-reference-fixture:" + owner,
		Changes: []string{
			`VOLUME ["/persisted"]`,
			`LABEL ` + devcontainer.ImageInstanceLabel + `=` + owner,
			`LABEL ` + devcontainer.ImageKindLabel + `=crew`,
			`LABEL ` + devcontainer.ImageAcquisitionKeyLabel + `=` + strings.Repeat("1", 64),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact = committed.ID
	wanted := []resourcelifecycle.ImageReference{{Ref: artifact, Required: true}}
	for _, p := range []*ImageReferences{refsA, refsB} {
		if _, err := p.Preserve(ctx, wanted); err != nil {
			t.Fatal(err)
		}
		pin, err := d.ContainerInspect(ctx, imageReferenceName(p.owner, artifact), client.ContainerInspectOptions{})
		if err != nil || pin.Container.State.Running || pin.Container.HostConfig.Tmpfs["/persisted"] == "" {
			t.Fatalf("pin not dormant or declared VOLUME not shadowed: %v", err)
		}
		for _, mount := range pin.Container.Mounts {
			if mount.Type == "volume" || mount.Type == "bind" {
				t.Fatalf("reference allocated persistent storage: %+v", mount)
			}
		}
	}
	remove := func() error {
		_, err := d.ImageRemove(ctx, artifact, client.ImageRemoveOptions{Force: false, PruneChildren: false})
		return err
	}
	if err := remove(); err == nil {
		t.Fatal("image removed while both installations selected it")
	}
	if err := refsA.Reconcile(ctx, &resourcelifecycle.ImageUse{}, empty); err != nil {
		t.Fatal(err)
	}
	if err := remove(); err == nil {
		t.Fatal("owner removed image still referenced by borrower")
	}
	// A restarted borrower must recover the existing deterministic pin.
	refsB = NewImageReferences(d, borrower)
	if _, err := refsB.Preserve(ctx, wanted); err != nil {
		t.Fatal(err)
	}
	borrowerGate := &resourcelifecycle.ImageUse{}
	release, err := borrowerGate.UseImages(ctx, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := refsB.Reconcile(ctx, borrowerGate, empty); err != nil {
		release()
		t.Fatal(err)
	}
	if err := remove(); err == nil {
		release()
		t.Fatal("reconciliation removed an actively acquired reference")
	}
	release()
	if err := refsB.Reconcile(ctx, borrowerGate, empty); err != nil {
		t.Fatal(err)
	}
	if err := remove(); err != nil {
		t.Fatalf("image not collectable after both installations release: %v", err)
	}
	artifact = ""
}

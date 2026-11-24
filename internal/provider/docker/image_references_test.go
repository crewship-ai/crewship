package docker

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
)

const referenceOwner = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const referenceBorrower = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type referenceFake struct {
	images     map[string]client.ImageInspectResult
	containers map[string]client.ContainerInspectResult
	creates    []client.ContainerCreateOptions
	removed    []string
	listErr    error
	createErr  error
	afterList  func()
}

func (*referenceFake) ServerVersion(context.Context, client.ServerVersionOptions) (client.ServerVersionResult, error) {
	return client.ServerVersionResult{Components: []system.ComponentVersion{{Name: "Engine"}}}, nil
}

func referenceFixture(t *testing.T) (*referenceFake, string) {
	t.Helper()
	id := "sha256:" + strings.Repeat("1", 64)
	var img client.ImageInspectResult
	// Reuse the SDK JSON shape without depending on its embedded OCI config.
	img.ID = id
	img.Config = newImageReferenceConfig(referenceOwner)
	img.Config.Volumes = map[string]struct{}{"/persisted": {}}
	return &referenceFake{images: map[string]client.ImageInspectResult{id: img}, containers: map[string]client.ContainerInspectResult{}}, id
}

func (f *referenceFake) ImageInspect(_ context.Context, ref string, _ ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	img, ok := f.images[ref]
	if !ok {
		return img, cerrdefs.ErrNotFound
	}
	return img, nil
}

func (f *referenceFake) ContainerCreate(_ context.Context, o client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	if f.createErr != nil {
		return client.ContainerCreateResult{}, f.createErr
	}
	id := strings.Repeat("c", 64)
	if len(f.creates) > 0 {
		id = strings.Repeat("d", 64)
	}
	f.creates = append(f.creates, o)
	var c client.ContainerInspectResult
	c.Container.ID = id
	c.Container.Name = "/" + o.Name
	c.Container.Config = o.Config
	c.Container.Image = o.Config.Image
	c.Container.State = &container.State{Status: "created"}
	f.containers[id] = c
	return client.ContainerCreateResult{ID: id}, nil
}

func (f *referenceFake) ContainerInspect(_ context.Context, ref string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	for id, c := range f.containers {
		if ref == id || ref == strings.TrimPrefix(c.Container.Name, "/") {
			return c, nil
		}
	}
	return client.ContainerInspectResult{}, cerrdefs.ErrNotFound
}

func (f *referenceFake) ContainerList(_ context.Context, _ client.ContainerListOptions) (client.ContainerListResult, error) {
	if f.listErr != nil {
		return client.ContainerListResult{}, f.listErr
	}
	var out client.ContainerListResult
	for id := range f.containers {
		out.Items = append(out.Items, container.Summary{ID: id, Names: []string{f.containers[id].Container.Name}})
	}
	slices.SortFunc(out.Items, func(a, b container.Summary) int { return strings.Compare(a.ID, b.ID) })
	if f.afterList != nil {
		f.afterList()
	}
	return out, nil
}

func (f *referenceFake) ContainerRemove(_ context.Context, id string, o client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	if o.Force || o.RemoveVolumes {
		return client.ContainerRemoveResult{}, errors.New("unsafe container removal")
	}
	f.removed = append(f.removed, id)
	delete(f.containers, id)
	return client.ContainerRemoveResult{}, nil
}

func TestImageReferencesPreserveExactIDWithoutVolumesOrRuntimeOwnership(t *testing.T) {
	f, id := referenceFixture(t)
	p := NewImageReferences(f, referenceBorrower)
	refs := []resourcelifecycle.ImageReference{{Ref: id, Required: true}}
	for range 2 {
		resolved, err := p.Preserve(t.Context(), refs)
		if err != nil || resolved[id] != id {
			t.Fatalf("preserve: %v %v", resolved, err)
		}
	}
	if len(f.creates) != 1 {
		t.Fatalf("created %d reference containers", len(f.creates))
	}
	o := f.creates[0]
	if o.Config.Image != id || o.Config.Healthcheck.Test[0] != "NONE" || !o.Config.NetworkDisabled ||
		o.HostConfig.NetworkMode != "none" || !o.HostConfig.ReadonlyRootfs || o.HostConfig.Tmpfs["/persisted"] == "" ||
		o.Config.Labels[resourcelifecycle.InstanceLabel] != "" || o.Config.Labels["crewship.kind"] != "" ||
		o.Config.Labels[devcontainer.TempContainerLabelKey] != "" {
		t.Fatalf("unsafe reference container: %+v %+v", o.Config, o.HostConfig)
	}
	// A distinct installation must have its own daemon-visible reference.
	if _, err := NewImageReferences(f, referenceOwner).Preserve(t.Context(), refs); err != nil {
		t.Fatal(err)
	}
	if len(f.creates) != 2 || f.creates[0].Name == f.creates[1].Name {
		t.Fatal("installations shared a reference container")
	}
}

func TestImageReferencesMissingSelectedFailsButFutureOutputDoesNot(t *testing.T) {
	f, id := referenceFixture(t)
	delete(f.images, id)
	p := NewImageReferences(f, referenceBorrower)
	if _, err := p.Preserve(t.Context(), []resourcelifecycle.ImageReference{{Ref: id, Required: true}}); !errors.Is(err, cerrdefs.ErrNotFound) {
		t.Fatalf("missing selected environment must fail explicitly: %v", err)
	}
	if _, err := p.Preserve(t.Context(), []resourcelifecycle.ImageReference{{Ref: "crewship-cache:new-output", BuildInput: true}}); err != nil {
		t.Fatalf("future output prevented initial build: %v", err)
	}
	// Ordinary configuration can be saved without querying unavailable Docker.
	if _, err := NewImageReferences(nil, referenceBorrower).Preserve(t.Context(), []resourcelifecycle.ImageReference{{Ref: "ubuntu:24.04"}}); err != nil {
		t.Fatalf("public base save requires Docker: %v", err)
	}
}

func TestImageReferencesCreationRaceAndForeignNameCollisionFailClosed(t *testing.T) {
	f, id := referenceFixture(t)
	f.createErr = cerrdefs.ErrNotFound // owner deleted between inspect/create
	if _, err := NewImageReferences(f, referenceBorrower).Preserve(t.Context(), []resourcelifecycle.ImageReference{{Ref: id, Required: true}}); err == nil {
		t.Fatal("lost create race was accepted")
	}
	f.createErr = nil
	var collision client.ContainerInspectResult
	collision.Container.Name = "/" + imageReferenceName(referenceBorrower, id)
	f.containers["foreign"] = collision
	if _, err := NewImageReferences(f, referenceBorrower).Preserve(t.Context(), []resourcelifecycle.ImageReference{{Ref: id, Required: true}}); err == nil {
		t.Fatal("foreign deterministic name collision was adopted")
	}
	if len(f.removed) != 0 || len(f.creates) != 0 {
		t.Fatal("conflicting resources were mutated")
	}
}

func TestImageReferencesStrictAdmissionRechecksDisappearedDaemonEvidence(t *testing.T) {
	for _, missing := range []string{"pin", "image"} {
		t.Run(missing, func(t *testing.T) {
			f, id := referenceFixture(t)
			p := NewImageReferences(f, referenceBorrower)
			refs := []resourcelifecycle.ImageReference{{Ref: id, Required: true}}
			if _, err := p.Preserve(t.Context(), refs); err != nil {
				t.Fatal(err)
			}
			f.containers = map[string]client.ContainerInspectResult{}
			if missing == "image" {
				delete(f.images, id)
			}
			_, err := p.Preserve(t.Context(), refs)
			if missing == "image" {
				if !errors.Is(err, cerrdefs.ErrNotFound) {
					t.Fatalf("cached knowledge hid missing selected image: %v", err)
				}
			} else if err != nil || len(f.creates) != 2 {
				t.Fatalf("missing pin was not repaired: creates=%d error=%v", len(f.creates), err)
			}
		})
	}
}

func TestImageReferencesRejectMutableCrossInstallationBuildInput(t *testing.T) {
	f, id := referenceFixture(t)
	alias := "crewship-cache:" + referenceOwner + "-111111111111"
	f.images[alias] = f.images[id]
	p := NewImageReferences(f, referenceBorrower)
	if _, err := p.Preserve(t.Context(), []resourcelifecycle.ImageReference{{Ref: alias, BuildInput: true}}); err == nil {
		t.Fatal("build accepted a foreign mutable alias after pinning another ID")
	}
	resolved, err := p.Preserve(t.Context(), []resourcelifecycle.ImageReference{{Ref: alias, Required: true}})
	if err != nil || resolved[alias] != id {
		t.Fatalf("runtime did not receive exact pinned ID: %v %v", resolved, err)
	}
}

func TestImageReferencesReconcileDeferScope(t *testing.T) {
	for _, mode := range []string{"missing image", "running container", "selected running container", "active build", "incomplete inventory", "selection recheck fails"} {
		t.Run(mode, func(t *testing.T) {
			f, first := referenceFixture(t)
			second := "sha256:" + strings.Repeat("2", 64)
			img := f.images[first]
			img.ID = second
			f.images[second] = img
			p := NewImageReferences(f, referenceBorrower)
			if _, err := p.Preserve(t.Context(), []resourcelifecycle.ImageReference{{Ref: first, Required: true}, {Ref: second, Required: true}}); err != nil {
				t.Fatal(err)
			}
			firstContainer := strings.Repeat("c", 64)
			secondContainer := strings.Repeat("d", 64)
			gate := &resourcelifecycle.ImageUse{}
			reads := 0
			switch mode {
			case "missing image":
				delete(f.images, first)
			case "running container", "selected running container":
				f.containers[firstContainer].Container.State.Running = true
				f.containers[firstContainer].Container.State.Status = "running"
			case "active build":
				release, err := gate.UseImages(t.Context(), first)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			case "incomplete inventory":
				f.listErr = errors.New("incomplete list")
			}
			err := p.Reconcile(t.Context(), gate, func(context.Context) ([]resourcelifecycle.ImageReference, error) {
				reads++
				if mode == "selected running container" {
					return []resourcelifecycle.ImageReference{{Ref: first}}, nil
				}
				if mode == "selection recheck fails" && reads == 2 {
					return nil, errors.New("incomplete selection")
				}
				return nil, nil
			})
			var partial *ReferenceSweepError
			switch mode {
			case "incomplete inventory", "selection recheck fails":
				if err == nil || len(f.removed) != 0 {
					t.Fatalf("global failure: removed=%v err=%v", f.removed, err)
				}
				if mode == "selection recheck fails" && (!errors.As(err, &partial) || partial.Deferred != 2 || reads != 2) {
					t.Fatalf("failed recheck: reads=%d err=%v", reads, err)
				}
				return
			case "missing image", "running container", "selected running container":
				if !errors.As(err, &partial) || partial.Deferred != 1 {
					t.Fatalf("expected one deferred reference: %v", err)
				}
			case "active build":
				if err != nil {
					t.Fatal(err)
				}
			}
			if !slices.Equal(f.removed, []string{secondContainer}) {
				t.Fatalf("removed %v", f.removed)
			}
		})
	}
}

func TestImageReferencesReconcileChecksFreshSelectionAndAcquisition(t *testing.T) {
	for _, mode := range []string{"late selection", "active build", "unreferenced", "inventory failure", "database failure"} {
		t.Run(mode, func(t *testing.T) {
			f, id := referenceFixture(t)
			p := NewImageReferences(f, referenceBorrower)
			if _, err := p.Preserve(t.Context(), []resourcelifecycle.ImageReference{{Ref: id, Required: true}}); err != nil {
				t.Fatal(err)
			}
			gate := &resourcelifecycle.ImageUse{}
			var desired []resourcelifecycle.ImageReference
			if mode == "late selection" {
				f.afterList = func() { desired = []resourcelifecycle.ImageReference{{Ref: id, Required: true}} }
			}
			if mode == "active build" {
				release, err := gate.UseImages(t.Context(), id)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			if mode == "inventory failure" {
				f.listErr = errors.New("Docker unavailable")
			}
			err := p.Reconcile(t.Context(), gate, func(context.Context) ([]resourcelifecycle.ImageReference, error) {
				if mode == "database failure" {
					return nil, errors.New("database unavailable")
				}
				return desired, nil
			})
			if (mode == "inventory failure" || mode == "database failure") && err == nil {
				t.Fatal("incomplete evidence was accepted")
			}
			want := 0
			if mode == "unreferenced" {
				want = 1
			}
			if len(f.removed) != want {
				t.Fatalf("removed %d references, want %d: %v", len(f.removed), want, err)
			}
		})
	}
}

// Reference config shape is intentionally shared with real ImageInspect JSON.
func newImageReferenceConfig(owner string) *dockerspec.DockerOCIImageConfig {
	c := &dockerspec.DockerOCIImageConfig{}
	c.Labels = map[string]string{devcontainer.ImageInstanceLabel: owner, devcontainer.ImageKindLabel: devcontainer.ImageKindCrew}
	return c
}

func TestImageReferencesRetainUnlabelledLegacySelectionsWithoutClaimingOwnership(t *testing.T) {
	f, id := referenceFixture(t)
	img := f.images[id]
	img.Config.Labels = nil
	f.images[id] = img
	if _, err := NewImageReferences(f, referenceBorrower).Preserve(t.Context(), []resourcelifecycle.ImageReference{{Ref: id, Required: true}}); err != nil {
		t.Fatal(err)
	}
	if len(f.creates) != 1 || f.creates[0].Config.Image != id {
		t.Fatal("legacy exact selection was left invisible to non-force deletion")
	}
	if len(f.images[id].Config.Labels) != 0 {
		t.Fatal("reference changed source image provenance")
	}
}

type podmanReferenceFake struct{ *referenceFake }

func (*podmanReferenceFake) ServerVersion(context.Context, client.ServerVersionOptions) (client.ServerVersionResult, error) {
	return client.ServerVersionResult{Components: []system.ComponentVersion{{Name: "Podman Engine"}}}, nil
}
func TestPodmanReferenceWithDisplayTag(t *testing.T) {
	f, id := referenceFixture(t)
	tag := "localhost/crewship-cache:sample"
	img := f.images[id]
	img.RepoTags = []string{tag}
	f.images[id] = img
	refs := []resourcelifecycle.ImageReference{{Ref: id, Required: true}}
	p := NewImageReferences(&podmanReferenceFake{f}, referenceBorrower)
	if _, err := p.Preserve(t.Context(), refs); err != nil {
		t.Fatal(err)
	}
	for key, ref := range f.containers {
		ref.Container.Image = strings.TrimPrefix(id, "sha256:")
		ref.Container.Config.Image = tag
		f.containers[key] = ref
	}
	if _, err := p.Preserve(t.Context(), refs); err != nil {
		t.Fatalf("verified image with Podman display tag: %v", err)
	}
	img.RepoTags = nil
	f.images[id] = img
	if _, err := p.Preserve(t.Context(), refs); err != nil {
		t.Fatalf("retag broke the retained revision: %v", err)
	}
	gate := &resourcelifecycle.ImageUse{}
	if err := p.Reconcile(t.Context(), gate, func(context.Context) ([]resourcelifecycle.ImageReference, error) { return refs, nil }); err != nil || len(f.removed) != 0 {
		t.Fatalf("reconcile retained revision: %v", err)
	}
	if _, err := NewImageReferences(f, referenceBorrower).Preserve(t.Context(), refs); err == nil {
		t.Fatal("accepted display tag without Podman evidence")
	}
	for key, ref := range f.containers {
		ref.Container.Image = strings.Repeat("b", 64)
		f.containers[key] = ref
	}
	if _, err := p.Preserve(t.Context(), refs); err == nil {
		t.Fatal("display tag overrode immutable image mismatch")
	}
}

// Reproduce the compat engine returning a display name on the first
// post-create inspect. Only the version probe before create can complete.
type slowReferencePodman struct {
	*referenceFake
	initialErr error
	probes     int
	display    string
}

func (f *slowReferencePodman) ServerVersion(context.Context, client.ServerVersionOptions) (client.ServerVersionResult, error) {
	f.probes++
	if f.initialErr != nil {
		return client.ServerVersionResult{}, f.initialErr
	}
	if f.probes > 1 {
		return client.ServerVersionResult{}, context.DeadlineExceeded
	}
	return client.ServerVersionResult{Components: []system.ComponentVersion{{Name: "Podman Engine"}}}, nil
}

func (f *slowReferencePodman) ContainerInspect(ctx context.Context, id string, opts client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	c, err := f.referenceFake.ContainerInspect(ctx, id, opts)
	if err == nil {
		cfg := *c.Container.Config
		cfg.Image = f.display
		c.Container.Config = &cfg
	}
	return c, err
}

func TestPodmanReferenceConfirmationAfterSlowCreate(t *testing.T) {
	f, id := referenceFixture(t)
	const display = "docker.io/library/crewship-cache:synthetic"
	img := f.images[id]
	img.RepoTags = []string{display}
	f.images[id] = img
	daemon := &slowReferencePodman{referenceFake: f, display: display}
	p := NewImageReferences(daemon, referenceBorrower)
	result, err := p.Preserve(t.Context(), []resourcelifecycle.ImageReference{{Ref: id, Required: true}})
	if err != nil || result[id] != id || daemon.probes != 1 || len(f.creates) != 1 {
		t.Fatalf("confirmation: %v probes=%d creates=%d: %v", result, daemon.probes, len(f.creates), err)
	}
}

func TestPodmanReferenceInitialProbeFailureLeavesNoContainer(t *testing.T) {
	f, id := referenceFixture(t)
	const tag = "docker.io/library/crewship-cache:probe-retry"
	img := f.images[id]
	img.RepoTags = []string{tag}
	f.images[id] = img
	offline := errors.New("version service unavailable")
	daemon := &slowReferencePodman{referenceFake: f, display: tag, initialErr: offline}
	p := NewImageReferences(daemon, referenceBorrower)
	refs := []resourcelifecycle.ImageReference{{Ref: id, Required: true}}
	if _, err := p.Preserve(t.Context(), refs); !errors.Is(err, offline) || len(f.creates) != 0 {
		t.Fatalf("initial probe: creates=%d err=%v", len(f.creates), err)
	}
	daemon.initialErr = nil
	daemon.probes = 0
	if _, err := p.Preserve(t.Context(), refs); err != nil || len(f.creates) != 1 || f.creates[0].Config.Labels[imageReferenceTagsLabel] != tag {
		t.Fatalf("retry: creates=%d err=%v", len(f.creates), err)
	}
}

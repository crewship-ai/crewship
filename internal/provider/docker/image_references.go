package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/dockerutil"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

const imageReferenceOwnerLabel = "crewship.image.reference.instance-id"
const imageReferenceIDLabel = "crewship.image.reference.image-id"
const imageReferenceNamePrefix = "crewship-image-ref-"

type ImageReferenceClient interface {
	ImageInspect(context.Context, string, ...client.ImageInspectOption) (client.ImageInspectResult, error)
	ContainerCreate(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerList(context.Context, client.ContainerListOptions) (client.ContainerListResult, error)
	ContainerRemove(context.Context, string, client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
}

// ImageReferences enrolls immutable environments with the daemon. Reference
// containers are never started, share image layers and hold no application data.
// Docker's non-force image removal sees these references across installations.
type ImageReferences struct {
	client ImageReferenceClient
	owner  string
	mu     sync.Mutex
}

func NewImageReferences(cli ImageReferenceClient, owner string) *ImageReferences {
	return &ImageReferences{client: cli, owner: devcontainer.ImageNamespace(owner)}
}

func imageReferenceName(owner, id string) string {
	return fmt.Sprintf("%s%x", imageReferenceNamePrefix, sha256.Sum256([]byte(owner+"\x00"+id)))
}

func (p *ImageReferences) Preserve(ctx context.Context, refs []resourcelifecycle.ImageReference) (map[string]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	resolved := map[string]string{}
	for _, ref := range refs {
		if ref.Ref == "" {
			continue
		}
		// A public/foreign repository alias already excludes its image from
		// managed GC. Ordinary configuration saves must not require Docker.
		if !ref.Required && !dockerutil.IsLocalImageID(ref.Ref) &&
			!strings.HasPrefix(ref.Ref, "crewship-cache:") && !strings.HasPrefix(ref.Ref, devcontainer.FeatureImageTagPrefix) {
			continue
		}
		stepCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		id, err := p.preserve(stepCtx, ref)
		cancel()
		if err != nil {
			return nil, err
		}
		if id != "" {
			resolved[ref.Ref] = id
		}
	}
	return resolved, nil
}

func (p *ImageReferences) preserve(ctx context.Context, ref resourcelifecycle.ImageReference) (string, error) {
	if p.client == nil || p.owner == "" {
		return "", fmt.Errorf("image reference protection has no installation/runtime")
	}
	img, err := p.client.ImageInspect(ctx, ref.Ref)
	if err != nil {
		if cerrdefs.IsNotFound(err) && !ref.Required && !(ref.BuildInput && dockerutil.IsLocalImageID(ref.Ref)) {
			return "", nil // future output or not-yet-pulled base
		}
		return "", fmt.Errorf("selected environment %q unavailable; restore its image or explicitly rebuild: %w", ref.Ref, err)
	}
	if !dockerutil.IsLocalImageID(img.ID) || img.Config == nil {
		return "", fmt.Errorf("image %q has incomplete immutable identity/configuration", ref.Ref)
	}
	labels := img.Config.Labels
	if ref.BuildInput && devcontainer.ImageNamespace(labels[devcontainer.ImageInstanceLabel]) != "" &&
		labels[devcontainer.ImageInstanceLabel] != p.owner && ref.Ref != img.ID && !strings.Contains(ref.Ref, "@sha256:") {
		return "", fmt.Errorf("cross-installation build input %q is mutable; use an immutable image ID or registry digest", ref.Ref)
	}
	// A reference proves this installation needs bytes, not that it owns
	// their provenance. Retain exact legacy/unlabelled selections as well.

	name := imageReferenceName(p.owner, img.ID)
	existing, err := p.client.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err == nil {
		return img.ID, p.validate(existing, img.ID)
	}
	if !cerrdefs.IsNotFound(err) {
		return "", fmt.Errorf("inspect environment reference: %w", err)
	}
	// ContainerCreate inherits declared image VOLUMEs even without a start.
	// Explicit tmpfs destinations suppress anonymous volume allocation. No
	// tmpfs is mounted because this container is never started.
	tmpfs := map[string]string{}
	for destination := range img.Config.Volumes {
		if !path.IsAbs(destination) || path.Clean(destination) != destination || destination == "/" {
			return "", fmt.Errorf("cannot retain image with unsafe volume destination %q", destination)
		}
		tmpfs[destination] = "ro,nosuid,nodev,noexec,size=4096"
	}
	created, err := p.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image: img.ID, Entrypoint: []string{"/bin/false"}, Cmd: []string{"crewship-image-reference"},
			User: "1001:1001", NetworkDisabled: true,
			Healthcheck: &container.HealthConfig{Test: []string{"NONE"}},
			Labels: map[string]string{
				imageReferenceOwnerLabel: p.owner, imageReferenceIDLabel: img.ID,
				resourcelifecycle.InstanceLabel: "", "crewship.kind": "", "crewship.crew-id": "",
				devcontainer.TempContainerLabelKey: "",
			},
		},
		HostConfig: &container.HostConfig{
			NetworkMode: "none", ReadonlyRootfs: true, Tmpfs: tmpfs,
			CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges:true"},
		},
	})
	if err != nil {
		// A timeout/conflict may mean the deterministic create succeeded. Its
		// immutable labels/ID/state, not the name alone, establish ownership.
		existing, inspectErr := p.client.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
		if inspectErr != nil {
			return "", fmt.Errorf("retain exact environment %s: %w", img.ID, err)
		}
		return img.ID, p.validate(existing, img.ID)
	}
	existing, err = p.client.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("confirm environment reference: %w", err)
	}
	return img.ID, p.validate(existing, img.ID)
}

func (p *ImageReferences) validate(result client.ContainerInspectResult, id string) error {
	c := result.Container
	raw, idErr := hex.DecodeString(c.ID)
	if idErr != nil || len(raw) != 32 || c.ID != strings.ToLower(c.ID) || c.Config == nil || c.State == nil || c.State.Running || c.State.Status != "created" ||
		c.Image != id || c.Config.Image != id ||
		c.Config.Labels[imageReferenceOwnerLabel] != p.owner || c.Config.Labels[imageReferenceIDLabel] != id ||
		strings.TrimPrefix(c.Name, "/") != imageReferenceName(p.owner, id) {
		return fmt.Errorf("environment reference identity/state mismatch for %q; retaining conflicting container", c.ID)
	}
	for _, m := range c.Mounts {
		if m.Type == "volume" || m.Type == "bind" {
			return fmt.Errorf("environment reference unexpectedly allocated persistent storage")
		}
	}
	return nil
}

// ReferenceSweepError reports candidates deferred after inventory completed.
type ReferenceSweepError struct {
	Deferred int
	Cause    error
}

func (e *ReferenceSweepError) Error() string { return e.Cause.Error() }
func (e *ReferenceSweepError) Unwrap() error { return e.Cause }

// Reconcile removes only proven own references no longer selected or acquired.
// Inventory is outside the lifecycle gate; each removal rereads desired refs
// and reserves the artifact's current IDs/aliases under a short final gate.
func (p *ImageReferences) Reconcile(ctx context.Context, gate *resourcelifecycle.ImageUse, desired func(context.Context) ([]resourcelifecycle.ImageReference, error)) error {
	if p.client == nil || p.owner == "" {
		return fmt.Errorf("image reference reconciliation has no installation/runtime")
	}
	refs, err := desired(ctx)
	if err != nil {
		return err
	}
	// Missing historical/current references cannot protect existing bytes.
	// Dispatch/restore still require the current artifact, never rebuilding it.
	for _, ref := range refs {
		ref.Required = false
		if _, err := p.Preserve(ctx, []resourcelifecycle.ImageReference{ref}); err != nil {
			return err
		}
	}
	listed, err := p.client.ContainerList(ctx, client.ContainerListOptions{All: true,
		Filters: make(client.Filters).Add("label", imageReferenceOwnerLabel+"="+p.owner)})
	if err != nil {
		return err
	}
	var partial ReferenceSweepError
	for i, candidate := range listed.Items {
		if err := ctx.Err(); err != nil {
			return &ReferenceSweepError{Deferred: partial.Deferred + len(listed.Items) - i, Cause: err}
		}
		err := p.releaseUnused(ctx, gate, candidate.ID, desired)
		if err == nil {
			continue
		}
		var global *referenceInventoryError
		if errors.As(err, &global) {
			return &ReferenceSweepError{Deferred: partial.Deferred + len(listed.Items) - i, Cause: err}
		}
		partial.Deferred++
		if partial.Cause == nil {
			partial.Cause = fmt.Errorf("reference %q: %w", candidate.ID, err)
		}
	}
	if partial.Deferred != 0 {
		return &partial
	}
	return nil
}

// A failure obtaining shared evidence invalidates the remaining pass.
type referenceInventoryError struct{ error }

func (e *referenceInventoryError) Unwrap() error { return e.error }

func (p *ImageReferences) releaseUnused(ctx context.Context, gate *resourcelifecycle.ImageUse, containerID string, desired func(context.Context) ([]resourcelifecycle.ImageReference, error)) error {
	release, ok := gate.TryCollect()
	if !ok {
		return &referenceInventoryError{errors.New("image collection gate busy")}
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if !p.mu.TryLock() {
		return &referenceInventoryError{errors.New("image reference protection busy")}
	}
	defer p.mu.Unlock()
	c, err := p.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return err
	}
	id := c.Container.Image
	if err := p.validate(c, id); err != nil {
		return err
	}
	img, err := p.client.ImageInspect(ctx, id)
	if err != nil {
		return err
	}
	if img.Config == nil {
		return fmt.Errorf("image reference has incomplete image configuration")
	}
	aliases := append([]string{id, devcontainer.ImageAcquisitionRef(img.Config.Labels)}, img.RepoTags...)
	releaseImage, idle := gate.TryCollectImages(aliases...)
	if !idle {
		return nil
	}
	defer releaseImage()
	refs, err := desired(ctx)
	if err != nil {
		return &referenceInventoryError{err}
	}
	for _, ref := range refs {
		for _, alias := range aliases {
			if ref.Ref != "" && ref.Ref == alias {
				return nil
			}
		}
	}
	if _, err := p.client.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: false, RemoveVolumes: false}); err != nil {
		return err
	}
	return nil
}

// SetImageReferenceAcquirer is called once while the server wires routes,
// before either the crew starter or durable service controller admits work.
func (p *Provider) SetImageReferenceAcquirer(acquire provider.ImageReferenceAcquirer) {
	p.imageReferences = acquire
}

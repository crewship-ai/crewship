package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"slices"
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
const imageReferenceTagsLabel = "crewship.image.reference.config-tags"
const imageReferenceDigestsLabel = "crewship.image.reference.repo-digests"
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
			!strings.HasPrefix(devcontainer.CanonicalCacheRef(ref.Ref), "crewship-cache:") && !strings.HasPrefix(devcontainer.CanonicalCacheRef(ref.Ref), devcontainer.FeatureImageTagPrefix) {
			continue
		}
		stepCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		id, err := p.preserve(stepCtx, ref)
		cancel()
		if err != nil {
			if id != "" {
				return nil, &referenceValidationError{id, err}
			}
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
	img.ID = dockerutil.ConfigImageID(img.ID)
	if img.ID == "" || img.Config == nil {
		return "", fmt.Errorf("image %q has incomplete immutable identity/configuration", ref.Ref)
	}
	labels := img.Config.Labels
	if ref.BuildInput && devcontainer.ImageNamespace(labels[devcontainer.ImageInstanceLabel]) != "" &&
		labels[devcontainer.ImageInstanceLabel] != p.owner && ref.Ref != img.ID && !strings.Contains(ref.Ref, "@sha256:") {
		return "", fmt.Errorf("cross-installation build input %q is mutable; use an immutable image ID or registry digest", ref.Ref)
	}
	// A reference proves this installation needs bytes, not that it owns
	// their provenance. Retain exact legacy/unlabelled selections as well.

	// Scope engine evidence to this operation and endpoint. A second version
	// probe after a slow create can consume the remaining deadline and turn
	// a verified Podman display tag into a spurious image mismatch.
	isPodman := sync.OnceValues(func() (bool, error) { return dockerutil.DetectPodman(ctx, p.client) })
	name := imageReferenceName(p.owner, img.ID)
	existing, err := p.client.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err == nil {
		return img.ID, p.validate(ctx, existing, img.ID, img.RepoTags, isPodman)
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
	referenceLabels := map[string]string{
		imageReferenceOwnerLabel: p.owner, imageReferenceIDLabel: img.ID,
		imageReferenceDigestsLabel:      strings.Join(img.RepoDigests, "\n"),
		resourcelifecycle.InstanceLabel: "", "crewship.kind": "", "crewship.crew-id": "",
		devcontainer.TempContainerLabelKey: "",
	}
	podman, err := isPodman()
	if err != nil {
		return "", fmt.Errorf("identify engine before creating environment reference: %w", err)
	}
	if podman {
		// Retagging the image does not change Config.Image of an existing
		// reference container. Keep the names inspected at creation.
		referenceLabels[imageReferenceTagsLabel] = strings.Join(img.RepoTags, "\n")
	}
	created, err := dockerutil.CreateContainer(ctx, p.client, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image: img.ID, Entrypoint: []string{"/bin/false"}, Cmd: []string{"crewship-image-reference"},
			User: "1001:1001", NetworkDisabled: true,
			Healthcheck: &container.HealthConfig{Test: []string{"NONE"}},
			Labels:      referenceLabels,
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
		return img.ID, p.validate(ctx, existing, img.ID, img.RepoTags, isPodman)
	}
	existing, err = p.client.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("confirm environment reference: %w", err)
	}
	return img.ID, p.validate(ctx, existing, img.ID, img.RepoTags, isPodman)
}

func (p *ImageReferences) validate(ctx context.Context, result client.ContainerInspectResult, imageID string, imageTags []string, isPodman func() (bool, error)) error {
	c := result.Container
	id := dockerutil.ConfigImageID(imageID)
	raw, idErr := hex.DecodeString(c.ID)
	if idErr != nil || len(raw) != 32 || c.ID != strings.ToLower(c.ID) || c.Config == nil || c.State == nil || c.State.Running || c.State.Status != "created" ||
		id == "" || dockerutil.ConfigImageID(c.Image) != id ||
		c.Config.Labels[imageReferenceOwnerLabel] != p.owner || c.Config.Labels[imageReferenceIDLabel] != id ||
		strings.TrimPrefix(c.Name, "/") != imageReferenceName(p.owner, id) {
		return fmt.Errorf("environment reference identity/state mismatch for %q; retaining conflicting container", c.ID)
	}
	// Podman may report a display tag in Config.Image even when create used
	// the config ID. Accept only names observed on this same image; a cache
	// rebuild may already have moved the display tag to a later revision.
	if dockerutil.ConfigImageID(c.Config.Image) != id {
		display := c.Config.Image
		recorded := strings.Split(c.Config.Labels[imageReferenceTagsLabel], "\n")
		podman, err := isPodman()
		if err != nil {
			return fmt.Errorf("identify engine for environment reference %q: %w", c.ID, err)
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("confirm configured environment image for %q: %w", c.ID, err)
		}
		if display == "" || !podman || !(matchesReference(imageTags, display) || matchesReference(recorded, display)) {
			return fmt.Errorf("environment reference configured image mismatch for %q", c.ID)
		}
	}
	for _, m := range c.Mounts {
		if m.Type == "volume" || m.Type == "bind" {
			return fmt.Errorf("environment reference unexpectedly allocated persistent storage")
		}
	}
	return nil
}

// The image was resolved, but its deterministic reference failed validation.
type referenceValidationError struct {
	imageID string
	error
}

func (e *referenceValidationError) Unwrap() error { return e.error }

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
	var partial ReferenceSweepError
	deferredNames := map[string]bool{}
	var protected []resourcelifecycle.ImageReference
	for _, ref := range refs {
		ref.Required = false
		if _, err := p.Preserve(ctx, []resourcelifecycle.ImageReference{ref}); err != nil {
			var invalid *referenceValidationError
			if !errors.As(err, &invalid) {
				return errors.Join(partial.Cause, err)
			}
			name := imageReferenceName(p.owner, invalid.imageID)
			if !deferredNames[name] {
				deferredNames[name] = true
				protected = append(protected, resourcelifecycle.ImageReference{Ref: invalid.imageID})
				partial.Deferred++
				if partial.Cause == nil {
					partial.Cause = fmt.Errorf("selected image %q: %w", invalid.imageID, err)
				}
			}
		}
	}
	listed, err := p.client.ContainerList(ctx, client.ContainerListOptions{All: true,
		Filters: make(client.Filters).Add("label", imageReferenceOwnerLabel+"="+p.owner)})
	if err != nil {
		return errors.Join(partial.Cause, err)
	}
	candidates := slices.DeleteFunc(listed.Items, func(c container.Summary) bool {
		return slices.ContainsFunc(c.Names, func(name string) bool { return deferredNames[strings.TrimPrefix(name, "/")] })
	})
	currentSelection := func(ctx context.Context) ([]resourcelifecycle.ImageReference, error) {
		current, err := desired(ctx)
		if err != nil {
			return nil, err
		}
		return append(current, protected...), nil
	}
	for i, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return &ReferenceSweepError{Deferred: partial.Deferred + len(candidates) - i, Cause: errors.Join(partial.Cause, err)}
		}
		err := p.releaseUnused(ctx, gate, candidate.ID, currentSelection)
		if err == nil {
			continue
		}
		var global *referenceInventoryError
		if errors.As(err, &global) {
			return &ReferenceSweepError{Deferred: partial.Deferred + len(candidates) - i, Cause: errors.Join(partial.Cause, err)}
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

func matchesReference(refs []string, ref string) bool {
	if ref == "" {
		return false
	}
	ref = devcontainer.CanonicalCacheRef(ref)
	return slices.ContainsFunc(refs, func(candidate string) bool { return devcontainer.CanonicalCacheRef(candidate) == ref })
}

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
	id := dockerutil.ConfigImageID(c.Container.Image)
	if id == "" || c.Container.ID != containerID {
		return fmt.Errorf("image reference has invalid container or config ID")
	}
	img, err := p.client.ImageInspect(ctx, id)
	if cerrdefs.IsNotFound(err) {
		return p.releaseMissing(ctx, gate, c, id, desired)
	}
	if err != nil {
		return err
	}
	if err := p.validate(ctx, c, img.ID, img.RepoTags, func() (bool, error) { return dockerutil.DetectPodman(ctx, p.client) }); err != nil {
		return err
	}
	if img.Config == nil {
		return fmt.Errorf("image reference has incomplete image configuration")
	}
	aliases := append([]string{id, devcontainer.ImageAcquisitionRef(img.Config.Labels)}, img.RepoTags...)
	aliases = append(aliases, img.RepoDigests...)
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
		if matchesReference(aliases, ref.Ref) {
			return nil
		}
	}
	if _, err := p.client.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: false, RemoveVolumes: false}); err != nil {
		return err
	}
	return nil
}

// A containerd re-export can remove the old image's catalog entry while its
// created reference still exists. Only release that exact dormant pin after a
// second inspection, checking the full selection under an all-image lease.
func (p *ImageReferences) releaseMissing(ctx context.Context, gate *resourcelifecycle.ImageUse, listed client.ContainerInspectResult, id string, desired func(context.Context) ([]resourcelifecycle.ImageReference, error)) error {
	release, idle := gate.TryCollectAllImages()
	if !idle {
		return nil
	}
	defer release()
	refs, err := desired(ctx)
	if err != nil {
		return &referenceInventoryError{err}
	}
	c, err := p.client.ContainerInspect(ctx, listed.Container.ID, client.ContainerInspectOptions{})
	if err != nil {
		return err
	}
	if err := p.validateMissingPin(ctx, c, listed.Container.ID, id); err != nil {
		return err
	}
	pin := c.Container
	aliases := append(strings.Split(pin.Config.Labels[imageReferenceTagsLabel], "\n"), id, pin.Config.Image, devcontainer.ImageAcquisitionRef(pin.Config.Labels))
	aliases = append(aliases, strings.Split(pin.Config.Labels[imageReferenceDigestsLabel], "\n")...)
	for _, ref := range refs {
		if ref.Ref == "" {
			continue
		}
		if matchesReference(aliases, ref.Ref) {
			return nil
		}
		if desc := pin.ImageManifestDescriptor; desc != nil && (ref.Ref == string(desc.Digest) || strings.HasSuffix(ref.Ref, "@"+string(desc.Digest))) {
			return nil
		}
		if dockerutil.IsLocalImageID(ref.Ref) {
			continue
		}
		// A tag could have named the missing artifact, even if old releases
		// didn't record it. Only a confirmed resolution to another config ID
		// establishes that this selection doesn't need the pin.
		resolved, err := p.client.ImageInspect(ctx, ref.Ref)
		if cerrdefs.IsNotFound(err) && absentConfiguredBase(ref, pin.Config.Labels) {
			continue
		}
		if err != nil {
			return fmt.Errorf("cannot resolve selection while reconciling missing image: %w", err)
		}
		selected := dockerutil.ConfigImageID(resolved.ID)
		if selected == "" || selected == id {
			return nil
		}
	}
	_, err = p.client.ImageInspect(ctx, id)
	if err == nil {
		return nil
	}
	if !cerrdefs.IsNotFound(err) {
		return err
	}
	final, err := p.client.ContainerInspect(ctx, pin.ID, client.ContainerInspectOptions{})
	if err != nil {
		return err
	}
	if err := p.validateMissingPin(ctx, final, pin.ID, id); err != nil {
		return err
	}
	_, err = p.client.ContainerRemove(ctx, pin.ID, client.ContainerRemoveOptions{Force: false, RemoveVolumes: false})
	return err
}

// A base that has never been pulled is normal. Its mutable tag cannot
// currently resolve to this missing feature. Exact selections, recorded pin
// aliases, and all uncertain inspections remain protected.
func absentConfiguredBase(ref resourcelifecycle.ImageReference, labels map[string]string) bool {
	if !ref.ConfiguredBaseOnly || ref.Required || ref.BuildInput || labels[devcontainer.ImageKindLabel] != devcontainer.ImageKindFeature || devcontainer.ImageAcquisitionRef(labels) == "" {
		return false
	}
	// A registry miss for an operator snapshot or custom tag remains ambiguous.
	return devcontainer.IsBuiltinBaseImage(ref.Ref)
}

func (p *ImageReferences) validateMissingPin(ctx context.Context, c client.ContainerInspectResult, containerID, id string) error {
	pin := c.Container
	if pin.ID != containerID || pin.HostConfig == nil || !pin.HostConfig.ReadonlyRootfs || pin.HostConfig.NetworkMode != "none" ||
		pin.Config == nil || !slices.Equal(pin.Config.Entrypoint, []string{"/bin/false"}) ||
		!slices.Equal(pin.Config.Cmd, []string{"crewship-image-reference"}) || pin.Config.User != "1001:1001" ||
		len(pin.HostConfig.Mounts) > 0 || len(pin.HostConfig.Binds) > 0 || len(pin.HostConfig.PortBindings) > 0 || len(pin.HostConfig.VolumesFrom) > 0 ||
		pin.State == nil || pin.State.Pid != 0 || pin.State.Paused || pin.State.Restarting || pin.State.Dead || len(pin.ExecIDs) > 0 {
		return fmt.Errorf("missing-image reference %q does not match the dormant pin configuration", pin.ID)
	}
	for _, m := range pin.Mounts {
		if m.Type != "tmpfs" {
			return fmt.Errorf("missing-image reference %q has non-transient storage", pin.ID)
		}
	}
	// Validate using only facts present in the container record. In particular,
	// Podman's display name must have been recorded when the pin was created.
	return p.validate(ctx, c, id, nil, func() (bool, error) { return dockerutil.DetectPodman(ctx, p.client) })
}

// SetImageReferenceAcquirer is called once while the server wires routes,
// before either the crew starter or durable service controller admits work.
func (p *Provider) SetImageReferenceAcquirer(acquire provider.ImageReferenceAcquirer) {
	p.imageReferences = acquire
}

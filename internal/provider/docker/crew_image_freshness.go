package docker

// #1845 — "this crew's image is behind", the detection this issue's delivery
// half needs.
//
// This is NOT a second detector. ensureImage already resolves the registry
// digest, already compares it against what is on disk, and #1825 already made
// it report which manifest a container was created from. What it does not do
// — and structurally cannot — is look again afterwards: it runs once, at
// container create, and a crew container is then reused until something
// recreates it. On the self-hosted fleets this product is built for that is
// weeks. Everything below reuses ensureImage's own primitives (the shared
// DigestResolver, LocalRepoDigest, the localCacheImagePrefix short-circuit) to
// ask the same question about a container that is ALREADY running.
//
// The sidecar-staleness signal (sidecar_binhash.go → warnStaleSidecarArtifact,
// orchestrator's emitStaleSidecarSignal) is a deliberately SEPARATE condition
// and stays where it is. A stale sidecar means the code executing inside the
// container right now is not the code this server shipped — memory recall and
// egress policy are degraded as you read this, and the fix is to rebuild and
// recopy the sidecar binary. A stale IMAGE means the container is a snapshot
// of an older release: nothing is misbehaving, and the fix is a pull plus a
// recycle. Same word, different urgency, different remediation — see
// internal/server/image_freshness.go for how the two are routed apart.

import (
	"context"
	"fmt"
	"strings"

	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/dockerutil"
	"github.com/crewship-ai/crewship/internal/provider"
)

// The reasons a crew is NOT reported as behind despite not matching cleanly.
// Exported as constants rather than inline strings because the API, the CLI
// and the scheduled sweep all branch on them, and a freshness check whose
// "could not tell" is indistinguishable from its "confirmed current" is a
// check that always passes.
const (
	// reasonNoContainer: the crew has no runtime container. Nothing is running
	// a stale image; the next EnsureCrewRuntime ensures the image first.
	reasonNoContainer = "no container"

	// reasonRegistryUnreachable: the manifest HEAD returned nothing. Air-gapped
	// host, wedged credential helper, blocked egress, or a throttling registry.
	// This is the branch every offline install takes, and it must never alert.
	reasonRegistryUnreachable = "registry unreachable"

	// reasonNoRunningDigest: the image the container was created from carries
	// no registry digest — a locally built derivative, or a daemon reporting no
	// RepoDigests. There is nothing to compare against.
	reasonNoRunningDigest = "image has no registry digest"

	// reasonLocalImage: the crew runs a crewship-cache:<hash> image, built and
	// committed locally, present in NO registry. ensureImage short-circuits
	// these before any registry interaction; so does this. Their freshness
	// question is "was the devcontainer rebuilt?", which the provisioning
	// surface already answers (agents_pending_restart), not "has the tag
	// moved?".
	reasonLocalImage = "locally built image"
)

// Compile-time proof that the docker provider satisfies the optional
// capability. Callers discover it by type assertion, so without this a typo in
// a signature would silently turn every image-freshness surface into a no-op
// instead of failing to build.
var _ provider.CrewImageFreshness = (*Provider)(nil)

// classifyImageDrift is the whole decision, isolated from every daemon call so
// it can be exhausted by a table.
//
// It fails OPEN in every direction. "I could not reach the registry" and "this
// image has no digest" are not evidence of staleness, and treating them as
// such would alert on every air-gapped install and every provisioned crew — a
// category that fires on healthy fleets is a category people mute, and a muted
// category is worse than none.
func classifyImageDrift(runningDigest, resolvedDigest string) (behind bool, reason string) {
	// Registry first: when it is unreachable nothing downstream is knowable,
	// and blaming the local image for the outer failure would send an operator
	// looking in the wrong place.
	if resolvedDigest == "" {
		return false, reasonRegistryUnreachable
	}
	if runningDigest == "" {
		return false, reasonNoRunningDigest
	}
	if runningDigest == resolvedDigest {
		return false, ""
	}
	return true, ""
}

// crewRuntimeImage resolves the reference a crew actually runs, mirroring
// EnsureCrewRuntime's CachedImage > Image > provider-default chain exactly.
// Kept as one function so the freshness check can never disagree with what
// starts — a second reconstruction is a second thing free to drift.
func (p *Provider) crewRuntimeImage(team provider.CrewConfig) string {
	if team.CachedImage != "" {
		return team.CachedImage
	}
	if team.Image != "" {
		return team.Image
	}
	return p.cfg.RuntimeImage
}

// CrewImageState implements provider.CrewImageFreshness. Read-only.
func (p *Provider) CrewImageState(ctx context.Context, team provider.CrewConfig) (*provider.CrewImageState, error) {
	ref := p.crewRuntimeImage(team)
	st := &provider.CrewImageState{Image: ref}

	if strings.HasPrefix(ref, localCacheImagePrefix) {
		st.Reason = reasonLocalImage
		return st, nil
	}

	containerID, running, err := p.FindCrewContainer(ctx, team.ID, team.Slug)
	if err != nil {
		return nil, fmt.Errorf("find crew container: %w", err)
	}
	if containerID == "" {
		st.Reason = reasonNoContainer
		return st, nil
	}
	st.ContainerID = containerID
	st.Running = running

	st.RunningDigest = p.containerImageDigest(ctx, containerID, ref)
	// Shared resolver, shared cache: this piggybacks on the same HEAD result
	// ensureImage would use on the next start rather than issuing a second
	// round-trip per crew per sweep.
	st.ResolvedDigest = p.digestResolver.Remote(ctx, ref)
	st.Behind, st.Reason = classifyImageDrift(st.RunningDigest, st.ResolvedDigest)
	return st, nil
}

// containerImageDigest reads back the registry manifest digest of the image a
// LIVE container was created from.
//
// Two hops, and both are necessary. ContainerInspect's Image field is the
// image *ID* — a digest of the config blob, not of the manifest, and never
// comparable to what a registry HEAD returns. ImageInspect on that ID is what
// carries RepoDigests, and LocalRepoDigest then picks the entry belonging to
// THIS repository: an image can be tagged into several repos and returning the
// first entry would compare a digest from a repo the crew does not run from.
//
// Every failure returns "" — the caller classifies that as
// reasonNoRunningDigest rather than as staleness.
func (p *Provider) containerImageDigest(ctx context.Context, containerID, ref string) string {
	inspectResult, err := p.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return ""
	}
	imageID := inspectResult.Container.Image
	if imageID == "" {
		return ""
	}
	img, err := p.client.ImageInspect(ctx, imageID)
	if err != nil {
		return ""
	}
	return dockerutil.LocalRepoDigest(img.RepoDigests, ref)
}

// RefreshCrewImage implements provider.CrewImageFreshness.
//
// Order matters: pull FIRST, drop the container SECOND. The reverse leaves a
// window where the crew has no container and no local image, so an agent
// dispatched in between pays the full pull inline (or fails, if the registry
// picked that moment to throttle). Pulling first means the worst case is a
// container that is still on the old image — exactly the state we started in.
func (p *Provider) RefreshCrewImage(ctx context.Context, team provider.CrewConfig) (*provider.CrewImageRefresh, error) {
	// Take the "before" reading through the same code path the report uses, so
	// what a refresh says it changed is expressed in the same terms the UI was
	// showing a moment earlier.
	before, err := p.CrewImageState(ctx, team)
	if err != nil {
		return nil, err
	}
	res := &provider.CrewImageRefresh{
		Image:          before.Image,
		PreviousDigest: before.RunningDigest,
	}

	// A locally built cache image has no registry to pull from; ensureImage
	// would return ErrCachedImageMissing at best. Dropping the container is
	// still the right (and only) remediation available — the next start
	// re-derives from the cached image the provisioner committed.
	if before.Reason != reasonLocalImage {
		prov, err := p.ensureImage(ctx, before.Image)
		if err != nil {
			return nil, fmt.Errorf("refresh image %s: %w", before.Image, err)
		}
		res.NewDigest = prov.Digest
	}

	if before.ContainerID == "" {
		return res, nil
	}
	if err := p.RemoveCrewRuntime(ctx, before.ContainerID); err != nil {
		return nil, err
	}
	// The warm cache asserts "this crew has a live container" for warmCrewTTL.
	// Leaving the entry after removing the container would have the next
	// EnsureCrewRuntime short-circuit onto an id that no longer exists.
	p.evictWarm(team.ID)
	res.ContainerRemoved = true
	return res, nil
}

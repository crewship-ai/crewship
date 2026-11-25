package devcontainer

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Provenance labels written on every image this installation builds.
//
// The keys are image-specific on purpose. A container inherits the labels of
// the image it starts from, and the runtime recognises its own containers by
// crewship.instance-id / crewship.kind; reusing those keys here would make
// every container started from a crew image answer to them unasked, the trap
// crewship.temp fell into (see TempContainerLabelKey).
//
// A label records who built the image. It is evidence of origin, not on its
// own a permission to delete: what an installation still needs is decided by
// its database.
const (
	ImageInstanceLabel       = "crewship.image.instance-id"
	ImageKindLabel           = "crewship.image.kind"
	ImageAcquisitionKeyLabel = "crewship.image.cache-key"

	// ImageKindCrew marks the final image a crew runs from.
	ImageKindCrew = "crew"
	// ImageKindFeature marks the intermediate BuildKit feature image.
	ImageKindFeature = "feature"
)

// normalizeInstanceID returns the installation identity in the one spelling
// tags and labels use, or "" when it is not a settled identity (32 random
// bytes in hex, see resourcelifecycle.LoadIdentity).
func normalizeInstanceID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if raw, err := hex.DecodeString(id); err != nil || len(raw) != 32 {
		return ""
	}
	return id
}

// ImageNamespace is the tag namespace of an installation's images, or "" when
// the installation has no settled identity. Two installations sharing a
// daemon resolve one configuration to different tags, so neither adopts an
// image whose only other reference lives in a database it cannot read.
func ImageNamespace(instanceID string) string {
	id := normalizeInstanceID(instanceID)
	if id == "" {
		return ""
	}
	return id
}

// CanonicalCacheRef maps Docker Hub's fully qualified spelling of Crewship's
// two local cache repositories to the spelling used at build time. Other
// namespaces, registries, image IDs and ordinary image references stay exact.
func CanonicalCacheRef(ref string) string {
	short := strings.TrimPrefix(ref, "docker.io/library/")
	if strings.HasPrefix(short, cacheImageRepoPrefix) || strings.HasPrefix(short, FeatureImageTagPrefix) {
		return short
	}
	return ref
}

// namespacedTag joins a repository prefix ("crewship-cache:"), the
// installation namespace and a hash. Without identity it is the legacy name.
func namespacedTag(prefix, instanceID, hash string) string {
	short := hash
	if len(short) > 12 {
		short = short[:12]
	}
	if ns := ImageNamespace(instanceID); ns != "" {
		return prefix + ns + "-" + short
	}
	return prefix + short
}

// featureImageTag is the tag of an intermediate feature image.
func featureImageTag(instanceID, contentHash string) string {
	return namespacedTag(FeatureImageTagPrefix, instanceID, contentHash)
}

// SetInstanceID binds the provisioner to its installation. Images built from
// then on carry the installation's tag namespace and provenance labels. An
// empty or malformed identity leaves legacy names and clears reserved labels:
// such images have no established owner and stay outside automatic deletion.
// Call before the first Provision; it is not synchronised against one.
func (p *Provisioner) SetInstanceID(id string) {
	p.instanceID = normalizeInstanceID(id)
}

// cacheTag is the tag this installation publishes a configuration hash under.
func (p *Provisioner) cacheTag(configHash string) string {
	return namespacedTag(cacheImageRepoPrefix, p.instanceID, configHash)
}

// imageOwnerLabels returns the provenance labels for an image of the given
// kind, or empty reserved values when identity is unavailable. Clearing them
// prevents an unowned build from inheriting ownership from its base. A shared image
// names no crew: several crews of one installation may select it.
func imageOwnerLabels(instanceID, kind string) map[string]string {
	id := normalizeInstanceID(instanceID)
	if id == "" {
		return map[string]string{ImageInstanceLabel: "", ImageKindLabel: ""}
	}
	return map[string]string{ImageInstanceLabel: id, ImageKindLabel: kind}
}

// imageBuildLabels persists the cache acquisition identity even after Docker
// drops its tag. Clearing the key prevents inheriting the base's acquisition.
func imageBuildLabels(instanceID, kind, key string) map[string]string {
	labels := imageOwnerLabels(instanceID, kind)
	labels[ImageAcquisitionKeyLabel] = ""
	if labels[ImageInstanceLabel] != "" {
		labels[ImageAcquisitionKeyLabel] = key
	}
	return labels
}

// ImageAcquisitionRef recovers the canonical build alias from immutable image
// provenance. Missing or malformed provenance is never guessed from an ID.
func ImageAcquisitionRef(labels map[string]string) string {
	owner := normalizeInstanceID(labels[ImageInstanceLabel])
	key := labels[ImageAcquisitionKeyLabel]
	raw, err := hex.DecodeString(key)
	if owner == "" || err != nil || len(raw) != 32 || key != strings.ToLower(key) {
		return ""
	}
	switch labels[ImageKindLabel] {
	case ImageKindCrew:
		return namespacedTag(cacheImageRepoPrefix, owner, key)
	case ImageKindFeature:
		return featureImageTag(owner, key)
	default:
		return ""
	}
}

var (
	imageLabelKeyRe   = regexp.MustCompile(`^[a-z0-9]+([.-][a-z0-9]+)*$`)
	imageLabelValueRe = regexp.MustCompile(`^[A-Za-z0-9._-]*$`)
)

// imageLabelDirectives renders labels as `LABEL "key"="value"` lines in key
// order. The same text is valid in a Dockerfile and as a `docker commit`
// change. Anything outside a conservative alphabet is an error rather than
// escaped: a label must never be able to open a new directive.
func imageLabelDirectives(labels map[string]string) ([]string, error) {
	if len(labels) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(labels))
	for k, v := range labels {
		if !imageLabelKeyRe.MatchString(k) {
			return nil, fmt.Errorf("devcontainer: invalid image label key %q", k)
		}
		if !imageLabelValueRe.MatchString(v) {
			return nil, fmt.Errorf("devcontainer: invalid value for image label %q", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, `LABEL "`+k+`"="`+labels[k]+`"`)
	}
	return out, nil
}

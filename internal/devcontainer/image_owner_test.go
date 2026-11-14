package devcontainer

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
)

const (
	installationA = "aaaaaaaaaaaa1111111111111111111111111111111111111111111111111111"
	installationB = "bbbbbbbbbbbb2222222222222222222222222222222222222222222222222222"
)

func TestImageNamespace(t *testing.T) {
	tests := []struct {
		name, id, want string
	}{
		{"settled identity", installationA, "aaaaaaaaaaaa"},
		{"upper case is folded", strings.ToUpper(installationA), "aaaaaaaaaaaa"},
		{"surrounding space is ignored", " " + installationB + "\n", "bbbbbbbbbbbb"},
		{"no identity", "", ""},
		{"too short", "abc123", ""},
		{"not hex", strings.Repeat("z", 64), ""},
		{"too long", installationA + "00", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ImageNamespace(tt.id); got != tt.want {
				t.Errorf("ImageNamespace(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestProvisionerTagsAreNamespacedByInstallation(t *testing.T) {
	const hash = "0123456789abcdef0123"
	tests := []struct {
		name, id, wantCache, wantFeat string
	}{
		{"installation A", installationA, "crewship-cache:aaaaaaaaaaaa-0123456789ab", "crewship-feat:aaaaaaaaaaaa-0123456789ab"},
		{"installation B", installationB, "crewship-cache:bbbbbbbbbbbb-0123456789ab", "crewship-feat:bbbbbbbbbbbb-0123456789ab"},
		{"no identity keeps the legacy names", "", "crewship-cache:0123456789ab", "crewship-feat:0123456789ab"},
		{"invalid identity keeps the legacy names", "not-an-identity", "crewship-cache:0123456789ab", "crewship-feat:0123456789ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewProvisioner(&mockCommitClient{}, nil, nil, testLogger())
			p.SetInstanceID(tt.id)
			if got := p.cacheTag(hash); got != tt.wantCache {
				t.Errorf("cacheTag = %q, want %q", got, tt.wantCache)
			}
			if got := featureImageTag(p.instanceID, hash); got != tt.wantFeat {
				t.Errorf("featureImageTag = %q, want %q", got, tt.wantFeat)
			}
		})
	}
}

func TestImageOwnerLabels(t *testing.T) {
	if got := imageOwnerLabels("", ImageKindCrew); got != nil {
		t.Errorf("no identity must label nothing, got %v", got)
	}
	got := imageOwnerLabels(installationA, ImageKindFeature)
	if got[ImageInstanceLabel] != installationA || got[ImageKindLabel] != ImageKindFeature || len(got) != 2 {
		t.Errorf("labels = %v", got)
	}
	// The keys must not be the ones runtime containers are recognised by: a
	// container inherits its image's labels.
	for k := range got {
		if k == "crewship.instance-id" || k == "crewship.kind" || k == "crewship.crew-id" {
			t.Errorf("image label %q collides with a container ownership label", k)
		}
	}
}

func TestGenerateDockerfile_Labels(t *testing.T) {
	df, err := GenerateDockerfile(DockerfileBuild{
		BaseImage:  "debian:bookworm-slim",
		ExtraSteps: []string{"RUN true"},
		Labels:     imageOwnerLabels(installationA, ImageKindCrew),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "RUN true\n" +
		`LABEL "crewship.image.instance-id"="` + installationA + `"` + "\n" +
		`LABEL "crewship.image.kind"="crew"` + "\n"
	if !strings.HasSuffix(df, want) {
		t.Errorf("labels must close the Dockerfile in sorted order.\ngot:\n%s\nwant suffix:\n%s", df, want)
	}

	plain, err := GenerateDockerfile(DockerfileBuild{BaseImage: "debian:bookworm-slim"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "LABEL") {
		t.Errorf("no labels requested, none may be emitted:\n%s", plain)
	}

	for name, labels := range map[string]map[string]string{
		"newline in value": {"crewship.image.kind": "crew\nRUN id"},
		"quote in value":   {"crewship.image.kind": `crew" x="y`},
		"bad key":          {"crewship image": "crew"},
	} {
		if _, err := GenerateDockerfile(DockerfileBuild{BaseImage: "debian:bookworm-slim", Labels: labels}); err == nil {
			t.Errorf("%s: want an error, a label must never open a new directive", name)
		}
	}
}

func TestStageOwnedBuildContext_NamespacesAndLabelsFeatureImage(t *testing.T) {
	stage := func(id string) (string, string) {
		t.Helper()
		dir, df, tag, err := stageOwnedBuildContext(id, "ubuntu:22.04", nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		return df, tag
	}
	dfA, tagA := stage(installationA)
	_, tagB := stage(installationB)
	dfNone, tagNone := stage("")

	if !strings.HasPrefix(tagA, "crewship-feat:aaaaaaaaaaaa-") || !strings.HasPrefix(tagB, "crewship-feat:bbbbbbbbbbbb-") {
		t.Errorf("feature tags must carry the installation namespace: %q %q", tagA, tagB)
	}
	if strings.Contains(strings.TrimPrefix(tagNone, FeatureImageTagPrefix), "-") {
		t.Errorf("without identity the legacy feature tag is kept, got %q", tagNone)
	}
	if !strings.Contains(dfA, `LABEL "crewship.image.kind"="feature"`) || !strings.Contains(dfA, installationA) {
		t.Errorf("owned feature image must be labelled:\n%s", dfA)
	}
	if strings.Contains(dfNone, "LABEL") {
		t.Errorf("without identity nothing is labelled:\n%s", dfNone)
	}
	// The legacy helper stays byte-identical for callers without identity.
	dir, dfLegacy, tagLegacy, err := stageBuildContext("ubuntu:22.04", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if dfLegacy != dfNone || tagLegacy != tagNone {
		t.Errorf("stageBuildContext must equal the identity-less staging")
	}
}

// Two installations sharing one daemon resolve the same configuration. The
// second must not adopt the first one's image: its database reference is
// invisible to the first installation, which would be free to delete it.
func TestProvision_InstallationsOnOneDaemonDoNotShareImages(t *testing.T) {
	cfg := &Config{Image: offlineBaseImage, ContainerEnv: map[string]string{"FOO": "bar"}}
	hash := configHash(offlineBaseImage, cfg, "", dockerfileGenFingerprint(offlineBaseImage, cfg))
	daemon := &mockCommitClient{existingImages: []string{offlineBaseImage}}

	provision := func(id string) *ProvisionResult {
		t.Helper()
		p := NewProvisioner(daemon, NewInstaller(&mockDockerClient{exitCode: 0}, testLogger()), nil, testLogger())
		p.SetInstanceID(id)
		res, err := p.Provision(context.Background(), offlineBaseImage, cfg, "")
		if err != nil {
			t.Fatalf("provision for %.12s: %v", id, err)
		}
		// The mock daemon does not publish a commit by itself.
		for _, ref := range daemon.commitRefs {
			if !slices.Contains(daemon.existingImages, ref) {
				daemon.existingImages = append(daemon.existingImages, ref)
			}
		}
		return res
	}

	resA := provision(installationA)
	if got, want := daemon.commitRefs, []string{"crewship-cache:aaaaaaaaaaaa-" + hash[:12]}; !slices.Equal(got, want) {
		t.Fatalf("installation A commits = %v, want %v", got, want)
	}
	wantChanges := []string{
		`LABEL "crewship.image.instance-id"="` + installationA + `"`,
		`LABEL "crewship.image.kind"="crew"`,
	}
	for _, want := range wantChanges {
		if !slices.Contains(daemon.commitChanges[0], want) {
			t.Errorf("commit changes %v lack %s", daemon.commitChanges[0], want)
		}
	}

	resB := provision(installationB)
	if len(daemon.commitRefs) != 2 || daemon.commitRefs[1] != "crewship-cache:bbbbbbbbbbbb-"+hash[:12] {
		t.Fatalf("installation B must build its own image, commits = %v", daemon.commitRefs)
	}
	if resA.ConfigHash != resB.ConfigHash {
		t.Errorf("the configuration hash is not installation-specific: %q vs %q", resA.ConfigHash, resB.ConfigHash)
	}

	// Inside one installation the image is still shared: a second crew with
	// the same configuration is a cache hit.
	provision(installationA)
	if len(daemon.commitRefs) != 2 {
		t.Errorf("a second provision in installation A must reuse its image, commits = %v", daemon.commitRefs)
	}
}

// Without a settled identity the provisioner behaves exactly as before: the
// legacy tag, no provenance labels. Such images are never owned.
func TestProvision_WithoutIdentityKeepsLegacyNamesAndLabelsNothing(t *testing.T) {
	cfg := &Config{Image: offlineBaseImage, ContainerEnv: map[string]string{"FOO": "bar"}}
	hash := configHash(offlineBaseImage, cfg, "", dockerfileGenFingerprint(offlineBaseImage, cfg))
	daemon := &mockCommitClient{existingImages: []string{offlineBaseImage}}
	p := NewProvisioner(daemon, NewInstaller(&mockDockerClient{exitCode: 0}, testLogger()), nil, testLogger())

	if _, err := p.Provision(context.Background(), offlineBaseImage, cfg, ""); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(daemon.commitRefs, []string{cacheImageTag(hash)}) {
		t.Errorf("commits = %v, want the legacy tag %s", daemon.commitRefs, cacheImageTag(hash))
	}
	for _, change := range daemon.commitChanges[0] {
		if strings.HasPrefix(change, "LABEL") {
			t.Errorf("unowned image must not be labelled: %s", change)
		}
	}
}

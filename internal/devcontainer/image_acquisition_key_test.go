package devcontainer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageAcquisitionProvenanceRecoversBothBuildAliases(t *testing.T) {
	key := strings.Repeat("1", 64)
	for _, kind := range []string{ImageKindCrew, ImageKindFeature} {
		labels := imageBuildLabels(installationA, kind, key)
		prefix := cacheImageRepoPrefix
		if kind == ImageKindFeature {
			prefix = FeatureImageTagPrefix
		}
		if got := ImageAcquisitionRef(labels); got != prefix+installationA+"-"+key[:12] {
			t.Fatalf("invalid recovered acquisition: %s", got)
		}
	}
	if labels := imageBuildLabels("", ImageKindCrew, key); labels[ImageAcquisitionKeyLabel] != "" {
		t.Fatal("identityless build inherited acquisition ownership")
	}
}

func TestFeatureAcquisitionKeyDescribesStagedPayloadAndReturnedDockerfile(t *testing.T) {
	featureDir := t.TempDir()
	mustWrite(t, filepath.Join(featureDir, "install.sh"), "#!/bin/sh\necho original\n", 0o755)
	features := []*ResolvedFeature{{Ref: "registry.invalid/tool:1", Dir: featureDir, Metadata: FeatureMetadata{ID: "tool"}}}
	dir, df, tag, err := stageOwnedBuildContext(installationA, "alpine:3", features, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	actual, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil || string(actual) != df {
		t.Fatal("builder payload differs from returned Dockerfile")
	}
	marker := "\nLABEL \"" + ImageAcquisitionKeyLabel + "\"=\""
	index := strings.LastIndex(df, marker)
	if index < 0 {
		t.Fatal("derived acquisition label missing")
	}
	payload, tail := df[:index], df[index+len(marker):]
	key := strings.TrimSuffix(tail, "\"\n")
	expected, err := hashStagedInputs(payload, []stagedFeature{{id: "tool", dir: filepath.Join(dir, featureContextDir, "tool")}})
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(featureDir, "install.sh"), "#!/bin/sh\necho changed\n", 0o755)
	otherDir, _, otherTag, err := stageOwnedBuildContext(installationA, "alpine:3", features, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(otherDir)
	if tag == otherTag {
		t.Fatal("feature payload change reused the canonical acquisition key")
	}
	if err != nil || key != expected || tag != featureImageTag(installationA, key) {
		t.Fatalf("derived provenance mismatch: key=%s expected=%s tag=%s error=%v", key, expected, tag, err)
	}
}

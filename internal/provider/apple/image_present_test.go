package apple

import "testing"

// `container image list --format json` carries no top-level "reference": the
// name lives in the descriptor annotations, and `id` holds the digest. The
// provider decoded into {Reference string}, so every entry read back as the
// empty string, no locally present image was ever recognised, and ensureImage
// fell through to a pull every time.
//
// That was invisible while the only image was pulled from a registry anyway.
// It broke the moment a crew ran its own provisioned image, which exists ONLY
// locally: `container image pull crewship-cache:…` fails, and the crew could
// not start (#1779).
const appleImageListJSON = `[
  {"id":"sha256:aaa","configuration":{"descriptor":{"annotations":{
     "com.apple.containerization.image.name":"crewship-cache:66e240493ae4",
     "io.containerd.image.name":"docker.io/library/crewship-cache:66e240493ae4"}}}},
  {"id":"sha256:bbb","configuration":{"descriptor":{"annotations":{
     "com.apple.containerization.image.name":"docker.io/library/alpine:3.20",
     "io.containerd.image.name":"docker.io/library/alpine:3.20"}}}}
]`

func TestImageListNames_ReadsTheAnnotatedNames(t *testing.T) {
	names, err := parseImageListNames([]byte(appleImageListJSON))
	if err != nil {
		t.Fatalf("parseImageListNames: %v", err)
	}
	if !names["crewship-cache:66e240493ae4"] {
		t.Errorf("locally built image not recognised; got %v", names)
	}
}

// A pulled image is annotated with its fully qualified name, so a plain
// `alpine:3.20` has to match too or every pull repeats on every start.
func TestImageListNames_NormalisesTheLibraryPrefix(t *testing.T) {
	names, err := parseImageListNames([]byte(appleImageListJSON))
	if err != nil {
		t.Fatalf("parseImageListNames: %v", err)
	}
	if !names["alpine:3.20"] {
		t.Errorf("docker.io/library prefix not normalised; got %v", names)
	}
}

func TestImageListNames_EmptyListIsNotAnError(t *testing.T) {
	names, err := parseImageListNames([]byte(`[]`))
	if err != nil {
		t.Fatalf("an empty store is not an error: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("expected no names, got %v", names)
	}
}

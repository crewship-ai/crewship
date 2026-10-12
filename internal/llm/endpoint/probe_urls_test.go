package endpoint

import "testing"

func TestNativeProbeURLsRetainMountAndQuery(t *testing.T) {
	e, err := Normalize("https://models.example.test/ollama/v1?api-version=preview")
	if err != nil {
		t.Fatal(err)
	}
	if got := e.TagsURL(); got != "https://models.example.test/ollama/api/tags?api-version=preview" {
		t.Fatalf("tags URL %q", got)
	}
	if got := e.ShowURL(); got != "https://models.example.test/ollama/api/show?api-version=preview" {
		t.Fatalf("show URL %q", got)
	}
	if got := e.String(); got != "https://models.example.test/ollama" {
		t.Fatalf("display root %q", got)
	}
}

func TestUnconfiguredEndpointHasNoDialTarget(t *testing.T) {
	e := Endpoint{}
	if e.TagsURL() != "" || e.ShowURL() != "" || e.String() != "" || e.IsContainerOnlyHost() {
		t.Fatalf("unconfigured endpoint acquired a target: %#v", e)
	}
	if root, ok := azureResourceRoot(nil); ok || root != nil {
		t.Fatal("nil root treated as Azure deployment")
	}
}

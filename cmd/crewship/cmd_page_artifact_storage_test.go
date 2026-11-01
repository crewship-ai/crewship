package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestPageArtifactStorageReadsWorkspaceUsage(t *testing.T) {
	stub := pageStub(t)
	stub.OnGet("/api/v1/pages/artifacts/storage", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(`{"used_bytes":900,"reserved_bytes":0,"limit_bytes":1000,"level":"critical","instance_level":"none","retention":"keep-all"}`), "application/json"
	})
	// Output goes to process stdout; TestAcceptance_ArtifactStorageBinary
	// asserts the rendered JSON through the real binary.
	if _, err := runParityArtifact(t, newPageArtifactCommand, "storage"); err != nil {
		t.Fatal(err)
	}
	if len(stub.CallsFor("GET", "/api/v1/pages/artifacts/storage")) != 1 {
		t.Fatal("storage command did not call the usage endpoint")
	}
}

func TestPageArtifactPublishAggregateStorageQuotaSaysNothingSaved(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost("/api/v1/pages/demo/artifact/versions", func(*http.Request, []byte) (int, []byte, string) {
		return 413, []byte(`{"error":"Workspace artifact storage is full. Nothing was saved and every existing version is retained.","code":"artifact_storage_quota","retention":"keep-all"}`), "application/json"
	})
	_, err := runParityArtifact(t, newPageArtifactCommand, "publish", "demo", "--base-version", "7", "--file", pageBundleFile(t, parityManifest))
	if err == nil {
		t.Fatal("storage refusal accepted")
	}
	if got := cli.ExitCodeFor(err); got != cli.ExitValidation {
		t.Fatalf("exit = %d, want %d", got, cli.ExitValidation)
	}
	for _, want := range []string{"workspace artifact storage is full", "nothing was saved", "--file", "retained", "storage"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("message %q lacks %q", err, want)
		}
	}
	if envelope := cli.NewErrorEnvelope(err); envelope.Error.Extensions["code"] != "artifact_storage_quota" {
		t.Fatalf("machine envelope lost the storage code: %+v", envelope.Error)
	}
	if len(stub.CallsFor("GET", "/api/v1/pages/demo/artifact")) != 0 {
		t.Fatal("a definitive storage refusal triggered a reconcile read")
	}
}

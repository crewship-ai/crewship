package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

func TestPageArtifactCopyCLIUsesExplicitRevisionAndIdentity(t *testing.T) {
	stub := pageStub(t)
	resetPageArtifactCommand(t)
	const endpoint = "/api/v1/pages/source/artifact/copies"
	stub.On("POST", endpoint, func(_ *http.Request, raw []byte) (int, []byte, string) {
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"source_revision": float64(7), "slug": "my-copy", "name": "My copy", "mutation_id": "copy-once"}
		if !reflect.DeepEqual(body, want) {
			t.Fatalf("copy payload=%#v, want %#v", body, want)
		}
		return 201, []byte(`{"page_slug":"my-copy","revision":1}`), "application/json"
	})
	if out, err := runPageCLI(t, "", "page", "artifact", "copy", "source", "my-copy", "--revision", "7", "--name", "My copy", "--mutation-id", "copy-once"); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if len(stub.CallsFor("POST", endpoint)) != 1 {
		t.Fatal("copy must send one explicit request")
	}
}

func TestPageArtifactCopyCLIRequiresExplicitSourceSelection(t *testing.T) {
	for _, args := range [][]string{
		{"source", "copy", "--name", "Copy"},
		{"source", "copy", "--revision", "0", "--name", "Copy"},
		{"source", "copy", "--revision", "1"},
		{"source", "copy", "--revision", "1", "--name", "Copy", "--mutation-id", ""},
	} {
		cmd := newPageArtifactCopyCommand()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("accepted incomplete source selection: %v", args)
		}
	}
}

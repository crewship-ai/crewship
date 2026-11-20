package dockerutil

import (
	"context"
	"strings"
	"testing"

	"github.com/moby/moby/client"
)

type identityInspector struct {
	query string
	id    string
}

func (i *identityInspector) ImageInspect(_ context.Context, ref string, _ ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	i.query = ref
	var result client.ImageInspectResult
	result.ID = i.id
	return result, nil
}

func TestVerifyDaemonImageID(t *testing.T) {
	bare := strings.Repeat("a", 64)
	id := "sha256:" + bare
	for _, tt := range []struct {
		name, returned, inspected, wantQuery string
		ok                                   bool
	}{
		{"canonical", id, bare, id, true},
		{"bare", bare, id, id, true},
		{"short", bare[:12], id, "", false},
		{"tag", "localhost/crewship:latest", id, "", false},
		{"manifest reference", "alpine@" + id, id, "", false},
		{"mismatch", id, strings.Repeat("b", 64), id, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inspector := &identityInspector{id: tt.inspected}
			got, err := VerifyDaemonImageID(t.Context(), inspector, tt.returned)
			if (err == nil) != tt.ok || (tt.ok && got != id) || inspector.query != tt.wantQuery {
				t.Fatalf("id=%q err=%v lookup=%q", got, err, inspector.query)
			}
		})
	}
}

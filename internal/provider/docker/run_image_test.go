package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestContainerStatusRetainsActualImageID(t *testing.T) {
	imageID := "sha256:" + strings.Repeat("a", 64)
	p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Id": "container-one", "Image": imageID,
			"Config": map[string]any{"Image": "mutable:latest"},
			"State":  map[string]any{"Running": true},
		})
	})
	defer close()
	got, err := p.ContainerStatus(context.Background(), "container-one")
	if err != nil || got.ImageID != imageID {
		t.Fatalf("status = %+v, %v; want actual %s", got, err, imageID)
	}
}

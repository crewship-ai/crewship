package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

func TestResetOwnedDockerLabelsOnlyAndNoForce(t *testing.T) {
	removed := []string{}
	own := map[string]string{resourcelifecycle.InstanceLabel: "own"}
	foreign := map[string]string{resourcelifecycle.InstanceLabel: "foreign"}
	p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/containers/json"):
			json.NewEncoder(w).Encode([]any{map[string]any{"Id": "owned-container", "Labels": own}, map[string]any{"Id": "foreign-container", "Labels": foreign}, map[string]any{"Id": "legacy-container"}})
		case strings.HasSuffix(path, "/containers/owned-container/json"):
			json.NewEncoder(w).Encode(map[string]any{"Id": "owned-container", "Config": map[string]any{"Labels": own}})
		case strings.HasSuffix(path, "/containers/owned-container/stop"):
			w.WriteHeader(204)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/volumes"):
			json.NewEncoder(w).Encode(map[string]any{"Volumes": []any{map[string]any{"Name": "owned-volume", "Labels": own}, map[string]any{"Name": "foreign-volume", "Labels": foreign}, map[string]any{"Name": "legacy-volume"}}})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/volumes/owned-volume"):
			json.NewEncoder(w).Encode(map[string]any{"Name": "owned-volume", "Labels": own})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/networks"):
			json.NewEncoder(w).Encode([]any{map[string]any{"Id": "owned-network", "Labels": own}, map[string]any{"Id": "foreign-network", "Labels": foreign}, map[string]any{"Id": "legacy-network"}})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/networks/owned-network"):
			json.NewEncoder(w).Encode(map[string]any{"Id": "owned-network", "Labels": own})
		case r.Method == http.MethodDelete:
			if strings.Contains(path, "foreign") || strings.Contains(path, "legacy") {
				t.Errorf("removed foreign/legacy resource %s", path)
			}
			if r.URL.Query().Get("force") == "true" || r.URL.Query().Get("v") == "true" {
				t.Errorf("forced or removed unlabelled anonymous volumes: %s", r.URL.RawQuery)
			}
			removed = append(removed, path)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request %s %s", r.Method, path)
			w.WriteHeader(500)
		}
	})
	defer close()
	if err := resetOwnedDocker(context.Background(), p.client, "own"); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 3 {
		t.Fatalf("removed %v", removed)
	}
}

func TestResetOwnedDockerRejectsIncompleteVolumeInventory(t *testing.T) {
	deletes := 0
	p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes++
			w.WriteHeader(204)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			json.NewEncoder(w).Encode([]any{})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/volumes") {
			json.NewEncoder(w).Encode(map[string]any{"Volumes": []any{}, "Warnings": []string{"partial inventory"}})
			return
		}
		t.Errorf("unexpected request %s", r.URL.Path)
		w.WriteHeader(500)
	})
	defer close()
	if err := resetOwnedDocker(context.Background(), p.client, "own"); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatal(err)
	}
	if deletes != 0 {
		t.Fatal("incomplete volume inventory removed resources")
	}
}

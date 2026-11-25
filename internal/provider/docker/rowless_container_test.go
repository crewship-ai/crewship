package docker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

func rowlessFixture() (resourcelifecycle.Container, container.InspectResponse) {
	id := strings.Repeat("a", 64)
	expected := resourcelifecycle.Container{ID: id, Kind: "crew", CrewID: "old-crew", InstanceID: strings.Repeat("b", 64)}
	size := int64(0)
	return expected, container.InspectResponse{ID: id, Image: "sha256:" + strings.Repeat("1", 64), SizeRw: &size,
		State: &container.State{Status: "created"}, NetworkSettings: &container.NetworkSettings{},
		HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true},
		Config:     &container.Config{Labels: map[string]string{resourcelifecycle.InstanceLabel: expected.InstanceID, crewKindLabel: "crew", crewCrewIDLabel: expected.CrewID}},
	}
}

func TestRowlessContainerPreservesInUseAndApplicationData(t *testing.T) {
	expected, valid := rowlessFixture()
	if reason := rowlessContainerRetention(valid, expected); reason != "" {
		t.Fatalf("unused empty container retained: %s", reason)
	}
	for name, change := range map[string]func(*container.InspectResponse){
		"running":        func(c *container.InspectResponse) { c.State.Status = "running"; c.State.Running = true },
		"exited":         func(c *container.InspectResponse) { c.State.Status = "exited" },
		"previous work":  func(c *container.InspectResponse) { c.RestartCount++ },
		"ports":          func(c *container.InspectResponse) { c.HostConfig.PublishAllPorts = true },
		"attached":       func(c *container.InspectResponse) { c.HostConfig.NetworkMode = "container:other" },
		"writable layer": func(c *container.InspectResponse) { c.HostConfig.ReadonlyRootfs = false },
		"bind":           func(c *container.InspectResponse) { c.HostConfig.Binds = []string{"/customer:/work"} },
		"volume": func(c *container.InspectResponse) {
			c.Mounts = []container.MountPoint{{Type: "volume", Name: "only-backup"}}
		},
		"application":       func(c *container.InspectResponse) { c.Config.Labels["com.docker.compose.project"] = "customer" },
		"other instance":    func(c *container.InspectResponse) { c.Config.Labels[resourcelifecycle.InstanceLabel] = "other" },
		"missing ownership": func(c *container.InspectResponse) { delete(c.Config.Labels, resourcelifecycle.InstanceLabel) },
		"incomplete state":  func(c *container.InspectResponse) { c.State = nil },
	} {
		t.Run(name, func(t *testing.T) {
			_, c := rowlessFixture()
			change(&c)
			if rowlessContainerRetention(c, expected) == "" {
				t.Fatal("container eligible with incomplete evidence or potential customer data")
			}
		})
	}
}

func TestRowlessContainerImageInspectionFailureScope(t *testing.T) {
	expected, c := rowlessFixture()
	for _, tc := range []struct {
		name      string
		code      int
		reason    string
		wantError bool
	}{
		{"image absent", http.StatusNotFound, "missing-image", false},
		{"engine unavailable", http.StatusServiceUnavailable, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/containers/"+c.ID+"/json"):
					_ = json.NewEncoder(w).Encode(c)
				case strings.HasSuffix(r.URL.Path, "/images/"+c.Image+"/json"):
					w.WriteHeader(tc.code)
					_ = json.NewEncoder(w).Encode(map[string]string{"message": tc.name})
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					w.WriteHeader(http.StatusInternalServerError)
				}
			}))
			t.Cleanup(srv.Close)
			cli, err := client.New(client.WithHost("tcp://"+strings.TrimPrefix(srv.URL, "http://")), client.WithAPIVersion("1.47"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cli.Close() })
			rt := cleanupRuntime{client: cli}
			reason, err := rt.OrphanRetentionReason(t.Context(), expected)
			if reason != tc.reason || (err != nil) != tc.wantError {
				t.Fatalf("reason=%q err=%v", reason, err)
			}
		})
	}
}

func TestRowlessContainerWritableLayerEvidence(t *testing.T) {
	size := int64(4096)
	for _, tc := range []struct {
		name         string
		size         *int64
		code         int
		body, reason string
		wantError    bool
	}{
		{"snapshot metadata", &size, 200, "[]", "", false},
		{"size unavailable", nil, 200, "[]", "", false},
		{"engine encodes empty slice", &size, 200, "null", "", false},
		{"no size and null", nil, 200, "null", "unverified-writable-layer", false},
		{"empty HTTP response", &size, 200, "", "", true},
		{"changed file", &size, 200, `[{"Kind":1,"Path":"/customer"}]`, "writable-layer", false},
		{"truncated diff", &size, 200, "[", "", true},
		{"unavailable diff", &size, 503, `{"message":"unavailable"}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected, c := rowlessFixture()
			c.SizeRw = tc.size
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/version"):
					_, _ = w.Write([]byte(`{"Components":[{"Name":"Engine"}]}`))
				case strings.HasSuffix(r.URL.Path, "/containers/"+c.ID+"/json"):
					_ = json.NewEncoder(w).Encode(c)
				case strings.HasSuffix(r.URL.Path, "/containers/"+c.ID+"/changes"):
					w.WriteHeader(tc.code)
					_, _ = w.Write([]byte(tc.body))
				case strings.HasSuffix(r.URL.Path, "/images/"+c.Image+"/json"):
					_ = json.NewEncoder(w).Encode(map[string]string{"Id": c.Image})
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					w.WriteHeader(http.StatusInternalServerError)
				}
			}))
			t.Cleanup(srv.Close)
			cli, err := client.New(client.WithHost("tcp://"+strings.TrimPrefix(srv.URL, "http://")), client.WithAPIVersion("1.47"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cli.Close() })
			reason, err := (&cleanupRuntime{client: cli}).OrphanRetentionReason(t.Context(), expected)
			if reason != tc.reason || (err != nil) != tc.wantError {
				t.Fatalf("reason=%q err=%v", reason, err)
			}
		})
	}
}

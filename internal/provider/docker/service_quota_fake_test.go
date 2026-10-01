package docker

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/container"
)

// fakeQuotaDaemon is a scripted Docker daemon for quota-enforced service
// tests. Fields are the daemon's observable state; the recorded slices say
// which mutating calls the provider made.
type fakeQuotaDaemon struct {
	t *testing.T

	mu sync.Mutex
	// info answered on GET /info.
	swapLimit, pidsLimit bool
	// volumes by name (GET /volumes/{name}).
	volumes map[string]map[string]any
	// containers listed on GET /containers/json.
	containers []map[string]any
	// hostConfigs answered on GET /containers/{id}/json.
	hostConfigs map[string]*container.HostConfig
	// imageConfig answered on GET /images/{ref}/json.
	imageConfig map[string]any

	volumeCreates    []map[string]any
	volumeRemoves    []string
	containerCreates []container.CreateRequest
	stopped, removed []string
	updated          []string
	volumeRemoveErr  error
	order            []string
}

func newFakeQuotaDaemon(t *testing.T) *fakeQuotaDaemon {
	return &fakeQuotaDaemon{
		t: t, swapLimit: true, pidsLimit: true,
		volumes:     map[string]map[string]any{},
		hostConfigs: map[string]*container.HostConfig{},
		imageConfig: map[string]any{},
	}
}

func (d *fakeQuotaDaemon) mutatingCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.volumeCreates) + len(d.volumeRemoves) + len(d.containerCreates) + len(d.stopped) + len(d.removed) + len(d.updated)
}

func (d *fakeQuotaDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	path := r.URL.Path
	id := func(suffix string) string {
		p := strings.TrimSuffix(path, suffix)
		return p[strings.LastIndex(p, "/")+1:]
	}
	switch {
	case strings.HasSuffix(path, "/info"):
		_ = json.NewEncoder(w).Encode(map[string]any{"SwapLimit": d.swapLimit, "PidsLimit": d.pidsLimit})
	case strings.HasSuffix(path, "/volumes/create"):
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		d.volumeCreates = append(d.volumeCreates, req)
		d.order = append(d.order, "volume-create")
		_ = json.NewEncoder(w).Encode(map[string]any{"Name": req["Name"]})
	case strings.Contains(path, "/volumes/") && r.Method == http.MethodGet:
		v, ok := d.volumes[id("")]
		if !ok {
			http.Error(w, `{"message":"no such volume"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(v)
	case strings.HasSuffix(path, "/volumes") && r.Method == http.MethodGet:
		list := make([]map[string]any, 0, len(d.volumes))
		for _, v := range d.volumes {
			list = append(list, v)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Volumes": list})
	case strings.Contains(path, "/volumes/") && r.Method == http.MethodDelete:
		d.order = append(d.order, "volume-remove")
		if d.volumeRemoveErr != nil {
			http.Error(w, `{"message":"`+d.volumeRemoveErr.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		d.volumeRemoves = append(d.volumeRemoves, id(""))
		w.WriteHeader(http.StatusNoContent)
	case strings.HasSuffix(path, "/containers/json"):
		_ = json.NewEncoder(w).Encode(d.containers)
	case strings.HasSuffix(path, "/containers/create"):
		var req container.CreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			d.t.Error(err)
		}
		d.containerCreates = append(d.containerCreates, req)
		_ = json.NewEncoder(w).Encode(map[string]string{"Id": "replacement"})
	case strings.HasSuffix(path, "/update"):
		d.updated = append(d.updated, id("/update"))
		var body container.UpdateConfig
		_ = json.NewDecoder(r.Body).Decode(&body)
		if hc := d.hostConfigs[id("/update")]; hc != nil {
			hc.RestartPolicy = body.RestartPolicy
		}
		_, _ = w.Write([]byte(`{}`))
	case strings.HasSuffix(path, "/stop"):
		d.stopped = append(d.stopped, id("/stop"))
		w.WriteHeader(http.StatusNoContent)
	case strings.HasSuffix(path, "/start"):
		w.WriteHeader(http.StatusNoContent)
	case strings.Contains(path, "/containers/") && strings.HasSuffix(path, "/json"):
		hc, ok := d.hostConfigs[id("/json")]
		if !ok {
			http.Error(w, `{"message":"no such container"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": id("/json"), "HostConfig": hc, "State": map[string]any{"Running": true}})
	case strings.Contains(path, "/containers/") && r.Method == http.MethodDelete:
		d.removed = append(d.removed, id(""))
		w.WriteHeader(http.StatusNoContent)
	case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "image", "Config": d.imageConfig})
	case strings.HasSuffix(path, "/images/create"):
		_, _ = w.Write([]byte("{}"))
	default:
		d.t.Errorf("unexpected request %s %s", r.Method, path)
		w.WriteHeader(http.StatusInternalServerError)
	}
}

package docker

// #3964: after an upgrade a crew container keeps executing the sidecar binary
// it was created with (the bind mount pins the old inode). The provider must
// recreate such a container on its own whenever that is safe (stopped, or
// running and confirmed idle under a runtime-use reservation) and must never
// tear down a runtime that is in use.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/dockerutil"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

const (
	refreshCrewID   = "crew-sidecar-1"
	refreshCrewSlug = "eng"
	refreshInstance = "installation"
	refreshOldID    = "old-cid-0123456789ab"
)

// fakeCrewContainer is one container the stateful fake daemon knows about.
type fakeCrewContainer struct {
	id     string
	name   string
	labels map[string]string
	status string
}

// sidecarFakeDaemon is a stateful Docker API fake: stop really stops, remove
// really removes and create really creates, so a test can drive a full
// stop -> remove -> create cycle and observe the end state.
type sidecarFakeDaemon struct {
	mu         sync.Mutex
	containers map[string]*fakeCrewContainer
	order      []string
	calls      []string
	seq        int
	crewName   string
}

func (d *sidecarFakeDaemon) record(s string) {
	d.calls = append(d.calls, s)
}

func (d *sidecarFakeDaemon) snapshot() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

func (d *sidecarFakeDaemon) count(prefix string) int {
	n := 0
	for _, c := range d.snapshot() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (d *sidecarFakeDaemon) crewContainer() *fakeCrewContainer {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, id := range d.order {
		if c := d.containers[id]; c != nil && c.name == d.crewName {
			cp := *c
			return &cp
		}
	}
	return nil
}

func (d *sidecarFakeDaemon) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		d.mu.Lock()
		defer d.mu.Unlock()
		writeJSON := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}
		switch {
		case strings.HasSuffix(path, "/containers/json") && r.Method == http.MethodGet:
			var items []map[string]any
			for _, id := range d.order {
				c := d.containers[id]
				if c == nil {
					continue
				}
				items = append(items, map[string]any{"Id": c.id, "Names": []string{"/" + c.name}, "State": c.status, "Labels": c.labels})
			}
			writeJSON(items)
		case strings.HasSuffix(path, "/containers/create"):
			var body struct {
				Labels map[string]string `json:"Labels"`
			}
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			name := r.URL.Query().Get("name")
			if name == d.crewName {
				for _, id := range d.order {
					if c := d.containers[id]; c != nil && c.name == name {
						w.WriteHeader(http.StatusConflict)
						writeJSON(map[string]string{"message": "Conflict. The container name is already in use"})
						return
					}
				}
			}
			d.seq++
			id := fmt.Sprintf("new-cid-%02d-0123456789", d.seq)
			d.containers[id] = &fakeCrewContainer{id: id, name: name, labels: body.Labels, status: "created"}
			d.order = append(d.order, id)
			if name == d.crewName {
				d.record("create " + id)
			}
			writeJSON(map[string]any{"Id": id})
		case strings.Contains(path, "/containers/") && strings.HasSuffix(path, "/json"):
			c := d.containers[extractContainerID(path)]
			if c == nil {
				w.WriteHeader(http.StatusNotFound)
				writeJSON(map[string]string{"message": "No such container"})
				return
			}
			labels := map[string]string{}
			for k, v := range c.labels {
				labels[k] = v
			}
			writeJSON(map[string]any{
				"Id":    c.id,
				"Name":  "/" + c.name,
				"Image": "sha256:" + strings.Repeat("a", 64),
				"State": map[string]any{"Status": c.status, "Running": c.status == "running"},
				"Config": map[string]any{
					"Image":  "default/runtime:latest",
					"Labels": labels,
				},
				"Mounts": []map[string]any{
					{"Destination": "/crew", "Type": "volume"},
					{"Destination": "/home/agent", "Type": "volume"},
					{"Destination": "/opt/crew-tools", "Type": "volume"},
				},
				"HostConfig": map[string]any{"Tmpfs": map[string]string{"/secrets": secretsTmpfsSpec}},
			})
		case strings.Contains(path, "/containers/") && strings.HasSuffix(path, "/stop"):
			id := extractContainerID(path)
			if c := d.containers[id]; c != nil {
				c.status = "exited"
			}
			d.record("stop " + id)
			w.WriteHeader(http.StatusNoContent)
		case strings.Contains(path, "/containers/") && strings.HasSuffix(path, "/start"):
			if c := d.containers[extractContainerID(path)]; c != nil {
				c.status = "running"
			}
			w.WriteHeader(http.StatusNoContent)
		case strings.Contains(path, "/containers/") && strings.HasSuffix(path, "/wait"):
			writeJSON(map[string]any{"StatusCode": 0})
		case strings.Contains(path, "/containers/") && r.Method == http.MethodDelete:
			id := extractContainerID(path)
			c := d.containers[id]
			if c == nil {
				w.WriteHeader(http.StatusNotFound)
				writeJSON(map[string]string{"message": "No such container"})
				return
			}
			force := r.URL.Query().Get("force")
			if c.status == "running" && force != "1" && force != "true" {
				w.WriteHeader(http.StatusConflict)
				writeJSON(map[string]string{"message": "cannot remove a running container"})
				return
			}
			if c.name == d.crewName {
				d.record(fmt.Sprintf("remove %s force=%v", id, force == "1" || force == "true"))
			}
			delete(d.containers, id)
		case strings.HasSuffix(path, "/volumes") && r.Method == http.MethodGet:
			writeJSON(map[string]any{"Volumes": []any{}, "Warnings": nil})
		case strings.HasSuffix(path, "/volumes/create"):
			writeJSON(map[string]any{"Name": "fake-volume"})
		case strings.HasSuffix(path, "/networks"):
			writeJSON([]map[string]any{{"Name": "crewship-test-net", "Id": "net-existing"}})
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			writeJSON(map[string]any{"Id": "sha256:fakeimg", "RepoDigests": []string{}, "Config": map[string]any{"Env": []string{}}})
		case strings.HasSuffix(path, "/exec") && r.Method == http.MethodPost:
			writeJSON(map[string]any{"Id": "fake-exec-id"})
		case strings.Contains(path, "/exec/") && strings.HasSuffix(path, "/json"):
			writeJSON(map[string]any{"Running": false, "ExitCode": 0})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}
}

// writeFakeSidecar writes an ELF-looking sidecar binary and returns its path
// and the hash the provider computes for it.
func writeFakeSidecar(t *testing.T, content string) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "crewship-sidecar")
	if err := os.WriteFile(path, []byte("\x7fELF"+content), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &Provider{cfg: Config{SidecarBinaryPath: path}}
	h := p.onDiskSidecarHash()
	if h == "" {
		t.Fatal("fake sidecar hash is empty")
	}
	return path, h
}

// newSidecarRefreshFixture returns a provider whose staged sidecar hashes to
// the returned value and a daemon holding one crew container stamped with
// stamp ("" = no stamp, as on a container from before #3964) in state status.
func newSidecarRefreshFixture(t *testing.T, stamp, status string) (*Provider, *sidecarFakeDaemon, string) {
	t.Helper()
	sidecarPath, hash := writeFakeSidecar(t, "new-sidecar-"+t.Name())

	d := &sidecarFakeDaemon{containers: map[string]*fakeCrewContainer{}}
	srv := httptest.NewServer(d.handler())
	cli, err := client.New(client.WithHost(srv.URL), client.WithAPIVersion("1.43"))
	if err != nil {
		srv.Close()
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() {
		_ = cli.Close()
		srv.Close()
	})
	p := &Provider{
		client: cli,
		cfg: Config{
			RuntimeImage:      "default/runtime:latest",
			Network:           "crewship-test-net",
			OutputBasePath:    t.TempDir(),
			SidecarBinaryPath: sidecarPath,
			EntrypointPath:    "/fake/entrypoint.sh",
			InstanceID:        refreshInstance,
		},
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		digestResolver: dockerutil.NewDigestResolver(0, 0),
	}
	d.crewName = p.CrewContainerName(refreshCrewID, refreshCrewSlug)
	// The host bind dirs a stopped container's restart checks for.
	for _, dir := range []string{
		filepath.Join(p.cfg.OutputBasePath, "workspaces", refreshCrewID),
		filepath.Join(p.cfg.OutputBasePath, refreshCrewID),
		filepath.Join(p.cfg.OutputBasePath, "crews", refreshCrewID),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	labels := map[string]string{
		resourcelifecycle.InstanceLabel: refreshInstance,
		crewCrewIDLabel:                 refreshCrewID,
		crewKindLabel:                   crewRuntimeKind,
	}
	// Current in every other respect, so only the sidecar can trigger a
	// recreate.
	if digest := p.crewRuntimeContractDigest(); digest != "" {
		labels["crewship.runtime-contract"] = digest
	}
	if stamp != "" {
		labels["crewship.sidecar-hash"] = stamp
	}
	d.containers[refreshOldID] = &fakeCrewContainer{id: refreshOldID, name: d.crewName, labels: labels, status: status}
	d.order = append(d.order, refreshOldID)
	return p, d, hash
}

func refreshCrew() provider.CrewConfig {
	return provider.CrewConfig{ID: refreshCrewID, Slug: refreshCrewSlug}
}

// recreateHook captures sidecar.recreated events through the provider's
// public hook.
type recreateHook struct {
	mu     sync.Mutex
	events []provider.SidecarRecreatedEvent
}

func (h *recreateHook) install(t *testing.T, p *Provider) {
	t.Helper()
	var r provider.SidecarRecreatedReporter = p
	r.SetSidecarRecreatedHook(func(_ context.Context, ev provider.SidecarRecreatedEvent) {
		h.mu.Lock()
		h.events = append(h.events, ev)
		h.mu.Unlock()
	})
}

func (h *recreateHook) snapshot() []provider.SidecarRecreatedEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]provider.SidecarRecreatedEvent(nil), h.events...)
}

// TestSidecarRefreshNewContainerIsStamped: the stamp is what every later
// comparison is made against.
func TestSidecarRefreshNewContainerIsStamped(t *testing.T) {
	p, d, hash := newSidecarRefreshFixture(t, "", "exited")
	delete(d.containers, refreshOldID)
	d.order = nil
	if _, err := p.EnsureCrewRuntime(t.Context(), refreshCrew()); err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	c := d.crewContainer()
	if c == nil {
		t.Fatal("no crew container created")
	}
	if got := c.labels["crewship.sidecar-hash"]; got != hash {
		t.Fatalf("crewship.sidecar-hash = %q, want %q", got, hash)
	}
}

// TestSidecarRefreshReconcile covers both entry points: a bare
// EnsureCrewRuntime (no reservation) and AcquireCrewRuntimeUse.
func TestSidecarRefreshReconcile(t *testing.T) {
	const oldHash = "0b6348e06bf8"
	for _, tc := range []struct {
		name     string
		stamp    string // "" = no stamp; "current" = the staged hash
		status   string
		acquire  bool
		idle     error // idle verifier verdict; only for acquire
		recreate bool
	}{
		{name: "current stamp running is reused", stamp: "current", status: "running", acquire: true},
		{name: "current stamp stopped is restarted, not recreated", stamp: "current", status: "exited"},
		{name: "outdated stamp stopped is recreated", stamp: oldHash, status: "exited", recreate: true},
		{name: "missing stamp stopped is recreated", stamp: "", status: "exited", recreate: true},
		{name: "outdated stamp created is recreated", stamp: oldHash, status: "created", recreate: true},
		{name: "outdated running without reservation is reused", stamp: oldHash, status: "running"},
		{name: "outdated running idle under reservation is recreated", stamp: oldHash, status: "running", acquire: true, recreate: true},
		{name: "missing stamp running idle under reservation is recreated", stamp: "", status: "running", acquire: true, recreate: true},
		{name: "outdated running busy under reservation is reused", stamp: oldHash, status: "running", acquire: true, idle: errors.New("runtime has a local invocation")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, d, hash := newSidecarRefreshFixture(t, "", tc.status)
			stamp := tc.stamp
			if stamp == "current" {
				stamp = hash
			}
			if stamp != "" {
				d.containers[refreshOldID].labels["crewship.sidecar-hash"] = stamp
			}
			var verified atomic.Int32
			p.SetRuntimeIdleVerifier(func(_ context.Context, crewID, containerID string) error {
				verified.Add(1)
				if crewID != refreshCrewID || containerID != refreshOldID {
					t.Errorf("idle verifier asked about %s/%s", crewID, containerID)
				}
				return tc.idle
			})
			hook := &recreateHook{}
			hook.install(t, p)

			var id string
			var err error
			if tc.acquire {
				var use *provider.RuntimeUse
				use, err = p.AcquireCrewRuntimeUse(t.Context(), refreshCrew())
				if err == nil {
					id = use.ContainerID()
					use.Release()
				}
			} else {
				id, err = p.EnsureCrewRuntime(t.Context(), refreshCrew())
			}
			if err != nil {
				t.Fatalf("ensure: %v (calls %v)", err, d.snapshot())
			}

			c := d.crewContainer()
			if c == nil {
				t.Fatalf("crew container gone (calls %v)", d.snapshot())
			}
			if id != c.id {
				t.Fatalf("returned %q, daemon holds %q", id, c.id)
			}
			// A running runtime is only ever stopped (after the idle check)
			// and then removed without force.
			for _, call := range d.snapshot() {
				if tc.status == "running" && strings.HasPrefix(call, "remove ") && strings.HasSuffix(call, "force=true") {
					t.Fatalf("outdated sidecar force-removed a running runtime: %v", d.snapshot())
				}
			}
			events := hook.snapshot()
			if !tc.recreate {
				if id != refreshOldID || d.count("create ") != 0 || d.count("remove ") != 0 || d.count("stop ") != 0 {
					t.Fatalf("runtime must be kept: id=%s calls=%v", id, d.snapshot())
				}
				if len(events) != 0 {
					t.Fatalf("no sidecar.recreated expected, got %+v", events)
				}
				return
			}
			if id == refreshOldID || d.count("create ") != 1 || d.count("remove ") != 1 {
				t.Fatalf("runtime must be recreated exactly once: id=%s calls=%v", id, d.snapshot())
			}
			if got := c.labels["crewship.sidecar-hash"]; got != hash {
				t.Fatalf("replacement stamped %q, want %q", got, hash)
			}
			if c.status != "running" {
				t.Fatalf("replacement status %q, want running", c.status)
			}
			if tc.status == "running" && verified.Load() == 0 {
				t.Fatal("a running runtime was replaced without the idle verifier")
			}
			if len(events) != 1 {
				t.Fatalf("want one sidecar.recreated event, got %+v", events)
			}
			ev := events[0]
			if ev.CrewID != refreshCrewID || ev.PreviousContainerID != refreshOldID || ev.ContainerID != c.id ||
				ev.PreviousSidecarHash != stamp || ev.SidecarHash != hash {
				t.Fatalf("event = %+v, want crew %s %s->%s hash %q->%q", ev, refreshCrewID, refreshOldID, c.id, stamp, hash)
			}
		})
	}
}

// TestSidecarRefreshNeverTouchesHeldRuntime: while another run holds the
// runtime, a new run is admitted into the outdated container at once (no
// waiting for the holder), and nothing is stopped or removed. Once the holder
// is gone, the next start replaces it.
func TestSidecarRefreshNeverTouchesHeldRuntime(t *testing.T) {
	p, d, hash := newSidecarRefreshFixture(t, "0b6348e06bf8", "running")
	p.SetRuntimeIdleVerifier(func(context.Context, string, string) error { return nil })

	held, err := p.RetainCrewRuntimeUse(t.Context(), refreshCrewID, refreshOldID)
	if err != nil {
		t.Fatal(err)
	}
	use, err := p.AcquireCrewRuntimeUse(t.Context(), refreshCrew())
	if err != nil {
		t.Fatalf("acquire beside an active run: %v", err)
	}
	if use.ContainerID() != refreshOldID || d.count("stop ") != 0 || d.count("remove ") != 0 {
		t.Fatalf("active runtime disturbed: id=%s calls=%v", use.ContainerID(), d.snapshot())
	}
	use.Release()
	held.Release()

	// Inside the warm TTL: the outdated runtime must not have been cached as
	// ready, so this start reconciles and replaces it.
	next, err := p.AcquireCrewRuntimeUse(t.Context(), refreshCrew())
	if err != nil {
		t.Fatalf("acquire after the run ended: %v", err)
	}
	defer next.Release()
	c := d.crewContainer()
	if next.ContainerID() == refreshOldID || c == nil || c.id != next.ContainerID() || c.labels["crewship.sidecar-hash"] != hash {
		t.Fatalf("idle outdated runtime not replaced: id=%s calls=%v", next.ContainerID(), d.snapshot())
	}
}

// TestSidecarRefreshConcurrentStarts: runs starting together on an idle
// outdated runtime never double-recreate it, and none of them is handed a
// container that is stopped or gone while it holds it. A start that loses the
// race for the exclusive gate reuses the old runtime (it is in use, so not
// idle); the first start after the burst replaces it.
func TestSidecarRefreshConcurrentStarts(t *testing.T) {
	p, d, hash := newSidecarRefreshFixture(t, "", "running")
	p.SetRuntimeIdleVerifier(func(context.Context, string, string) error { return nil })
	hook := &recreateHook{}
	hook.install(t, p)

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			use, err := p.AcquireCrewRuntimeUse(t.Context(), refreshCrew())
			if err != nil {
				errs[i] = err
				return
			}
			defer use.Release()
			d.mu.Lock()
			c := d.containers[use.ContainerID()]
			running := c != nil && c.status == "running"
			d.mu.Unlock()
			if !running {
				errs[i] = fmt.Errorf("handed runtime %s that is not running", use.ContainerID())
			}
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
	}
	if d.count("create ") > 1 || d.count("remove ") > 1 || d.count("stop ") > 1 {
		t.Fatalf("outdated runtime recreated more than once: %v", d.snapshot())
	}

	// The crew is idle now: the next start converges it if the burst did not.
	use, err := p.AcquireCrewRuntimeUse(t.Context(), refreshCrew())
	if err != nil {
		t.Fatalf("acquire after the burst: %v", err)
	}
	use.Release()
	c := d.crewContainer()
	if c == nil || c.id != use.ContainerID() || c.labels["crewship.sidecar-hash"] != hash {
		t.Fatalf("crew not converged on the current sidecar: calls=%v", d.snapshot())
	}
	if d.count("create ") != 1 || d.count("remove ") != 1 {
		t.Fatalf("want exactly one recreate, got calls %v", d.snapshot())
	}
	if got := hook.snapshot(); len(got) != 1 {
		t.Fatalf("want exactly one sidecar.recreated, got %d: %+v", len(got), got)
	}
}

// TestSidecarRefreshNoHashNoOpinion: an unreadable staged binary (unknown
// hash) never recreates anything.
func TestSidecarRefreshNoHashNoOpinion(t *testing.T) {
	p, d, _ := newSidecarRefreshFixture(t, "0b6348e06bf8", "exited")
	p.cfg.SidecarBinaryPath = filepath.Join(t.TempDir(), "missing")
	if _, err := p.EnsureCrewRuntime(t.Context(), refreshCrew()); err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if d.count("create ") != 0 || d.count("remove ") != 0 {
		t.Fatalf("recreated without a known sidecar hash: %v", d.snapshot())
	}
}

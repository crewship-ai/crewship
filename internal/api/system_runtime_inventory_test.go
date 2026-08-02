package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider/docker"
)

// GET /api/v1/system/runtime is the only thing that can tell an operator what
// container runtimes their machine has. Until #1690 it could name at most two
// — one Docker-compatible result plus Apple — because the detector it called
// returned on the first socket that answered, and it had no way to say which
// of them the running server had actually connected to.
//
// These tests pin the inventory contract: every runtime that answered, each
// with its own daemon's version and socket, and exactly one `in_use` flag
// derived from the provider this process is really holding.

// adminRuntimeRequest builds an authenticated ADMIN request — the role floor
// above which the handler stops redacting host detail (#865).
func adminRuntimeRequest() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/runtime", nil)
	ctx := withUser(req.Context(), &AuthUser{ID: "u1"})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	return req.WithContext(ctx)
}

// stubbedRuntimeHandler returns a handler whose probes are replaced by fixed
// answers, so the test asserts the handler's own logic rather than whatever
// happens to be installed on the machine running `go test`.
func stubbedRuntimeHandler(t *testing.T, dockerRuntimes []docker.DetectResult, appleVersion string) *SystemHandler {
	t.Helper()
	h := NewSystemHandler(testLogger(), "test")
	h.detectDocker = func(context.Context) []docker.DetectResult { return dockerRuntimes }
	h.detectApple = func(context.Context) (string, error) {
		if appleVersion == "" {
			return "", errNoAppleRuntimeForTest
		}
		return appleVersion, nil
	}
	// Default: nothing running. Individual tests override.
	h.activeRuntime = func() (docker.DetectResult, bool, bool) { return docker.DetectResult{}, false, false }
	return h
}

// usingDocker is the ground truth a running docker provider reports: its own
// Detected(), which is what SameRuntimeEndpoint compares against.
func usingDocker(d docker.DetectResult) func() (docker.DetectResult, bool, bool) {
	return func() (docker.DetectResult, bool, bool) { return d, false, true }
}

func usingApple() (docker.DetectResult, bool, bool) { return docker.DetectResult{}, true, true }

var errNoAppleRuntimeForTest = &stubError{"apple container CLI not found"}

type stubError struct{ s string }

func (e *stubError) Error() string { return e.s }

// decodedEntry decodes one element of the `runtimes` array.
type decodedEntry struct {
	Runtime string `json:"runtime"`
	Version string `json:"version"`
	Socket  string `json:"socket"`
	InUse   bool   `json:"in_use"`
}

func runtimeBody(t *testing.T, h *SystemHandler) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	h.Runtime(w, adminRuntimeRequest())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

func decodeEntries(t *testing.T, body map[string]any) []decodedEntry {
	t.Helper()
	raw, err := json.Marshal(body["runtimes"])
	if err != nil {
		t.Fatalf("re-marshal runtimes: %v", err)
	}
	var out []decodedEntry
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode runtimes: %v", err)
	}
	return out
}

// The value, not the shape: three runtimes answered, three come back, each
// carrying ITS OWN version and socket rather than the winner's.
func TestSystemRuntime_ReportsEveryDetectedRuntime(t *testing.T) {
	t.Parallel()
	h := stubbedRuntimeHandler(t, []docker.DetectResult{
		{Runtime: "orbstack", Version: "29.4.0", Socket: "/var/run/docker.sock", Host: "unix:///var/run/docker.sock"},
		{Runtime: "podman", Version: "6.0.2", Socket: "/run/user/501/podman/podman.sock", Host: "unix:///run/user/501/podman/podman.sock"},
	}, "1.2.0")

	got := decodeEntries(t, runtimeBody(t, h))
	want := []decodedEntry{
		{Runtime: "orbstack", Version: "29.4.0", Socket: "/var/run/docker.sock"},
		{Runtime: "podman", Version: "6.0.2", Socket: "/run/user/501/podman/podman.sock"},
		{Runtime: "apple", Version: "1.2.0", Socket: ""},
	}
	if len(got) != len(want) {
		t.Fatalf("runtimes = %+v, want %d entries", got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("runtimes[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// in_use follows the provider this process is really holding — not the probe
// order. Podman first in the list, OrbStack the one actually dialled.
func TestSystemRuntime_InUseFollowsTheActiveProvider(t *testing.T) {
	t.Parallel()
	h := stubbedRuntimeHandler(t, []docker.DetectResult{
		{Runtime: "podman", Version: "6.0.2", Socket: "/run/user/501/podman/podman.sock"},
		{Runtime: "orbstack", Version: "29.4.0", Socket: "/var/run/docker.sock"},
	}, "1.2.0")
	h.activeRuntime = usingDocker(docker.DetectResult{
		Runtime: "orbstack", Version: "29.4.0",
		Socket: "/var/run/docker.sock", Host: "unix:///var/run/docker.sock",
	})

	body := runtimeBody(t, h)
	got := decodeEntries(t, body)

	inUse := []string{}
	for _, e := range got {
		if e.InUse {
			inUse = append(inUse, e.Runtime)
		}
	}
	if len(inUse) != 1 || inUse[0] != "orbstack" {
		t.Fatalf("in_use runtimes = %v, want exactly [orbstack]; entries=%+v", inUse, got)
	}

	// The top-level summary names the runtime in use, not the first probed.
	if body["runtime"] != "orbstack" {
		t.Errorf("runtime = %v, want orbstack", body["runtime"])
	}
	if body["version"] != "29.4.0" {
		t.Errorf("version = %v, want 29.4.0", body["version"])
	}
	if body["socket"] != "/var/run/docker.sock" {
		t.Errorf("socket = %v, want /var/run/docker.sock", body["socket"])
	}
}

// The apple provider has no socket, so it is matched by name.
func TestSystemRuntime_InUseCanBeAppleContainers(t *testing.T) {
	t.Parallel()
	h := stubbedRuntimeHandler(t, []docker.DetectResult{
		{Runtime: "docker", Version: "28.0.1", Socket: "/var/run/docker.sock"},
	}, "1.2.0")
	h.activeRuntime = usingApple

	got := decodeEntries(t, runtimeBody(t, h))
	for _, e := range got {
		want := e.Runtime == "apple"
		if e.InUse != want {
			t.Errorf("%s in_use = %v, want %v", e.Runtime, e.InUse, want)
		}
	}
}

// `crewship start --no-docker` (and a provider that failed to build) leaves the
// server with no container provider at all. Runtimes are still PRESENT on the
// host; none of them is in use, and the response must not pretend otherwise.
func TestSystemRuntime_NoActiveProviderMarksNothingInUse(t *testing.T) {
	t.Parallel()
	h := stubbedRuntimeHandler(t, []docker.DetectResult{
		{Runtime: "orbstack", Version: "29.4.0", Socket: "/var/run/docker.sock"},
	}, "1.2.0")

	body := runtimeBody(t, h)
	if body["available"] != true {
		t.Errorf("available = %v, want true (the runtimes exist, they are just unused)", body["available"])
	}
	for _, e := range decodeEntries(t, body) {
		if e.InUse {
			t.Errorf("%s reported in_use with no container provider running", e.Runtime)
		}
	}
	if body["runtime"] != nil {
		t.Errorf("runtime = %v, want null when nothing is in use", body["runtime"])
	}
}

// DOCKER_HOST can point the server at a daemon that is not in the candidate
// socket list at all (a remote tcp:// engine). The runtime in use must never be
// missing from the inventory that claims to list what is in use.
func TestSystemRuntime_ActiveRuntimeOutsideTheProbeListIsStillListed(t *testing.T) {
	t.Parallel()
	h := stubbedRuntimeHandler(t, []docker.DetectResult{
		{Runtime: "podman", Version: "6.0.2", Socket: "/run/user/501/podman/podman.sock"},
	}, "")
	h.activeRuntime = usingDocker(docker.DetectResult{
		Runtime: "docker", Version: "28.0.1",
		Socket: "tcp://build-host:2376", Host: "tcp://build-host:2376",
	})

	got := decodeEntries(t, runtimeBody(t, h))
	var found *decodedEntry
	for i := range got {
		if got[i].Socket == "tcp://build-host:2376" {
			found = &got[i]
		}
	}
	if found == nil {
		t.Fatalf("runtimes = %+v, want an entry for the daemon actually in use", got)
	}
	if !found.InUse || found.Runtime != "docker" {
		t.Errorf("remote entry = %+v, want {runtime:docker in_use:true}", *found)
	}
}

// install_links is how the UI offers a way forward. It was only emitted when
// NOTHING was detected, so an operator with one runtime could not be told what
// the other six were.
func TestSystemRuntime_InstallLinksAccompanyAnAvailableRuntime(t *testing.T) {
	t.Parallel()
	h := stubbedRuntimeHandler(t, []docker.DetectResult{
		{Runtime: "docker", Version: "28.0.1", Socket: "/var/run/docker.sock"},
	}, "")

	body := runtimeBody(t, h)
	links, ok := body["install_links"].(map[string]any)
	if !ok {
		t.Fatalf("install_links = %v, want a map alongside an available runtime", body["install_links"])
	}
	for _, want := range []string{"docker", "podman", "colima", "orbstack", "rancher", "apple"} {
		if _, ok := links[want]; !ok {
			t.Errorf("install_links missing %q (have %v)", want, links)
		}
	}
}

// The machine with nothing installed is where install_links matters most, and
// where a missing entry is least likely to be noticed — the operator has no
// runtime to compare the list against.
func TestSystemRuntime_NoRuntimeAtAllStillOffersEveryInstallLink(t *testing.T) {
	t.Parallel()
	h := stubbedRuntimeHandler(t, nil, "")

	body := runtimeBody(t, h)
	if body["available"] != false {
		t.Errorf("available = %v, want false", body["available"])
	}
	links, ok := body["install_links"].(map[string]any)
	if !ok {
		t.Fatalf("install_links = %v, want a map when nothing is installed", body["install_links"])
	}
	if len(links) != len(installLinks) {
		t.Errorf("install_links has %d entries, want all %d", len(links), len(installLinks))
	}
	for want := range installLinks {
		if _, ok := links[want]; !ok {
			t.Errorf("install_links missing %q (have %v)", want, links)
		}
	}
}

// The #865 redaction still holds: a caller below ADMIN gets availability and
// nothing else — no socket paths, no daemon versions, no inventory.
func TestSystemRuntime_NonAdminSeesNoHostDetail(t *testing.T) {
	t.Parallel()
	h := stubbedRuntimeHandler(t, []docker.DetectResult{
		{Runtime: "orbstack", Version: "29.4.0", Socket: "/var/run/docker.sock"},
	}, "1.2.0")
	h.activeRuntime = usingDocker(docker.DetectResult{
		Runtime: "orbstack", Socket: "/var/run/docker.sock", Host: "unix:///var/run/docker.sock",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/runtime", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "u1"}))
	w := httptest.NewRecorder()
	h.Runtime(w, req)

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["available"] != true {
		t.Errorf("available = %v, want true", body["available"])
	}
	for _, leaked := range []string{"runtimes", "socket", "version", "runtime"} {
		if _, ok := body[leaked]; ok {
			t.Errorf("non-admin response leaked %q: %v", leaked, body)
		}
	}
}

// activeRuntimeFrom is the bridge between the process's container provider and
// the in_use flag. The docker provider can name the daemon it dialled; the
// apple provider cannot, and is identified by elimination — those two are the
// only providers `container.provider` can build (internal/config/config.go).
func TestActiveRuntimeFrom(t *testing.T) {
	t.Parallel()

	t.Run("nil provider means nothing is in use", func(t *testing.T) {
		got, isApple, ok := activeRuntimeFrom(nil)()
		if ok || isApple || got != (docker.DetectResult{}) {
			t.Errorf("got (%+v,%v,%v), want (zero,false,false)", got, isApple, ok)
		}
	})

	t.Run("docker provider hands back its own detection verbatim", func(t *testing.T) {
		want := docker.DetectResult{
			Runtime: "orbstack", Version: "29.4.0",
			Socket: "/var/run/docker.sock", Host: "unix:///var/run/docker.sock",
		}
		got, isApple, ok := activeRuntimeFrom(fakeDetectedProvider{want})()
		if !ok || isApple || got != want {
			t.Errorf("got (%+v,%v,%v), want (%+v,false,true)", got, isApple, ok, want)
		}
	})

	t.Run("a provider that cannot name a daemon is apple", func(t *testing.T) {
		got, isApple, ok := activeRuntimeFrom(struct{}{})()
		if !ok || !isApple || got != (docker.DetectResult{}) {
			t.Errorf("got (%+v,%v,%v), want (zero,true,true)", got, isApple, ok)
		}
	})
}

// The trap the provider author called out: Detect stores DOCKER_HOST verbatim
// ("unix:///var/run/docker.sock") while DetectAll stores a plain path
// ("/var/run/docker.sock"). Comparing Socket strings would report NOTHING in
// use on the single most common setup there is — a server started with
// DOCKER_HOST pointing at the socket it would have found anyway.
func TestSystemRuntime_InUseSurvivesTheTwoSpellingsOfOneSocket(t *testing.T) {
	t.Parallel()
	h := stubbedRuntimeHandler(t, []docker.DetectResult{
		// As DetectAll reports it: plain path.
		{Runtime: "orbstack", Version: "29.4.0", Socket: "/var/run/docker.sock", Host: "unix:///var/run/docker.sock"},
	}, "")
	// As Detect reports it when DOCKER_HOST is set: the URL, verbatim, in both
	// Socket and Host.
	h.activeRuntime = usingDocker(docker.DetectResult{
		Runtime: "orbstack", Version: "29.4.0",
		Socket: "unix:///var/run/docker.sock", Host: "unix:///var/run/docker.sock",
	})

	body := runtimeBody(t, h)
	got := decodeEntries(t, body)
	if len(got) != 1 {
		t.Fatalf("runtimes = %+v, want 1 entry (one daemon, one entry)", got)
	}
	if !got[0].InUse {
		t.Errorf("entry %+v not marked in_use — the two spellings of one socket were compared as strings", got[0])
	}
	if body["runtime"] != "orbstack" {
		t.Errorf("runtime = %v, want orbstack", body["runtime"])
	}
}

type fakeDetectedProvider struct{ d docker.DetectResult }

func (f fakeDetectedProvider) Detected() docker.DetectResult { return f.d }

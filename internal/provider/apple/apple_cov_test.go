package apple

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCLI is a stub `container` binary installed on PATH. Every invocation
// appends its arguments (space-joined) to a log file, then runs the
// test-provided shell body.
type fakeCLI struct {
	dir string
	log string
}

// installFakeContainer writes a fake `container` shell script into a temp dir
// and prepends that dir to PATH so the package's exec.Command("container", ...)
// calls hit the stub. The body is plain /bin/sh; $DIR points at the temp dir
// for stateful scripts. Tests using this MUST NOT call t.Parallel (t.Setenv).
func installFakeContainer(t *testing.T, body string) *fakeCLI {
	t.Helper()
	dir := t.TempDir()
	logFile := filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> '" + logFile + "'\n" +
		"DIR='" + dir + "'\n" +
		"export DIR\n" +
		body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "container"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake container: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &fakeCLI{dir: dir, log: logFile}
}

// calls returns one entry per CLI invocation recorded by the fake binary.
func (f *fakeCLI) calls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(f.log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read calls log: %v", err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func (f *fakeCLI) hasCall(t *testing.T, prefix string) bool {
	t.Helper()
	for _, c := range f.calls(t) {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// newTestProvider builds a Provider without going through New (no Detect).
func newTestProvider(cfg Config) *Provider {
	return &Provider{
		cfg:    cfg,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		execs:  make(map[string]*execEntry),
		done:   make(chan struct{}),
	}
}

func TestNewSuccessWithFakeCLI(t *testing.T) {
	fake := installFakeContainer(t, `
case "$1" in
  system)
    if [ "$2" = "version" ]; then
      echo '[{"appName":"container","version":"9.9.9"}]'
    fi
    exit 0;;
  network)
    if [ "$2" = "list" ]; then echo '[]'; fi
    exit 0;;
esac
exit 0`)

	p, err := New(context.Background(), Config{
		RuntimeImage: "img:1",
		Network:      "mynet",
	}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	if p.HostAddress() == "" {
		t.Error("expected non-empty HostAddress")
	}
	if got := p.CrewContainerName("eng"); got != "crewship-team-eng" {
		t.Errorf("CrewContainerName = %q, want crewship-team-eng", got)
	}
	if !fake.hasCall(t, "network create mynet") {
		t.Errorf("expected 'network create mynet' call, got %v", fake.calls(t))
	}
}

func TestNewDetectFails(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no `container` binary anywhere on PATH
	_, err := New(context.Background(), Config{}, nil)
	if err == nil {
		t.Fatal("expected error when container CLI is missing")
	}
	if !strings.Contains(err.Error(), "apple container runtime") {
		t.Errorf("error = %v, want mention of apple container runtime", err)
	}
}

func TestCrewContainerNameCustomPrefix(t *testing.T) {
	p := newTestProvider(Config{ContainerPrefix: "acme"})
	if got := p.CrewContainerName("eng"); got != "acme-team-eng" {
		t.Errorf("CrewContainerName = %q, want acme-team-eng", got)
	}
	p2 := newTestProvider(Config{})
	if got := p2.CrewContainerName("ops"); got != "crewship-team-ops" {
		t.Errorf("CrewContainerName default prefix = %q, want crewship-team-ops", got)
	}
}

func TestEnsureNetwork(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantErr    string
		wantCreate bool
		denyCreate bool
	}{
		{
			name:       "list unavailable is non-fatal",
			body:       `exit 1`,
			denyCreate: true,
		},
		{
			name:       "bad json is non-fatal",
			body:       `echo 'not-json'; exit 0`,
			denyCreate: true,
		},
		{
			name: "network already exists in list",
			body: `
if [ "$2" = "list" ]; then echo '[{"name":"mynet"}]'; fi
exit 0`,
			denyCreate: true,
		},
		{
			name: "creates missing network",
			body: `
if [ "$2" = "list" ]; then echo '[]'; fi
exit 0`,
			wantCreate: true,
		},
		{
			name: "create race already exists is non-fatal",
			body: `
if [ "$2" = "list" ]; then echo '[]'; exit 0; fi
echo 'network mynet already exists' >&2
exit 1`,
		},
		{
			name: "create failure returns error",
			body: `
if [ "$2" = "list" ]; then echo '[]'; exit 0; fi
echo 'boom' >&2
exit 1`,
			wantErr: "create network mynet",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := installFakeContainer(t, tt.body)
			p := newTestProvider(Config{Network: "mynet"})

			err := p.ensureNetwork(context.Background(), "mynet")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ensureNetwork err = %v, want contains %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ensureNetwork: %v", err)
			}
			created := fake.hasCall(t, "network create mynet")
			if tt.wantCreate && !created {
				t.Errorf("expected network create call, got %v", fake.calls(t))
			}
			if tt.denyCreate && created {
				t.Errorf("did not expect network create call, got %v", fake.calls(t))
			}
		})
	}
}

func TestEnsureImage(t *testing.T) {
	tests := []struct {
		name     string
		ref      string
		body     string
		wantErr  string
		wantPull bool
	}{
		{
			name:    "list failure",
			ref:     "img:1",
			body:    `exit 1`,
			wantErr: "list images",
		},
		{
			name:    "bad json",
			ref:     "img:1",
			body:    `echo 'garbage'; exit 0`,
			wantErr: "parse image list",
		},
		{
			name: "exact reference present",
			ref:  "img:1",
			body: `
if [ "$2" = "list" ]; then echo '[{"reference":"img:1"}]'; fi
exit 0`,
		},
		{
			name: "docker.io library prefix match",
			ref:  "alpine:3",
			body: `
if [ "$2" = "list" ]; then echo '[{"reference":"docker.io/library/alpine:3"}]'; fi
exit 0`,
		},
		{
			name: "missing image gets pulled",
			ref:  "img:2",
			body: `
if [ "$2" = "list" ]; then echo '[{"reference":"other:1"}]'; fi
exit 0`,
			wantPull: true,
		},
		{
			name: "pull failure",
			ref:  "img:2",
			body: `
if [ "$2" = "list" ]; then echo '[]'; exit 0; fi
echo 'registry down' >&2
exit 1`,
			wantErr: "pull image img:2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := installFakeContainer(t, tt.body)
			p := newTestProvider(Config{})

			err := p.ensureImage(context.Background(), tt.ref)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ensureImage err = %v, want contains %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ensureImage: %v", err)
			}
			pulled := fake.hasCall(t, "image pull "+tt.ref)
			if pulled != tt.wantPull {
				t.Errorf("pull call = %v, want %v (calls: %v)", pulled, tt.wantPull, fake.calls(t))
			}
		})
	}
}

func TestFindContainer(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		lookup  string
		wantErr string
		wantID  string
	}{
		{
			name:   "found by configuration id",
			body:   `echo '[{"status":"running","configuration":{"id":"crew-a"}},{"status":"stopped","configuration":{"id":"crew-b"}}]'`,
			lookup: "crew-b",
			wantID: "crew-b",
		},
		{
			name:    "not found",
			body:    `echo '[]'`,
			lookup:  "ghost",
			wantErr: `container "ghost" not found`,
		},
		{
			name:    "cli failure",
			body:    `exit 1`,
			lookup:  "x",
			wantErr: "container list",
		},
		{
			name:    "bad json",
			body:    `echo 'nope'`,
			lookup:  "x",
			wantErr: "parse container list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installFakeContainer(t, tt.body)
			p := newTestProvider(Config{})

			got, err := p.findContainer(context.Background(), tt.lookup)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("findContainer err = %v, want contains %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("findContainer: %v", err)
			}
			if got.Configuration.ID != tt.wantID {
				t.Errorf("found id = %q, want %q", got.Configuration.ID, tt.wantID)
			}
		})
	}
}

func TestInspectContainer(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantErr    string
		wantStatus string
		wantID     string
	}{
		{
			name:       "array result",
			body:       `echo '[{"status":"running","configuration":{"id":"abc"}}]'`,
			wantStatus: "running",
			wantID:     "abc",
		},
		{
			name:       "single object result",
			body:       `echo '{"status":"stopped","configuration":{"id":"xyz"}}'`,
			wantStatus: "stopped",
			wantID:     "xyz",
		},
		{
			name:    "empty array",
			body:    `echo '[]'`,
			wantErr: "empty inspect result",
		},
		{
			name:    "unparseable output",
			body:    `echo 'garbage'`,
			wantErr: "parse inspect output",
		},
		{
			name:    "cli failure",
			body:    `echo 'no such container' >&2; exit 1`,
			wantErr: "inspect cid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installFakeContainer(t, tt.body)
			p := newTestProvider(Config{})

			got, err := p.inspectContainer(context.Background(), "cid")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("inspectContainer err = %v, want contains %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("inspectContainer: %v", err)
			}
			if got.Status != tt.wantStatus || got.Configuration.ID != tt.wantID {
				t.Errorf("got status=%q id=%q, want status=%q id=%q",
					got.Status, got.Configuration.ID, tt.wantStatus, tt.wantID)
			}
		})
	}
}

func TestStopCrewRuntime(t *testing.T) {
	fake := installFakeContainer(t, `exit 0`)
	p := newTestProvider(Config{})

	if err := p.StopCrewRuntime(context.Background(), "abcdef"); err != nil {
		t.Fatalf("StopCrewRuntime: %v", err)
	}
	if !fake.hasCall(t, "stop --time 10 abcdef") {
		t.Errorf("expected 'stop --time 10 abcdef' call, got %v", fake.calls(t))
	}
}

func TestStopCrewRuntimeFailure(t *testing.T) {
	installFakeContainer(t, `echo 'not running' >&2; exit 1`)
	p := newTestProvider(Config{})

	err := p.StopCrewRuntime(context.Background(), "abcdefghijklmnop")
	if err == nil {
		t.Fatal("expected error")
	}
	// Error message uses shortID (first 12 chars) and includes CLI stderr.
	if !strings.Contains(err.Error(), "stop crew runtime abcdefghijkl") {
		t.Errorf("error = %v, want short ID abcdefghijkl", err)
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("error = %v, want CLI stderr included", err)
	}
}

func TestRemoveCrewRuntime(t *testing.T) {
	fake := installFakeContainer(t, `exit 0`)
	p := newTestProvider(Config{})

	if err := p.RemoveCrewRuntime(context.Background(), "cid-1"); err != nil {
		t.Fatalf("RemoveCrewRuntime: %v", err)
	}
	if !fake.hasCall(t, "delete --force cid-1") {
		t.Errorf("expected 'delete --force cid-1' call, got %v", fake.calls(t))
	}
}

func TestRemoveCrewRuntimeFailure(t *testing.T) {
	installFakeContainer(t, `exit 1`)
	p := newTestProvider(Config{})

	err := p.RemoveCrewRuntime(context.Background(), "cid-1")
	if err == nil || !strings.Contains(err.Error(), "remove crew runtime cid-1") {
		t.Fatalf("err = %v, want remove crew runtime error", err)
	}
}

func TestContainerStatusStates(t *testing.T) {
	tests := []struct {
		cliStatus string
		wantState string
	}{
		{"running", "running"},
		{"Running", "running"},
		{"created", "creating"},
		{"starting", "creating"},
		{"stopped", "stopped"},
		{"exited", "stopped"},
		{"weird", "error"},
	}

	for _, tt := range tests {
		t.Run(tt.cliStatus, func(t *testing.T) {
			installFakeContainer(t,
				`echo '[{"status":"`+tt.cliStatus+`","configuration":{"id":"cid-1"}}]'`)
			p := newTestProvider(Config{})

			st, err := p.ContainerStatus(context.Background(), "cid-1")
			if err != nil {
				t.Fatalf("ContainerStatus: %v", err)
			}
			if st.State != tt.wantState {
				t.Errorf("state = %q, want %q", st.State, tt.wantState)
			}
			if st.ID != "cid-1" {
				t.Errorf("id = %q, want cid-1", st.ID)
			}
		})
	}
}

func TestContainerStatusInspectError(t *testing.T) {
	installFakeContainer(t, `exit 1`)
	p := newTestProvider(Config{})

	_, err := p.ContainerStatus(context.Background(), "cid-1")
	if err == nil || !strings.Contains(err.Error(), "container inspect") {
		t.Fatalf("err = %v, want container inspect error", err)
	}
}

func TestHostAddressReturnsDetectedIP(t *testing.T) {
	p := newTestProvider(Config{})
	p.hostIP = "192.168.1.50"
	if got := p.HostAddress(); got != "192.168.1.50" {
		t.Errorf("HostAddress = %q, want 192.168.1.50", got)
	}
}

func TestRunCLISuccessAndFailure(t *testing.T) {
	installFakeContainer(t, `
if [ "$1" = "ok" ]; then echo 'fine'; exit 0; fi
echo 'kaboom' >&2
exit 7`)

	out, err := runCLI(context.Background(), "ok")
	if err != nil {
		t.Fatalf("runCLI ok: %v", err)
	}
	if strings.TrimSpace(string(out)) != "fine" {
		t.Errorf("stdout = %q, want fine", out)
	}

	_, err = runCLI(context.Background(), "bad", "arg")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "container bad arg") || !strings.Contains(err.Error(), "kaboom") {
		t.Errorf("error = %v, want command args and stderr", err)
	}
}

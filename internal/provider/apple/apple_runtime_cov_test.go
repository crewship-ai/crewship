package apple

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// crewBody is a fake-CLI body for the full create flow: no existing
// container, image already present, create prints an ID, inspect reports
// the real ID plus a gateway.
const crewBody = `
case "$1" in
  network)
    if [ "$2" = "list" ]; then echo '[{"name":"mynet"}]'; fi
    exit 0;;
  list) echo '[]'; exit 0;;
  image)
    if [ "$2" = "list" ]; then echo '[{"reference":"img:1"}]'; fi
    exit 0;;
  create) echo 'cid-raw'; exit 0;;
  start) exit 0;;
  inspect) echo '[{"status":"running","configuration":{"id":"real-id"},"networks":[{"ipv4Gateway":"192.168.67.1"}]}]'; exit 0;;
esac
exit 0`

func TestEnsureCrewRuntimeUnsafeIDAndSlug(t *testing.T) {
	installFakeContainer(t, `exit 0`)
	p := newTestProvider(Config{OutputBasePath: t.TempDir()})

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "../etc", Slug: "eng"})
	if err == nil || !strings.Contains(err.Error(), "crew id not safe for path") {
		t.Fatalf("err = %v, want crew id not safe", err)
	}

	_, err = p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "a/b"})
	if err == nil || !strings.Contains(err.Error(), "crew slug not safe for path") {
		t.Fatalf("err = %v, want crew slug not safe", err)
	}
}

func TestEnsureCrewRuntimeNetworkError(t *testing.T) {
	installFakeContainer(t, `
if [ "$1 $2" = "network list" ]; then echo '[]'; exit 0; fi
if [ "$1 $2" = "network create" ]; then echo 'boom' >&2; exit 1; fi
exit 0`)
	p := newTestProvider(Config{Network: "mynet", OutputBasePath: t.TempDir()})

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"})
	if err == nil || !strings.Contains(err.Error(), "ensure network") {
		t.Fatalf("err = %v, want ensure network error", err)
	}
}

func TestEnsureCrewRuntimeExistingRunning(t *testing.T) {
	fake := installFakeContainer(t, `
case "$1" in
  list) echo '[{"status":"running","configuration":{"id":"crewship-team-eng"}}]'; exit 0;;
esac
exit 0`)
	p := newTestProvider(Config{OutputBasePath: t.TempDir()})

	id, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id != "crewship-team-eng" {
		t.Errorf("id = %q, want crewship-team-eng", id)
	}
	if fake.hasCall(t, "create") || fake.hasCall(t, "start") {
		t.Errorf("running container should be returned as-is, calls: %v", fake.calls(t))
	}
}

func TestEnsureCrewRuntimeStartsStoppedContainer(t *testing.T) {
	base := t.TempDir()
	// All bind-mount dirs exist -> the stopped container is started, not recreated.
	for _, d := range []string{
		filepath.Join(base, "workspaces", "crew1"),
		filepath.Join(base, "crew1"),
		filepath.Join(base, "crews", "crew1"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// In Apple Containers configuration.id IS the container name.
	fake := installFakeContainer(t, `
case "$1" in
  list) echo '[{"status":"stopped","configuration":{"id":"crewship-team-eng"}}]'; exit 0;;
  start) exit 0;;
esac
exit 0`)
	p := newTestProvider(Config{OutputBasePath: base})

	id, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id != "crewship-team-eng" {
		t.Errorf("id = %q, want crewship-team-eng", id)
	}
	if !fake.hasCall(t, "start crewship-team-eng") {
		t.Errorf("expected 'start crewship-team-eng' call, got %v", fake.calls(t))
	}
	if fake.hasCall(t, "create") {
		t.Errorf("should not recreate when binds exist, calls: %v", fake.calls(t))
	}
}

func TestEnsureCrewRuntimeStartStoppedFails(t *testing.T) {
	base := t.TempDir()
	for _, d := range []string{
		filepath.Join(base, "workspaces", "crew1"),
		filepath.Join(base, "crew1"),
		filepath.Join(base, "crews", "crew1"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	installFakeContainer(t, `
case "$1" in
  list) echo '[{"status":"stopped","configuration":{"id":"crewship-team-eng"}}]'; exit 0;;
  start) exit 1;;
esac
exit 0`)
	p := newTestProvider(Config{OutputBasePath: base})

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"})
	if err == nil || !strings.Contains(err.Error(), "start existing container") {
		t.Fatalf("err = %v, want start existing container error", err)
	}
}

func TestEnsureCrewRuntimeBindsMissingRecreates(t *testing.T) {
	// Bind-mount dirs do NOT exist -> stopped container is rm'd and recreated.
	fake := installFakeContainer(t, `
case "$1" in
  list) echo '[{"status":"stopped","configuration":{"id":"crewship-team-eng"}}]'; exit 0;;
  rm) exit 0;;
  image)
    if [ "$2" = "list" ]; then echo '[{"reference":"img:1"}]'; fi
    exit 0;;
  create) echo 'new-cid'; exit 0;;
  start) exit 0;;
  inspect) echo '[{"status":"running","configuration":{"id":"new-cid"},"networks":[]}]'; exit 0;;
esac
exit 0`)
	p := newTestProvider(Config{RuntimeImage: "img:1", OutputBasePath: t.TempDir()})

	id, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id != "new-cid" {
		t.Errorf("id = %q, want new-cid", id)
	}
	if !fake.hasCall(t, "rm crewship-team-eng") {
		t.Errorf("expected 'rm crewship-team-eng' call, got %v", fake.calls(t))
	}
	if !fake.hasCall(t, "create") {
		t.Errorf("expected create call, got %v", fake.calls(t))
	}
}

func TestEnsureCrewRuntimeCreatesNewWithDefaults(t *testing.T) {
	base := t.TempDir()
	fake := installFakeContainer(t, crewBody)
	p := newTestProvider(Config{RuntimeImage: "img:1", Network: "mynet", OutputBasePath: base})

	id, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id != "real-id" {
		t.Errorf("id = %q, want real-id (from inspect)", id)
	}

	// Host IP discovered from gateway when previously unknown.
	if got := p.HostAddress(); got != "192.168.67.1" {
		t.Errorf("HostAddress = %q, want 192.168.67.1 from inspect gateway", got)
	}

	var createCall string
	for _, c := range fake.calls(t) {
		if strings.HasPrefix(c, "create ") {
			createCall = c
		}
	}
	if createCall == "" {
		t.Fatalf("no create call recorded: %v", fake.calls(t))
	}
	for _, want := range []string{
		"--name crewship-team-eng",
		"--cpus 1",
		"--memory 512M",
		"--read-only",
		"--env CREWSHIP_CREW_ID=crew1",
		"-v " + filepath.Join(base, "workspaces", "crew1") + ":/workspace",
		"-v " + filepath.Join(base, "crew1") + ":/output",
		"-v " + filepath.Join(base, "crews", "crew1") + ":/crew",
		"--tmpfs /tmp",
		"--tmpfs /home/agent",
		"--network mynet",
		"--user 1001:1001",
		"img:1 sleep infinity",
	} {
		if !strings.Contains(createCall, want) {
			t.Errorf("create call missing %q: %s", want, createCall)
		}
	}

	// Host-side bind dirs were created.
	for _, d := range []string{
		filepath.Join(base, "crew1"),
		filepath.Join(base, "workspaces", "crew1"),
		filepath.Join(base, "crews", "crew1", "shared"),
		filepath.Join(base, "crews", "crew1", "agents"),
	} {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			t.Errorf("expected dir %s to exist: %v", d, err)
		}
	}
}

func TestEnsureCrewRuntimeCustomResources(t *testing.T) {
	fake := installFakeContainer(t, crewBody)
	p := newTestProvider(Config{RuntimeImage: "img:1", OutputBasePath: t.TempDir()})
	p.hostIP = "10.0.0.1" // already known -> gateway must not overwrite it

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID: "crew2", Slug: "ops", MemoryMB: 2048, CPUs: 2.7,
	})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	var createCall string
	for _, c := range fake.calls(t) {
		if strings.HasPrefix(c, "create ") {
			createCall = c
		}
	}
	if !strings.Contains(createCall, "--cpus 2") {
		t.Errorf("create call missing truncated --cpus 2: %s", createCall)
	}
	if !strings.Contains(createCall, "--memory 2048M") {
		t.Errorf("create call missing --memory 2048M: %s", createCall)
	}
	if strings.Contains(createCall, "--network") {
		t.Errorf("no network configured, create call must not pass --network: %s", createCall)
	}
	if got := p.HostAddress(); got != "10.0.0.1" {
		t.Errorf("HostAddress = %q, want pre-set 10.0.0.1 preserved", got)
	}
}

func TestEnsureCrewRuntimeImageError(t *testing.T) {
	installFakeContainer(t, `
case "$1" in
  list) echo '[]'; exit 0;;
  image) exit 1;;
esac
exit 0`)
	p := newTestProvider(Config{RuntimeImage: "img:1", OutputBasePath: t.TempDir()})

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"})
	if err == nil || !strings.Contains(err.Error(), "ensure image") {
		t.Fatalf("err = %v, want ensure image error", err)
	}
}

func TestEnsureCrewRuntimeDirCreationErrors(t *testing.T) {
	okBody := `
case "$1" in
  list) echo '[]'; exit 0;;
  image)
    if [ "$2" = "list" ]; then echo '[{"reference":"img:1"}]'; fi
    exit 0;;
esac
exit 0`

	t.Run("output dir", func(t *testing.T) {
		installFakeContainer(t, okBody)
		base := filepath.Join(t.TempDir(), "blocker")
		if err := os.WriteFile(base, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		p := newTestProvider(Config{RuntimeImage: "img:1", OutputBasePath: base})
		_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"})
		if err == nil || !strings.Contains(err.Error(), "create output dir") {
			t.Fatalf("err = %v, want create output dir error", err)
		}
	})

	t.Run("workspace dir", func(t *testing.T) {
		installFakeContainer(t, okBody)
		base := t.TempDir()
		if err := os.WriteFile(filepath.Join(base, "workspaces"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		p := newTestProvider(Config{RuntimeImage: "img:1", OutputBasePath: base})
		_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"})
		if err == nil || !strings.Contains(err.Error(), "create workspace dir") {
			t.Fatalf("err = %v, want create workspace dir error", err)
		}
	})

	t.Run("crew dir", func(t *testing.T) {
		installFakeContainer(t, okBody)
		base := t.TempDir()
		if err := os.WriteFile(filepath.Join(base, "crews"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		p := newTestProvider(Config{RuntimeImage: "img:1", OutputBasePath: base})
		_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"})
		if err == nil || !strings.Contains(err.Error(), "create crew dir") {
			t.Fatalf("err = %v, want create crew dir error", err)
		}
	})
}

func TestEnsureCrewRuntimeCreateRaceRecovered(t *testing.T) {
	// First `list` shows nothing; `create` fails with "already exists" and
	// drops a marker so the second `list` reveals the racing container.
	fake := installFakeContainer(t, `
case "$1" in
  list)
    if [ -f "$DIR/created" ]; then
      echo '[{"status":"stopped","configuration":{"id":"crewship-team-eng"}}]'
    else
      echo '[]'
    fi
    exit 0;;
  image)
    if [ "$2" = "list" ]; then echo '[{"reference":"img:1"}]'; fi
    exit 0;;
  create)
    : > "$DIR/created"
    echo 'container already exists' >&2
    exit 1;;
  start) exit 0;;
esac
exit 0`)
	p := newTestProvider(Config{RuntimeImage: "img:1", OutputBasePath: t.TempDir()})

	id, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id != "crewship-team-eng" {
		t.Errorf("id = %q, want crewship-team-eng", id)
	}
	if !fake.hasCall(t, "start crewship-team-eng") {
		t.Errorf("expected stopped raced container to be started, calls: %v", fake.calls(t))
	}
}

func TestEnsureCrewRuntimeCreateRaceStartFails(t *testing.T) {
	installFakeContainer(t, `
case "$1" in
  list)
    if [ -f "$DIR/created" ]; then
      echo '[{"status":"stopped","configuration":{"id":"crewship-team-eng"}}]'
    else
      echo '[]'
    fi
    exit 0;;
  image)
    if [ "$2" = "list" ]; then echo '[{"reference":"img:1"}]'; fi
    exit 0;;
  create)
    : > "$DIR/created"
    echo 'container already exists' >&2
    exit 1;;
  start) exit 1;;
esac
exit 0`)
	p := newTestProvider(Config{RuntimeImage: "img:1", OutputBasePath: t.TempDir()})

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"})
	if err == nil || !strings.Contains(err.Error(), "start existing container after race") {
		t.Fatalf("err = %v, want race start error", err)
	}
}

func TestEnsureCrewRuntimeCreateRaceFindFails(t *testing.T) {
	// "already exists" but the container never shows up in list -> original
	// create error is surfaced.
	installFakeContainer(t, `
case "$1" in
  list) echo '[]'; exit 0;;
  image)
    if [ "$2" = "list" ]; then echo '[{"reference":"img:1"}]'; fi
    exit 0;;
  create)
    echo 'container already exists' >&2
    exit 1;;
esac
exit 0`)
	p := newTestProvider(Config{RuntimeImage: "img:1", OutputBasePath: t.TempDir()})

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"})
	if err == nil || !strings.Contains(err.Error(), "container create") {
		t.Fatalf("err = %v, want container create error", err)
	}
}

func TestEnsureCrewRuntimeCreateFails(t *testing.T) {
	installFakeContainer(t, `
case "$1" in
  list) echo '[]'; exit 0;;
  image)
    if [ "$2" = "list" ]; then echo '[{"reference":"img:1"}]'; fi
    exit 0;;
  create) echo 'out-of-disk' >&2; exit 1;;
esac
exit 0`)
	p := newTestProvider(Config{RuntimeImage: "img:1", OutputBasePath: t.TempDir()})

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"})
	if err == nil || !strings.Contains(err.Error(), "container create") {
		t.Fatalf("err = %v, want container create error", err)
	}
	if !strings.Contains(err.Error(), "out-of-disk") {
		t.Errorf("err = %v, want CLI stderr included", err)
	}
}

func TestEnsureCrewRuntimeStartFails(t *testing.T) {
	installFakeContainer(t, `
case "$1" in
  list) echo '[]'; exit 0;;
  image)
    if [ "$2" = "list" ]; then echo '[{"reference":"img:1"}]'; fi
    exit 0;;
  create) echo 'cid-raw'; exit 0;;
  start) exit 1;;
esac
exit 0`)
	p := newTestProvider(Config{RuntimeImage: "img:1", OutputBasePath: t.TempDir()})

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"})
	if err == nil || !strings.Contains(err.Error(), "container start") {
		t.Fatalf("err = %v, want container start error", err)
	}
}

func TestEnsureCrewRuntimeEmptyCreateOutputAndInspectFailure(t *testing.T) {
	// create prints nothing -> falls back to the container name; inspect
	// failing afterwards is non-fatal.
	installFakeContainer(t, `
case "$1" in
  list) echo '[]'; exit 0;;
  image)
    if [ "$2" = "list" ]; then echo '[{"reference":"img:1"}]'; fi
    exit 0;;
  create) exit 0;;
  start) exit 0;;
  inspect) exit 1;;
esac
exit 0`)
	p := newTestProvider(Config{RuntimeImage: "img:1", OutputBasePath: t.TempDir()})

	id, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id != "crewship-team-eng" {
		t.Errorf("id = %q, want fallback container name crewship-team-eng", id)
	}
}

func TestRemoveCrewVolumes(t *testing.T) {
	base := t.TempDir()
	p := newTestProvider(Config{OutputBasePath: base})

	if err := p.RemoveCrewVolumes(context.Background(), "../evil"); err == nil ||
		!strings.Contains(err.Error(), "crew slug not safe for path") {
		t.Fatalf("err = %v, want slug not safe", err)
	}

	home := filepath.Join(base, "homes", "eng")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.RemoveCrewVolumes(context.Background(), "eng"); err != nil {
		t.Fatalf("RemoveCrewVolumes: %v", err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("expected %s removed, stat err = %v", home, err)
	}

	// Removing a non-existent home is a no-op.
	if err := p.RemoveCrewVolumes(context.Background(), "ghost"); err != nil {
		t.Fatalf("RemoveCrewVolumes nonexistent: %v", err)
	}
}

func TestCopyToContainerUnsupported(t *testing.T) {
	p := newTestProvider(Config{})
	err := p.CopyToContainer(context.Background(), "cid", "/dst", strings.NewReader("x"))
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("err = %v, want not supported", err)
	}
}

func TestCloseStopsGCExecs(t *testing.T) {
	p := newTestProvider(Config{})

	returned := make(chan struct{})
	go func() {
		p.gcExecs()
		close(returned)
	}()

	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("gcExecs did not return after Close")
	}
}

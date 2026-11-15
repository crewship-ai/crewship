package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/diskusage"
	dockerprovider "github.com/crewship-ai/crewship/internal/provider/docker"
)

const testGiB = uint64(1) << 30

func TestBuildStorageCheck(t *testing.T) {
	errProbe := errors.New("statfs unavailable")
	tests := []struct {
		name        string
		dataDir     string
		dockerRoot  string
		dockerKnown bool
		floor       int64
		free        map[string]uint64
		probeErr    map[string]error
		wantErr     string // substring; "" = build allowed
	}{
		{name: "plenty of room everywhere", dataDir: "/data", dockerRoot: "/var/lib/docker", dockerKnown: true, floor: 4 << 30,
			free: map[string]uint64{"/data": 50 * testGiB, "/var/lib/docker": 50 * testGiB}},
		{name: "data volume below its floor", dataDir: "/data", dockerRoot: "/var/lib/docker", dockerKnown: true, floor: 4 << 30,
			free: map[string]uint64{"/data": testGiB / 2, "/var/lib/docker": 50 * testGiB}, wantErr: "0.5 GiB free on the Crewship data volume (/data)"},
		{name: "data volume exactly at its floor", dataDir: "/data", floor: 4 << 30,
			free: map[string]uint64{"/data": testGiB}},
		{name: "docker storage below its floor", dataDir: "/data", dockerRoot: "/var/lib/docker", dockerKnown: true, floor: 4 << 30,
			free: map[string]uint64{"/data": 50 * testGiB, "/var/lib/docker": 3 * testGiB}, wantErr: "3.0 GiB free on Docker's storage (/var/lib/docker), a build needs at least 4.0 GiB"},
		{name: "docker storage not measurable is unknown, not pressure", dataDir: "/data", dockerRoot: "/var/lib/docker", dockerKnown: false, floor: 4 << 30,
			free: map[string]uint64{"/data": 50 * testGiB, "/var/lib/docker": 0}},
		{name: "docker floor disabled", dataDir: "/data", dockerRoot: "/var/lib/docker", dockerKnown: true, floor: 0,
			free: map[string]uint64{"/data": 50 * testGiB, "/var/lib/docker": 0}},
		{name: "data volume floor stays when the docker floor is disabled", dataDir: "/data", floor: 0,
			free: map[string]uint64{"/data": 0}, wantErr: "Crewship data volume"},
		{name: "in-memory database has no data volume", dataDir: "", dockerRoot: "/var/lib/docker", dockerKnown: true, floor: 4 << 30,
			free: map[string]uint64{"/var/lib/docker": 50 * testGiB}},
		{name: "a filesystem larger than int64 is not pressure", dataDir: "/data", dockerRoot: "/var/lib/docker", dockerKnown: true, floor: 4 << 30,
			free: map[string]uint64{"/data": 1 << 63, "/var/lib/docker": 1<<64 - 1}},
		{name: "a failing probe is unknown, not pressure", dataDir: "/data", dockerRoot: "/var/lib/docker", dockerKnown: true, floor: 4 << 30,
			probeErr: map[string]error{"/data": errProbe, "/var/lib/docker": errProbe}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := buildStorageCheck{
				dataDir: func(context.Context) string { return tt.dataDir },
				dockerRoot: func(context.Context) (string, error) {
					if !tt.dockerKnown {
						return "", errors.New("not measurable")
					}
					return tt.dockerRoot, nil
				},
				usage: func(path string) (diskusage.Stats, error) {
					if err := tt.probeErr[path]; err != nil {
						return diskusage.Stats{}, err
					}
					return diskusage.Stats{Path: path, FreeBytes: tt.free[path]}, nil
				},
				dockerMinFree: tt.floor,
			}
			err := check.check(context.Background())
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("build refused: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrBuildStorage) || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want ErrBuildStorage containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestBuildMinFreeFromEnv(t *testing.T) {
	tests := []struct {
		name, value string
		set         bool
		want        int64
		invalid     bool
	}{
		{name: "unset keeps the default", want: defaultBuildMinFreeBytes},
		{name: "blank keeps the default", set: true, value: "  ", want: defaultBuildMinFreeBytes},
		{name: "explicit floor", set: true, value: "10", want: 10 << 30},
		{name: "zero disables", set: true, value: "0", want: 0},
		{name: "negative is unusable", set: true, value: "-1", want: defaultBuildMinFreeBytes, invalid: true},
		{name: "absurdly large is unusable", set: true, value: "1048577", want: defaultBuildMinFreeBytes, invalid: true},
		{name: "text is unusable", set: true, value: "lots", want: defaultBuildMinFreeBytes, invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv(buildMinFreeEnv, tt.value)
			}
			got, invalid := buildMinFreeFromEnv()
			if got != tt.want || invalid != tt.invalid {
				t.Errorf("buildMinFreeFromEnv() = %d, %t; want %d, %t", got, invalid, tt.want, tt.invalid)
			}
		})
	}
}

func TestMeasurableDockerRoot(t *testing.T) {
	present := func(string) error { return nil }
	absent := func(string) error { return errors.New("no such file or directory") }
	storage := func(root, operatingSystem string, err error) dockerStorageFunc {
		return func(context.Context) (dockerprovider.DaemonStorageInfo, error) {
			return dockerprovider.DaemonStorageInfo{RootDir: root, OperatingSystem: operatingSystem, Driver: "overlay2"}, err
		}
	}
	linux := storage("/var/lib/docker", "Ubuntu 24.04 LTS", nil)
	const socket = "unix:///var/run/docker.sock"
	tests := []struct {
		name    string
		host    string
		storage dockerStorageFunc
		stat    func(string) error
		want    bool
	}{
		{"local engine with visible root", socket, linux, present, true},
		{"no docker client", socket, nil, present, false},
		{"remote daemon over TCP", "tcp://10.0.0.5:2376", linux, present, false},
		{"Docker Desktop keeps its data in a VM", socket, storage("/var/lib/docker", "Docker Desktop", nil), present, false},
		{"root not visible to this process (server in a container, VM-backed runtime)", socket, linux, absent, false},
		{"daemon does not answer", socket, storage("", "", errors.New("unavailable")), present, false},
		{"daemon reports no root", socket, storage("", "Ubuntu 24.04 LTS", nil), present, false},
		{"daemon reports a relative root", socket, storage("docker", "Ubuntu 24.04 LTS", nil), present, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, err := measurableDockerRoot(context.Background(), tt.host, tt.storage, tt.stat)
			ok := err == nil
			if ok != tt.want || (ok && root != "/var/lib/docker") {
				t.Errorf("measurableDockerRoot = %q, %t; want measurable=%t", root, ok, tt.want)
			}
		})
	}
}

func TestBuildFailureAdvice(t *testing.T) {
	storage := "Free up disk space on the server and send your message again."
	config := "Fix the devcontainer configuration and send your message again."
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"storage refusal", fmt.Errorf("provision: %w: 3.0 GiB free", ErrBuildStorage), storage},
		{"storage refusal rebuilt from a job's stored text", errors.New("build failed: provision: " + ErrBuildStorage.Error() + ": 3.0 GiB free"), storage},
		{"a build that ran out of space part-way", errors.New("build failed: committing container: write /var/lib/docker/overlay2/x: No space left on device"), storage},
		{"an ordinary build failure", errors.New("build failed: docker build failed for crewship-feat:abc: exit status 1"), config},
		{"no error", nil, config},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildFailureAdvice(tt.err); got != tt.want {
				t.Errorf("buildFailureAdvice = %q, want %q", got, tt.want)
			}
		})
	}
}

// The data-volume probe reads the main database's directory from SQLite.
func TestEnableBuildStoragePreflightFindsTheDatabaseDirectory(t *testing.T) {
	db := setupTestDB(t)
	var file string
	if err := db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&file); err != nil {
		t.Fatalf("pragma_database_list: %v", err)
	}
	h := &ProvisioningHandler{db: db, logger: newTestLogger(), provisioner: devcontainer.NewProvisioner(nil, nil, nil, nil)}
	h.EnableBuildStoragePreflight()
	if !h.provisioner.HasBuildPreflight() {
		t.Fatal("no build preflight installed")
	}
	// An in-memory test database has no file: the probe must answer "no
	// data volume" rather than measure the working directory.
	if file == "" {
		return
	}
	if _, err := os.Stat(filepath.Dir(file)); err != nil {
		t.Errorf("database directory %q is not a real directory: %v", filepath.Dir(file), err)
	}
}

func TestBuildStorageUnknownIsReportedAndDoesNotProbeWrongFilesystem(t *testing.T) {
	t.Parallel()
	reported := map[string]error{}
	check := buildStorageCheck{
		dataDir:       func(context.Context) string { return "/data" },
		dockerRoot:    func(context.Context) (string, error) { return "", errors.New("containerd store path unknown") },
		dockerMinFree: 4 << 30,
		usage: func(p string) (diskusage.Stats, error) {
			if p != "/data" {
				t.Fatalf("unexpected filesystem probe: %s", p)
			}
			return diskusage.Stats{}, errors.New("statfs denied")
		},
		unknown: func(volume string, err error) { reported[volume] = err },
	}
	if err := check.check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(reported) != 2 || reported["Crewship data"] == nil || reported["Docker image"] == nil {
		t.Fatalf("unknown measurements were not reported: %v", reported)
	}
}

func TestMeasurableDockerRootRejectsSplitAndUnrecognisedBackends(t *testing.T) {
	t.Parallel()
	for _, info := range []dockerprovider.DaemonStorageInfo{
		{Driver: "overlayfs"},
		{Driver: "overlay2", DriverStatus: [][2]string{{"driver-type", "io.containerd.snapshotter.v1"}}},
		{Driver: "future-snapshotter"},
		{Driver: "devicemapper"},
		{Driver: "zfs"},
		{Driver: "btrfs"},
		{},
	} {
		info.RootDir = "/var/lib/docker"
		root, err := measurableDockerRoot(t.Context(), "unix:///var/run/docker.sock",
			func(context.Context) (dockerprovider.DaemonStorageInfo, error) { return info, nil },
			func(string) error { t.Fatal("must not stat a guessed image-store filesystem"); return nil })
		if err == nil || root != "" {
			t.Fatalf("backend %+v measured as %q: %v", info, root, err)
		}
	}
}

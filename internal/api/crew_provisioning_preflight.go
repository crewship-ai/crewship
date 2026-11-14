package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/diskusage"
)

// Storage check before a crew image build (#5391).
//
// A build writes several gigabytes, and a disk that fills mid-build does not
// only fail the build: the database on the same volume starts failing writes.
// The check runs once per build, after the cache lookup, and refuses the build
// with a message an operator can act on. It never deletes anything to make
// room, and where a volume cannot be measured the answer is "unknown" and the
// build proceeds — a guessed number would refuse builds for no reason.

const (
	// buildMinFreeEnv overrides the floor on Docker's storage, in whole GiB.
	// 0 turns that check off. The data-volume floor is not configurable.
	buildMinFreeEnv = "CREWSHIP_BUILD_MIN_FREE_GB"

	// defaultBuildMinFreeBytes is the floor on Docker's storage. A default
	// crew build pulls a ~1.2 GB base, writes ~0.7 GB of feature layers and a
	// ~1 GB commit layer that briefly exists twice (the temp container's
	// writable layer and the committed copy), plus BuildKit's own records.
	defaultBuildMinFreeBytes = int64(4) << 30

	// buildDataVolumeMinFreeBytes is the floor on the volume holding the main
	// database, the same headroom the scheduler keeps before accepting work
	// (work.DefaultMinFreeDiskBytes).
	buildDataVolumeMinFreeBytes = int64(1) << 30
)

// ErrBuildStorage refuses a crew image build for lack of free disk space.
var ErrBuildStorage = errors.New("not enough free disk space to build a crew image")

// buildStorageCheck is the check with its probes injectable. A probe that
// cannot answer is "unknown", never pressure.
type buildStorageCheck struct {
	// dataDir returns the directory of the main database file, or "" when
	// the database has no file.
	dataDir func(ctx context.Context) string
	// dockerRoot returns Docker's storage directory and whether this process
	// can measure it.
	dockerRoot func(ctx context.Context) (string, bool)
	usage      func(path string) (diskusage.Stats, error)
	// dockerMinFree is the floor on Docker's storage; 0 disables that check.
	dockerMinFree int64
}

func (c buildStorageCheck) check(ctx context.Context) error {
	if c.usage == nil {
		return nil
	}
	if c.dataDir != nil {
		if dir := c.dataDir(ctx); dir != "" {
			if st, err := c.usage(dir); err == nil && int64(st.FreeBytes) < buildDataVolumeMinFreeBytes {
				return fmt.Errorf("%w: %s free on the Crewship data volume (%s), at least %s must stay free for the database; free up space and build again",
					ErrBuildStorage, gib(int64(st.FreeBytes)), dir, gib(buildDataVolumeMinFreeBytes))
			}
		}
	}
	if c.dockerMinFree > 0 && c.dockerRoot != nil {
		if root, ok := c.dockerRoot(ctx); ok && root != "" {
			if st, err := c.usage(root); err == nil && int64(st.FreeBytes) < c.dockerMinFree {
				return fmt.Errorf("%w: %s free on Docker's storage (%s), a build needs at least %s (%s); free up space and build again",
					ErrBuildStorage, gib(int64(st.FreeBytes)), root, gib(c.dockerMinFree), buildMinFreeEnv)
			}
		}
	}
	return nil
}

func gib(n int64) string {
	return strconv.FormatFloat(float64(n)/float64(int64(1)<<30), 'f', 1, 64) + " GiB"
}

// buildMinFreeFromEnv reads the Docker storage floor. Unset or unparseable
// keeps the default; the second result reports an unusable value.
func buildMinFreeFromEnv() (floor int64, invalid bool) {
	raw, set := os.LookupEnv(buildMinFreeEnv)
	if !set || strings.TrimSpace(raw) == "" {
		return defaultBuildMinFreeBytes, false
	}
	gb, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || gb < 0 || gb > 1<<20 {
		return defaultBuildMinFreeBytes, true
	}
	return int64(gb) << 30, false
}

// dockerInfoClient is the part of the Docker client the storage probe needs.
type dockerInfoClient interface {
	DaemonHost() string
	Info(ctx context.Context, options client.InfoOptions) (client.SystemInfoResult, error)
}

// measurableDockerRoot returns Docker's root directory when free space
// measured by this process is the daemon's free space: the daemon is reached
// over a local Unix socket, is not Docker Desktop (its storage is a VM disk),
// and the directory exists here. A server running in a container without the
// directory mounted, a remote daemon and a VM-backed daemon all answer false.
func measurableDockerRoot(ctx context.Context, docker dockerInfoClient, stat func(string) error) (string, bool) {
	if docker == nil || !strings.HasPrefix(docker.DaemonHost(), "unix://") {
		return "", false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	info, err := docker.Info(ctx, client.InfoOptions{})
	if err != nil {
		return "", false
	}
	root := info.Info.DockerRootDir
	if root == "" || !filepath.IsAbs(root) || strings.Contains(info.Info.OperatingSystem, "Docker Desktop") {
		return "", false
	}
	if stat(root) != nil {
		return "", false
	}
	return root, true
}

// EnableBuildStoragePreflight makes the provisioner check free disk space
// before every build. Called once while routes are wired. Safe with no
// provisioner.
func (h *ProvisioningHandler) EnableBuildStoragePreflight() {
	if h == nil || h.provisioner == nil {
		return
	}
	floor, invalid := buildMinFreeFromEnv()
	if invalid {
		h.logger.Warn("build storage check: unusable setting, keeping the default",
			"setting", buildMinFreeEnv, "default_gib", defaultBuildMinFreeBytes>>30)
	}
	check := buildStorageCheck{
		dataDir: func(ctx context.Context) string {
			if h.db == nil {
				return ""
			}
			var file string
			if err := h.db.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&file); err != nil || file == "" {
				return ""
			}
			return filepath.Dir(file)
		},
		dockerRoot: func(ctx context.Context) (string, bool) {
			if h.docker == nil {
				return "", false
			}
			return measurableDockerRoot(ctx, h.docker, func(p string) error { _, err := os.Stat(p); return err })
		},
		usage:         diskusage.Usage,
		dockerMinFree: floor,
	}
	h.provisioner.SetBuildPreflight(check.check)
}

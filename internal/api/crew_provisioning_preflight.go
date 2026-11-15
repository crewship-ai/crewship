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

	"github.com/crewship-ai/crewship/internal/diskusage"
	dockerprovider "github.com/crewship-ai/crewship/internal/provider/docker"
)

// Storage check before a crew image build (#5391).
//
// A build writes several gigabytes, and a disk that fills mid-build does not
// only fail the build: the database on the same volume starts failing writes.
// The check runs once per build, after the cache lookup, and refuses the build
// with a message an operator can act on. On the commit path that is before
// anything is pulled or written; on the build-only path the feature archives
// are already in the feature cache by then (they are needed to tell a cache
// hit), and the check precedes the image build. It never deletes anything to make
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
	dockerRoot func(ctx context.Context) (string, error)
	usage      func(path string) (diskusage.Stats, error)
	// dockerMinFree is the floor on Docker's storage; 0 disables that check.
	dockerMinFree int64
	unknown       func(volume string, err error)
}

func (c buildStorageCheck) check(ctx context.Context) error {
	report := func(volume string, err error) {
		if c.unknown != nil {
			c.unknown(volume, err)
		}
	}
	if c.usage == nil {
		report("all", errors.New("filesystem usage probe unavailable"))
		return nil
	}
	if c.dataDir != nil {
		if dir := c.dataDir(ctx); dir != "" {
			st, err := c.usage(dir)
			if err != nil {
				report("Crewship data", err)
			} else if st.FreeBytes < uint64(buildDataVolumeMinFreeBytes) {
				return fmt.Errorf("%w: %s free on the Crewship data volume (%s), at least %s must stay free for the database; free up space and build again",
					ErrBuildStorage, gib(st.FreeBytes), dir, gib(uint64(buildDataVolumeMinFreeBytes)))
			}
		}
	}
	if c.dockerMinFree > 0 && c.dockerRoot != nil {
		root, err := c.dockerRoot(ctx)
		if err != nil {
			report("Docker image", err)
		} else if root == "" {
			report("Docker image", errors.New("image storage path unavailable"))
		} else if st, err := c.usage(root); err != nil {
			report("Docker image", err)
		} else if st.FreeBytes < uint64(c.dockerMinFree) {
			return fmt.Errorf("%w: %s free on Docker's storage (%s), a build needs at least %s (%s); free up space and build again",
				ErrBuildStorage, gib(st.FreeBytes), root, gib(uint64(c.dockerMinFree)), buildMinFreeEnv)
		}
	}
	return nil
}

// buildFailureAdvice closes the chat message for a failed build with what the
// user can do about it. A build refused for lack of disk space is not a
// configuration mistake, and neither is one that ran out of space part-way
// (the kernel's ENOSPC text, as the daemon and BuildKit relay it). The error
// may have been rebuilt from a job's stored text, so texts are matched too.
func buildFailureAdvice(err error) string {
	if err != nil && (errors.Is(err, ErrBuildStorage) ||
		strings.Contains(err.Error(), ErrBuildStorage.Error()) ||
		strings.Contains(strings.ToLower(err.Error()), "no space left on device")) {
		return "Free up disk space on the server and send your message again."
	}
	return "Fix the devcontainer configuration and send your message again."
}

func gib(n uint64) string {
	return strconv.FormatFloat(float64(n)/float64(uint64(1)<<30), 'f', 1, 64) + " GiB"
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

type dockerStorageFunc func(ctx context.Context) (dockerprovider.DaemonStorageInfo, error)

// measurableDockerRoot is a conservative local graph-driver probe. A local
// Unix endpoint and visible path are prerequisites, not proof for containerd,
// VM storage, thin pools or independently configured snapshotters.
func measurableDockerRoot(ctx context.Context, daemonHost string, storage dockerStorageFunc, stat func(string) error) (string, error) {
	if storage == nil {
		return "", errors.New("daemon storage probe unavailable")
	}
	if !strings.HasPrefix(daemonHost, "unix://") {
		return "", errors.New("daemon is remote or behind a socket proxy")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	info, err := storage(ctx)
	if err != nil {
		return "", fmt.Errorf("daemon storage inspection: %w", err)
	}
	if strings.Contains(info.OperatingSystem, "Docker Desktop") {
		return "", errors.New("Docker Desktop image storage is inside a VM")
	}
	if !info.ClassicImageStore() {
		return "", fmt.Errorf("image storage backend %q has no measurable filesystem from daemon info", info.Driver)
	}
	if info.RootDir == "" || !filepath.IsAbs(info.RootDir) {
		return "", errors.New("daemon did not report an absolute storage path")
	}
	if err := stat(info.RootDir); err != nil {
		return "", fmt.Errorf("daemon storage path not accessible: %w", err)
	}
	return info.RootDir, nil
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
			if err := h.db.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&file); err != nil {
				h.logger.Warn("build storage check unknown; continuing without data-volume measurement", "error", err)
				return ""
			}
			if file == "" {
				return ""
			}
			return filepath.Dir(file)
		},
		dockerRoot: func(ctx context.Context) (string, error) {
			if h.docker == nil {
				return "", errors.New("no Docker client; image storage measurement unsupported")
			}
			return measurableDockerRoot(ctx, h.docker.DaemonHost(),
				func(ctx context.Context) (dockerprovider.DaemonStorageInfo, error) {
					return dockerprovider.DaemonStorage(ctx, h.docker)
				},
				func(p string) error { _, err := os.Stat(p); return err })
		},
		usage:         diskusage.Usage,
		dockerMinFree: floor,
		unknown: func(volume string, err error) {
			h.logger.Warn("build storage check unknown; continuing without this measurement", "volume", volume, "reason", err)
		},
	}
	h.provisioner.SetBuildPreflight(check.check)
}

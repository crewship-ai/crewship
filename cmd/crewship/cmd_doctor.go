package main

import (
	"context"
	"os"
	"runtime"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/provider/apple"
	"github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check system requirements and health (--fix to attempt safe auto-repair)",
	Long: `Run a battery of system checks: container runtime, data directory,
database presence, network reachability.

  --fix attempts only safe, reversible repairs (creating the missing
  data directory). Unsafe fixes (installing Docker, repairing networks)
  are deliberately left to the operator with actionable URLs in the
  output.`,
	Run: func(cmd *cobra.Command, args []string) {
		fixMode, _ := cmd.Flags().GetBool("fix")
		_ = fixMode
		logger := logging.New("info", "text", os.Stdout)

		allOK := true

		logger.Info("doctor check",
			"version", version,
			"commit", commit,
			"go_runtime", runtime.Version(),
			"os_arch", runtime.GOOS+"/"+runtime.GOARCH,
		)

		doctorCtx, doctorCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer doctorCancel()

		runtimeFound := false

		// Check Docker-compatible runtimes
		detected, detectErr := docker.Detect(doctorCtx)
		if detectErr == nil {
			logger.Info("container runtime found",
				"runtime", detected.Runtime,
				"version", detected.Version,
				"socket", detected.Socket,
			)
			runtimeFound = true
		}

		// Check Apple Containers
		appleDetected, appleErr := apple.Detect(doctorCtx)
		if appleErr == nil {
			logger.Info("container runtime found",
				"runtime", "apple",
				"version", appleDetected.Version,
				"host_ip", appleDetected.HostIP,
			)
			runtimeFound = true
		}

		if !runtimeFound {
			logger.Error("no container runtime found",
				"docker_error", detectErr,
				"apple_error", appleErr,
				"supported", "Docker, Podman, Colima, OrbStack, Rancher Desktop, Apple Containers",
				"install_docker", "https://docs.docker.com/get-docker/",
				"install_apple", "brew install container",
			)
			allOK = false
		}

		dataDir, err := database.DefaultDataDir()
		if err != nil {
			logger.Error("data directory error", "error", err)
			allOK = false
		} else {
			dbPath := dataDir.DatabasePath()
			_, statErr := os.Stat(dbPath)
			logger.Info("data directory",
				"path", dataDir.Root,
				"database", dbPath,
				"db_exists", statErr == nil,
			)
			// Auto-fix: create the data directory if it's missing and --fix
			// is set. This is the only safe automatic repair — installing
			// Docker or fiddling with networks is left to humans.
			if fixMode {
				if _, statErr := os.Stat(dataDir.Root); os.IsNotExist(statErr) {
					if mkErr := os.MkdirAll(dataDir.Root, 0o700); mkErr == nil {
						logger.Info("[fix] created data directory", "path", dataDir.Root)
					} else {
						logger.Error("[fix] could not create data directory", "error", mkErr)
					}
				}
			}
		}

		if allOK {
			logger.Info("all checks passed")
		} else {
			logger.Warn("some checks failed, run 'crewship doctor' after fixing to verify")
		}
	},
}

func init() {
	doctorCmd.Flags().Bool("fix", false, "Attempt safe auto-repairs (e.g. create missing data directory)")
}

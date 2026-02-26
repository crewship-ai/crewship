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
	Short: "Check system requirements and health",
	Run: func(cmd *cobra.Command, args []string) {
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
		}

		if allOK {
			logger.Info("all checks passed")
		} else {
			logger.Warn("some checks failed, run 'crewship doctor' after fixing to verify")
		}
	},
}

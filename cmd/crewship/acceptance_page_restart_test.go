package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Two actual daemon processes with a SIGKILL between them. The isolated fixture
// starts dashboard-only; it neither mounts Docker nor reads a developer config.
func exercisePageDaemonRestart(t *testing.T, db *sql.DB, binary, projects, ws, token, build string) {
	t.Helper()
	var sequence int
	var name, path string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &path); err != nil || path == "" {
		t.Fatalf("restart needs file-backed fixture: %v", err)
	}
	dir, err := os.MkdirTemp("", "pages-restart-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	cfg := filepath.Join(dir, "server.yaml")
	config := fmt.Sprintf("server:\n  host: 127.0.0.1\n  port: %d\nipc:\n  socket_path: %s/ipc.sock\nstorage:\n  base_path: %s/storage\n  log_path: %s/logs\n  page_projects_path: %s\n  page_runtime_origin: http://pages.example.net\nstate:\n  bolt_path: %s/state.db\nauth:\n  nextjs_url: %s\nlogging:\n  level: error\n", port, dir, dir, dir, projects, dir, endpoint)
	if err := os.WriteFile(cfg, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	cliCfg := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(cliCfg, []byte("server: "+endpoint+"\nworkspace: "+ws+"\ntoken: "+token+"\nformat: json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var cleanEnv []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "CREWSHIP_") || key == "DATABASE_URL" || key == "NEXTAUTH_SECRET" || strings.HasPrefix(key, "ENCRYPTION_KEY") {
			continue
		}
		cleanEnv = append(cleanEnv, entry)
	}
	client := &http.Client{Timeout: time.Second}
	for boot := 0; boot < 2; boot++ {
		func() {
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, binary, "start", "--no-docker", "--config", cfg, "--db", path)
			child.Dir = dir
			child.Env = append(append([]string{}, cleanEnv...), "CREWSHIP_DATA_DIR="+dir, "CREWSHIP_SKIP_SIDECAR=1")
			log, err := os.Create(filepath.Join(dir, fmt.Sprintf("boot-%d.log", boot)))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			child.Stdout = log
			child.Stderr = log
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
			ready := false
			for deadline := time.Now().Add(25 * time.Second); time.Now().Before(deadline); {
				response, err := client.Get(endpoint + "/api/health")
				if err == nil {
					response.Body.Close()
					if response.StatusCode == 200 {
						ready = true
						break
					}
				}
				time.Sleep(100 * time.Millisecond)
			}
			if !ready {
				data, _ := os.ReadFile(log.Name())
				t.Fatalf("isolated daemon did not start: %s", data)
			}
			runCLI := func(args ...string) []byte {
				cli := exec.CommandContext(ctx, binary, args...)
				cli.Dir = dir
				cli.Env = append(append([]string{}, cleanEnv...), "CREWSHIP_CONFIG="+cliCfg)
				output, err := cli.CombinedOutput()
				if err != nil {
					t.Fatalf("CLI after boot %d: %v %s", boot, err, output)
				}
				return output
			}
			output := runCLI("page", "project", "application", "health")
			var app struct {
				Publication *struct {
					Version int `json:"version"`
				} `json:"publication"`
				Artifact *struct {
					JavaScript string `json:"javascript"`
				} `json:"artifact"`
			}
			if err := json.Unmarshal(output, &app); err != nil {
				t.Fatal(err)
			}
			if boot == 0 {
				if app.Publication != nil {
					t.Fatal("withdrawal lost on restart")
				}
				runCLI("page", "project", "publish", "health", "--build", build, "--revision", "3", "--expected-publication", "2", "--reviewed-code")
				output = runCLI("page", "project", "application", "health")
				if err := json.Unmarshal(output, &app); err != nil {
					t.Fatal(err)
				}
			}
			if app.Publication == nil || app.Publication.Version != 3 || app.Artifact == nil || app.Artifact.JavaScript == "" {
				t.Fatal("published artifact lost across process restart")
			}
			t.Logf("real daemon boot %d preserved publication/artifact through CLI (build worker disabled)", boot+1)
		}()
	}
}

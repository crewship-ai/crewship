//go:build !clionly

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
	pageprofile "github.com/crewship-ai/crewship/tools/pages-build"
	"github.com/spf13/cobra"
)

const (
	fakeFingerprint = "f00d"
	fakeCurrentID   = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	fakeOldID       = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	fakeBuiltID     = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
)

// fakePageDocker replaces every Docker-facing hook for one test. images maps
// image ID to its profile label value.
type fakePageDocker struct {
	pingErr   error
	images    map[string]string
	order     []string
	buildOut  string
	buildErr  error
	buildID   string
	builds    int
	buildDir  string
	label     string
	tag       string
	removed   []string
	removeErr map[string]error
	cfg       *config.Config
	cfgErr    error
	dockerEnv string
}

func (f *fakePageDocker) install(t *testing.T) {
	t.Helper()
	saved := []any{pageDockerPing, pageLabeledImages, pageDockerBuild, pageDockerRemove, pageLoadConfig, pageProfileFinger, pageDockerHost, pageConfirm}
	t.Cleanup(func() {
		pageDockerPing = saved[0].(func(context.Context) error)
		pageLabeledImages = saved[1].(func(context.Context, string) ([]string, error))
		pageDockerBuild = saved[2].(func(context.Context, string, string, string, string, io.Writer) error)
		pageDockerRemove = saved[3].(func(context.Context, string, io.Writer) error)
		pageLoadConfig = saved[4].(func(string) (*config.Config, error))
		pageProfileFinger = saved[5].(func() string)
		pageDockerHost = saved[6].(func() string)
		pageConfirm = saved[7].(func(*cobra.Command, string) error)
	})
	if f.images == nil {
		f.images = map[string]string{}
	}
	pageDockerPing = func(context.Context) error { return f.pingErr }
	pageLabeledImages = func(_ context.Context, fp string) ([]string, error) {
		var out []string
		for _, id := range f.order {
			if label, ok := f.images[id]; ok && (fp == "" || label == fp) {
				out = append(out, id)
			}
		}
		return out, nil
	}
	pageDockerBuild = func(_ context.Context, dir, label, tag, iidfile string, w io.Writer) error {
		f.builds++
		f.buildDir, f.label, f.tag = dir, label, tag
		if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err != nil {
			return err
		}
		_, _ = io.WriteString(w, f.buildOut)
		if f.buildErr != nil {
			return f.buildErr
		}
		id := f.buildID
		if id == "" {
			id = fakeBuiltID
		}
		f.images[id] = strings.TrimPrefix(label, pageprofile.ImageLabel+"=")
		f.order = append([]string{id}, f.order...)
		return os.WriteFile(iidfile, []byte(id+"\n"), 0o600)
	}
	pageDockerRemove = func(_ context.Context, id string, _ io.Writer) error {
		if err := f.removeErr[id]; err != nil {
			return err
		}
		delete(f.images, id)
		f.removed = append(f.removed, id)
		return nil
	}
	pageLoadConfig = func(string) (*config.Config, error) { return f.cfg, f.cfgErr }
	pageProfileFinger = func() string { return fakeFingerprint }
	pageDockerHost = func() string { return f.dockerEnv }
	pageConfirm = func(cmd *cobra.Command, _ string) error {
		if yes, _ := cmd.Flags().GetBool("yes"); yes {
			return nil
		}
		return errors.New("aborted")
	}
}

func readyPagesConfig() *config.Config {
	cfg := config.Default()
	cfg.Storage.PageProjectsPath = "/srv/crewship-page-projects"
	cfg.Storage.PageRuntimeOrigin = "https://pages.example.com"
	return cfg
}

func TestPageEnable(t *testing.T) {
	for _, tc := range []struct {
		name       string
		fake       fakePageDocker
		wantErr    string
		wantStdout string
		wantBuilds int
		stderrHas  []string
		stderrNot  []string
	}{
		{
			name:       "already enabled builds nothing",
			fake:       fakePageDocker{images: map[string]string{fakeCurrentID: fakeFingerprint}, order: []string{fakeCurrentID}, cfg: readyPagesConfig()},
			wantStdout: fakeCurrentID + "\n",
			stderrHas:  []string{"already enabled"},
			stderrNot:  []string{"Still needed", "older"},
		},
		{
			name:       "builds, labels and reports older images",
			fake:       fakePageDocker{images: map[string]string{fakeOldID: "oldprofile"}, order: []string{fakeOldID}, buildOut: "Step 1/9 : FROM node\n", cfg: readyPagesConfig()},
			wantStdout: fakeBuiltID + "\n",
			wantBuilds: 1,
			stderrHas:  []string{"Step 1/9", "Pages Apps enabled", "No restart needed", "kept for rollback", "docker image rm " + fakeOldID, "https://pages.example.com"},
		},
		{
			name: "names missing projects path and runtime origin",
			fake: fakePageDocker{cfg: func() *config.Config {
				c := config.Default()
				c.Storage.BasePath = "/var/lib/crewship/crews"
				c.Auth.NextjsURL = "https://studio.example.com"
				return c
			}()},
			wantStdout: fakeBuiltID + "\n",
			wantBuilds: 1,
			stderrHas:  []string{"CREWSHIP_PAGE_PROJECTS_PATH", "absolute directory outside storage.base_path (/var/lib/crewship/crews)", "CREWSHIP_PAGE_RUNTIME_ORIGIN", "--config"},
		},
		{
			name:       "unreadable config is advisory",
			fake:       fakePageDocker{cfgErr: errors.New("config validation: boom")},
			wantStdout: fakeBuiltID + "\n",
			wantBuilds: 1,
			stderrHas:  []string{"config validation: boom", "crewship doctor"},
		},
		{
			name:    "docker missing writes nothing",
			fake:    fakePageDocker{pingErr: errors.New("the Docker daemon is not reachable")},
			wantErr: "cannot enable Pages Apps: the Docker daemon is not reachable",
		},
		{
			name:       "build failure",
			fake:       fakePageDocker{buildErr: errors.New("exit status 1"), buildOut: "ERROR: failed to solve: pnpm install\n"},
			wantErr:    "docker build failed",
			wantBuilds: 1,
		},
		{
			name:       "socket proxy refuses builds",
			fake:       fakePageDocker{buildErr: errors.New("exit status 1"), buildOut: "Error response from daemon: <html><body><h1>403 Forbidden</h1>Request forbidden by administrative rules.</body></html>\n", dockerEnv: "tcp://docker-socket-proxy:2375"},
			wantErr:    "on the Docker host itself with the same crewship version",
			wantBuilds: 1,
		},
		{
			name:       "invalid image id from build",
			fake:       fakePageDocker{buildID: "latest"},
			wantErr:    "pinned local image ID",
			wantBuilds: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.fake
			f.install(t)
			var stdout, stderr bytes.Buffer
			err := runPageEnable(context.Background(), &stdout, &stderr, "")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				if stdout.Len() != 0 {
					t.Errorf("stdout = %q on failure, want empty", stdout.String())
				}
			} else if err != nil {
				t.Fatalf("runPageEnable: %v\nstderr:\n%s", err, stderr.String())
			}
			if got := stdout.String(); tc.wantErr == "" && got != tc.wantStdout {
				t.Errorf("stdout = %q, want %q (stdout is only the image ID)", got, tc.wantStdout)
			}
			if f.builds != tc.wantBuilds {
				t.Errorf("builds = %d, want %d", f.builds, tc.wantBuilds)
			}
			if f.builds > 0 {
				if f.label != pageprofile.ImageLabel+"="+fakeFingerprint {
					t.Errorf("label = %q", f.label)
				}
				// Untagged images are pruned and, on the containerd store,
				// invisible to the label lookup.
				if f.tag != "crewship-pages-build:"+fakeFingerprint {
					t.Errorf("tag = %q", f.tag)
				}
				if _, err := os.Stat(f.buildDir); !os.IsNotExist(err) {
					t.Errorf("build context %s not removed (stat err %v)", f.buildDir, err)
				}
			}
			for _, want := range tc.stderrHas {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr lacks %q:\n%s", want, stderr.String())
				}
			}
			for _, not := range tc.stderrNot {
				if strings.Contains(stderr.String(), not) {
					t.Errorf("stderr has %q:\n%s", not, stderr.String())
				}
			}
		})
	}
}

func TestPageBuildRefusedByProxy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		out, host string
		want      bool
	}{
		{"Error response from daemon: 403 Forbidden", "", true},
		{"Request forbidden by administrative rules.", "", true},
		{"permission denied: forbidden", "tcp://docker-socket-proxy:2375", true},
		{"permission denied: forbidden", "", false},
		{"ERROR: failed to solve: process did not complete", "tcp://docker-socket-proxy:2375", false},
	} {
		if got := pageBuildRefusedByProxy(tc.out, tc.host); got != tc.want {
			t.Errorf("pageBuildRefusedByProxy(%q, %q) = %v, want %v", tc.out, tc.host, got, tc.want)
		}
	}
}

func runDisableForTest(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newPageDisableCommand()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	cmd.SetContext(context.Background())
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func TestPageDisable(t *testing.T) {
	t.Run("removes every Pages image", func(t *testing.T) {
		f := &fakePageDocker{images: map[string]string{fakeCurrentID: fakeFingerprint, fakeOldID: "old"}, order: []string{fakeCurrentID, fakeOldID}}
		f.install(t)
		stdout, stderr, err := runDisableForTest(t, "--yes")
		if err != nil {
			t.Fatalf("disable: %v", err)
		}
		if stdout != fakeCurrentID+"\n"+fakeOldID+"\n" {
			t.Errorf("stdout = %q, want the removed IDs", stdout)
		}
		if len(f.images) != 0 {
			t.Errorf("images left: %v", f.images)
		}
		if !strings.Contains(stderr, "published applications keep serving") || !strings.Contains(stderr, "crewship page enable") {
			t.Errorf("stderr does not explain the result:\n%s", stderr)
		}
	})
	t.Run("refuses without confirmation", func(t *testing.T) {
		f := &fakePageDocker{images: map[string]string{fakeCurrentID: fakeFingerprint}, order: []string{fakeCurrentID}}
		f.install(t)
		if _, _, err := runDisableForTest(t); err == nil {
			t.Fatal("disable without --yes succeeded")
		}
		if len(f.removed) != 0 {
			t.Errorf("removed %v without confirmation", f.removed)
		}
	})
	t.Run("nothing to remove", func(t *testing.T) {
		f := &fakePageDocker{}
		f.install(t)
		stdout, stderr, err := runDisableForTest(t)
		if err != nil || stdout != "" || !strings.Contains(stderr, "not enabled") {
			t.Fatalf("err=%v stdout=%q stderr=%q", err, stdout, stderr)
		}
	})
	t.Run("an image in use is reported and fails the command", func(t *testing.T) {
		f := &fakePageDocker{images: map[string]string{fakeCurrentID: fakeFingerprint, fakeOldID: "old"}, order: []string{fakeCurrentID, fakeOldID}, removeErr: map[string]error{fakeCurrentID: errors.New("image is being used by running container")}}
		f.install(t)
		stdout, stderr, err := runDisableForTest(t, "--yes")
		if err == nil || !strings.Contains(err.Error(), "1 of 2") {
			t.Fatalf("err = %v", err)
		}
		if stdout != fakeOldID+"\n" || !strings.Contains(stderr, "being used") {
			t.Errorf("stdout=%q stderr=%q", stdout, stderr)
		}
	})
	t.Run("docker missing", func(t *testing.T) {
		f := &fakePageDocker{pingErr: errors.New("not reachable")}
		f.install(t)
		if _, _, err := runDisableForTest(t, "--yes"); err == nil || !strings.Contains(err.Error(), "cannot disable") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCheckPagesApps(t *testing.T) {
	t.Parallel()
	ready := readyPagesConfig()
	noProjects := readyPagesConfig()
	noProjects.Storage.PageProjectsPath = ""
	noOrigin := readyPagesConfig()
	noOrigin.Storage.PageRuntimeOrigin = ""
	noOrigin.Auth.NextjsURL = "https://studio.example.com"
	explicit := readyPagesConfig()
	explicit.Storage.PageBuildImage = fakeOldID
	for _, tc := range []struct {
		name          string
		current, all  []string
		cfg           *config.Config
		cfgErr        error
		status, has   string
		hintHas       string
		notFailOrWarn bool
	}{
		{name: "not enabled is informational", cfg: ready, status: "INFO", has: "not enabled", hintHas: "crewship page enable"},
		{name: "outdated", all: []string{fakeOldID}, cfg: ready, status: "WARN", has: "outdated", hintHas: "crewship page enable"},
		{name: "enabled", current: []string{fakeCurrentID}, cfg: ready, status: "PASS", has: "enabled (111111111111), runtime origin https://pages.example.com"},
		{name: "projects path missing", current: []string{fakeCurrentID}, cfg: noProjects, status: "WARN", has: "page project storage is not configured", hintHas: "CREWSHIP_PAGE_PROJECTS_PATH"},
		{name: "runtime origin missing", current: []string{fakeCurrentID}, cfg: noOrigin, status: "WARN", has: "no Page runtime origin", hintHas: "CREWSHIP_PAGE_RUNTIME_ORIGIN"},
		{name: "explicit image wins", cfg: explicit, status: "PASS", has: "CREWSHIP_PAGE_BUILD_IMAGE"},
		{name: "unreadable config", current: []string{fakeCurrentID}, cfgErr: errors.New("bad yaml"), status: "WARN", has: "unreadable", hintHas: "bad yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := checkPagesApps(tc.current, tc.all, tc.cfg, tc.cfgErr)
			if got.name != pagesCheckName || got.status != tc.status || !strings.Contains(got.detail, tc.has) || !strings.Contains(got.hint, tc.hintHas) {
				t.Errorf("got %+v, want status %s detail ~%q hint ~%q", got, tc.status, tc.has, tc.hintHas)
			}
		})
	}
}

func TestRunCheckPagesApps_DockerDownIsInfo(t *testing.T) {
	f := &fakePageDocker{pingErr: errors.New("down")}
	f.install(t)
	if got := runCheckPagesApps(context.Background()); got.status != "INFO" {
		t.Errorf("got %+v, want INFO — the container runtime row owns a missing daemon", got)
	}
}

// The Pages settings must be readable where no crewship-sidecar sits next to
// the binary (the test binary has none), and the caller's environment must be
// left as it was.
func TestLoadPagesServerConfig_IgnoresSidecarAndRestoresEnv(t *testing.T) {
	t.Setenv("CREWSHIP_SKIP_SIDECAR", "0")
	t.Setenv("CREWSHIP_SIDECAR_PATH", "")
	t.Setenv("CREWSHIP_PAGE_PROJECTS_PATH", "/srv/crewship-page-projects")
	cfg, err := loadPagesServerConfig("")
	if err != nil {
		t.Fatalf("loadPagesServerConfig: %v", err)
	}
	if cfg.Storage.PageProjectsPath != "/srv/crewship-page-projects" {
		t.Errorf("projects path = %q", cfg.Storage.PageProjectsPath)
	}
	if got := os.Getenv("CREWSHIP_SKIP_SIDECAR"); got != "0" {
		t.Errorf("CREWSHIP_SKIP_SIDECAR = %q after the read, want the caller's %q", got, "0")
	}
}

// The real docker CLI path, read-only: the label query must parse against a
// live daemon. Never builds or removes anything. Skips without Docker.
func TestPageLabeledImages_LiveDaemon(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not installed")
	}
	if err := pageDockerPingCLI(context.Background()); err != nil {
		t.Skipf("no Docker daemon: %v", err)
	}
	if _, err := pageLabeledImages(context.Background(), pageprofile.Fingerprint()); err != nil {
		t.Fatalf("LabeledImages: %v", err)
	}
}

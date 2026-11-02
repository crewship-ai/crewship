//go:build !clionly

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/pagebuild"
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
// image ID to its profile label value; tags maps image ID to its RepoTags
// (default: the exact compiler tag for its label). recorded is written to the
// record in the config's page project storage (a temp dir) before the test.
type fakePageDocker struct {
	pingErr   error
	images    map[string]string
	tags      map[string][]string
	recorded  map[string]string
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
	saved := []any{pageDockerPing, pageInspectImage, pageDockerBuild, pageDockerRemove, pageLoadConfig, pageImageFinger, pageDockerHost, pageConfirm}
	t.Cleanup(func() {
		pageDockerPing = saved[0].(func(context.Context) error)
		pageInspectImage = saved[1].(func(context.Context, string) (pagebuild.ImageInfo, error))
		pageDockerBuild = saved[2].(func(context.Context, string, string, string, string, io.Writer) error)
		pageDockerRemove = saved[3].(func(context.Context, string, io.Writer) error)
		pageLoadConfig = saved[4].(func(string) (*config.Config, error))
		pageImageFinger = saved[5].(func() string)
		pageDockerHost = saved[6].(func() string)
		pageConfirm = saved[7].(func(*cobra.Command, string) error)
	})
	if f.images == nil {
		f.images = map[string]string{}
	}
	if f.tags == nil {
		f.tags = map[string][]string{}
	}
	if f.cfg != nil && f.cfg.Storage.PageProjectsPath != "" {
		f.cfg.Storage.PageProjectsPath = t.TempDir()
		if len(f.recorded) > 0 {
			rec := &pagebuild.BuildImageRecord{Images: map[string]pagebuild.RecordedImage{}}
			for fp, id := range f.recorded {
				rec.Images[fp] = pagebuild.RecordedImage{ID: id, Tag: pagebuild.ImageTag(fp)}
			}
			if err := pagebuild.WriteBuildImageRecord(f.cfg.Storage.PageProjectsPath, rec); err != nil {
				t.Fatal(err)
			}
		}
	}
	pageDockerPing = func(context.Context) error { return f.pingErr }
	pageInspectImage = func(_ context.Context, ref string) (pagebuild.ImageInfo, error) {
		label, ok := f.images[ref]
		if !ok {
			return pagebuild.ImageInfo{}, errors.New("No such image: " + ref)
		}
		tags, ok := f.tags[ref]
		if !ok {
			tags = []string{pagebuild.ImageTag(label)}
		}
		return pagebuild.ImageInfo{ID: ref, RepoTags: tags, Labels: map[string]string{pageprofile.ImageLabel: label}}, nil
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
	pageImageFinger = func() string { return fakeFingerprint }
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
	const attackerID = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	noOrigin := func() *config.Config {
		c := readyPagesConfig()
		c.Storage.PageRuntimeOrigin = ""
		c.Auth.NextjsURL = "https://studio.example.com"
		return c
	}
	noProjects := func() *config.Config {
		c := readyPagesConfig()
		c.Storage.PageProjectsPath = ""
		c.Storage.BasePath = "/var/lib/crewship/crews"
		return c
	}
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
			fake:       fakePageDocker{images: map[string]string{fakeCurrentID: fakeFingerprint}, recorded: map[string]string{fakeFingerprint: fakeCurrentID}, cfg: readyPagesConfig()},
			wantStdout: fakeCurrentID + "\n",
			stderrHas:  []string{"already enabled"},
			stderrNot:  []string{"Still needed", "rollback"},
		},
		{
			name:       "builds, records and reports other versions",
			fake:       fakePageDocker{images: map[string]string{fakeOldID: "oldprofile"}, recorded: map[string]string{"oldprofile": fakeOldID}, buildOut: "Step 1/9 : FROM node\n", cfg: readyPagesConfig()},
			wantStdout: fakeBuiltID + "\n",
			wantBuilds: 1,
			stderrHas:  []string{"Step 1/9", "Pages Apps enabled", "recorded in", "No restart needed", "kept for rollback", "disable --all", "https://pages.example.com"},
		},
		{
			// #3108 security review: an image carrying this release's label
			// (a crew image built FROM a labelled base) is not the compiler.
			name:       "a label-only image is never adopted",
			fake:       fakePageDocker{images: map[string]string{attackerID: fakeFingerprint}, tags: map[string][]string{attackerID: {"crewship-cache:crew-1"}}, cfg: readyPagesConfig()},
			wantStdout: fakeBuiltID + "\n",
			wantBuilds: 1,
		},
		{
			name:       "a recorded image that lost its tag is rebuilt and re-recorded",
			fake:       fakePageDocker{images: map[string]string{fakeCurrentID: fakeFingerprint}, tags: map[string][]string{fakeCurrentID: {"evil/runtime:latest"}}, recorded: map[string]string{fakeFingerprint: fakeCurrentID}, cfg: readyPagesConfig()},
			wantStdout: fakeBuiltID + "\n",
			wantBuilds: 1,
			stderrHas:  []string{"not usable", "not tagged"},
		},
		{
			name:       "names a missing runtime origin",
			fake:       fakePageDocker{cfg: noOrigin()},
			wantStdout: fakeBuiltID + "\n",
			wantBuilds: 1,
			stderrHas:  []string{"CREWSHIP_PAGE_RUNTIME_ORIGIN", "--config"},
		},
		{
			name:    "no project storage refuses before building",
			fake:    fakePageDocker{cfg: noProjects()},
			wantErr: "page project storage is not configured",
		},
		{
			name:    "unreadable config refuses",
			fake:    fakePageDocker{cfgErr: errors.New("config validation: boom")},
			wantErr: "config validation: boom",
		},
		{
			name:    "docker missing writes nothing",
			fake:    fakePageDocker{pingErr: errors.New("the Docker daemon is not reachable"), cfg: readyPagesConfig()},
			wantErr: "cannot enable Pages Apps: the Docker daemon is not reachable",
		},
		{
			name:       "build failure",
			fake:       fakePageDocker{buildErr: errors.New("exit status 1"), buildOut: "ERROR: failed to solve: pnpm install\n", cfg: readyPagesConfig()},
			wantErr:    "docker build failed",
			wantBuilds: 1,
		},
		{
			name:       "socket proxy refuses builds",
			fake:       fakePageDocker{buildErr: errors.New("exit status 1"), buildOut: "Error response from daemon: <html><body><h1>403 Forbidden</h1>Request forbidden by administrative rules.</body></html>\n", dockerEnv: "tcp://docker-socket-proxy:2375", cfg: readyPagesConfig()},
			wantErr:    "--image-only",
			wantBuilds: 1,
		},
		{
			name:       "invalid image id from build",
			fake:       fakePageDocker{buildID: "latest", cfg: readyPagesConfig()},
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
				if f.tag != "crewship-pages-build:"+fakeFingerprint {
					t.Errorf("tag = %q", f.tag)
				}
				if _, err := os.Stat(f.buildDir); !os.IsNotExist(err) {
					t.Errorf("build context %s not removed (stat err %v)", f.buildDir, err)
				}
			}
			if tc.wantErr == "" {
				// The server trusts only the record: it must name the
				// printed image, with the exact tag, in a 0600 file.
				dir := f.cfg.Storage.PageProjectsPath
				rec, err := pagebuild.ReadBuildImageRecord(dir)
				if err != nil {
					t.Fatalf("record: %v", err)
				}
				if got := rec.Images[fakeFingerprint]; got.ID+"\n" != tc.wantStdout || got.Tag != pagebuild.ImageTag(fakeFingerprint) {
					t.Errorf("record entry = %+v, want %s", got, strings.TrimSpace(tc.wantStdout))
				}
				if info, err := os.Stat(pagebuild.BuildImageRecordPath(dir)); err != nil || info.Mode().Perm() != 0o600 {
					t.Errorf("record mode = %v (err %v), want 0600", info.Mode().Perm(), err)
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

func TestPageEnableImageOnlyRecordsNothing(t *testing.T) {
	f := &fakePageDocker{cfg: readyPagesConfig()}
	f.install(t)
	var stdout, stderr bytes.Buffer
	if err := runPageEnableImageOnly(context.Background(), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != fakeBuiltID+"\n" || !strings.Contains(stderr.String(), "CREWSHIP_PAGE_BUILD_IMAGE="+fakeBuiltID) {
		t.Fatalf("stdout %q stderr %q", stdout.String(), stderr.String())
	}
	if _, err := os.Stat(pagebuild.BuildImageRecordPath(f.cfg.Storage.PageProjectsPath)); !os.IsNotExist(err) {
		t.Fatalf("--image-only wrote a record (stat err %v)", err)
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
	both := func() *fakePageDocker {
		return &fakePageDocker{
			images:   map[string]string{fakeCurrentID: fakeFingerprint, fakeOldID: "old"},
			recorded: map[string]string{fakeFingerprint: fakeCurrentID, "old": fakeOldID},
			cfg:      readyPagesConfig(),
		}
	}
	recordOf := func(t *testing.T, f *fakePageDocker) map[string]pagebuild.RecordedImage {
		t.Helper()
		rec, err := pagebuild.ReadBuildImageRecord(f.cfg.Storage.PageProjectsPath)
		if err != nil {
			t.Fatal(err)
		}
		return rec.Images
	}
	t.Run("keeps other versions' images by default", func(t *testing.T) {
		f := both()
		f.install(t)
		stdout, _, err := runDisableForTest(t, "--yes")
		if err != nil {
			t.Fatalf("disable: %v", err)
		}
		if stdout != fakeCurrentID+"\n" {
			t.Errorf("stdout = %q, want only this version's image", stdout)
		}
		if _, kept := f.images[fakeOldID]; !kept || len(f.images) != 1 {
			t.Errorf("images left: %v, want only the other version's", f.images)
		}
		if rec := recordOf(t, f); len(rec) != 1 || rec["old"].ID != fakeOldID {
			t.Errorf("record = %v, want only the other version", rec)
		}
	})
	t.Run("--all removes every recorded image", func(t *testing.T) {
		f := both()
		f.install(t)
		stdout, stderr, err := runDisableForTest(t, "--yes", "--all")
		if err != nil {
			t.Fatalf("disable: %v", err)
		}
		if stdout != fakeCurrentID+"\n"+fakeOldID+"\n" && stdout != fakeOldID+"\n"+fakeCurrentID+"\n" {
			t.Errorf("stdout = %q, want the removed IDs", stdout)
		}
		if len(f.images) != 0 || len(recordOf(t, f)) != 0 {
			t.Errorf("images left: %v, record %v", f.images, recordOf(t, f))
		}
		if !strings.Contains(stderr, "published applications keep serving") || !strings.Contains(stderr, "crewship page enable") {
			t.Errorf("stderr does not explain the result:\n%s", stderr)
		}
	})
	t.Run("a recorded ID whose image no longer verifies is forgotten, not removed", func(t *testing.T) {
		f := both()
		f.tags = map[string][]string{fakeCurrentID: {"crewship-cache:crew-1"}}
		f.install(t)
		stdout, stderr, err := runDisableForTest(t, "--yes")
		if err != nil {
			t.Fatalf("disable: %v", err)
		}
		if stdout != "" || len(f.removed) != 0 || !strings.Contains(stderr, "not removing") {
			t.Fatalf("stdout %q removed %v stderr %q", stdout, f.removed, stderr)
		}
		if _, still := recordOf(t, f)[fakeFingerprint]; still {
			t.Error("record still names this version")
		}
	})
	t.Run("refuses without confirmation", func(t *testing.T) {
		f := both()
		f.install(t)
		if _, _, err := runDisableForTest(t); err == nil {
			t.Fatal("disable without --yes succeeded")
		}
		if len(f.removed) != 0 || len(recordOf(t, f)) != 2 {
			t.Errorf("removed %v / record %v without confirmation", f.removed, recordOf(t, f))
		}
	})
	t.Run("nothing recorded", func(t *testing.T) {
		f := &fakePageDocker{images: map[string]string{fakeCurrentID: fakeFingerprint}, cfg: readyPagesConfig()}
		f.install(t)
		stdout, stderr, err := runDisableForTest(t, "--yes", "--all")
		if err != nil || stdout != "" || !strings.Contains(stderr, "not enabled") || len(f.removed) != 0 {
			t.Fatalf("err=%v stdout=%q stderr=%q removed=%v", err, stdout, stderr, f.removed)
		}
	})
	t.Run("an image in use is reported and fails the command", func(t *testing.T) {
		f := both()
		f.removeErr = map[string]error{fakeCurrentID: errors.New("image is being used by running container")}
		f.install(t)
		stdout, stderr, err := runDisableForTest(t, "--yes", "--all")
		if err == nil || !strings.Contains(err.Error(), "1 of 2") {
			t.Fatalf("err = %v", err)
		}
		if stdout != fakeOldID+"\n" || !strings.Contains(stderr, "being used") {
			t.Errorf("stdout=%q stderr=%q", stdout, stderr)
		}
	})
	t.Run("docker missing", func(t *testing.T) {
		f := &fakePageDocker{pingErr: errors.New("not reachable"), cfg: readyPagesConfig()}
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
	enabled := pagesImageState{recorded: fakeCurrentID}
	for _, tc := range []struct {
		name        string
		state       pagesImageState
		cfg         *config.Config
		cfgErr      error
		status, has string
		hintHas     string
	}{
		{name: "not enabled is informational", cfg: ready, status: "INFO", has: "not enabled", hintHas: "crewship page enable"},
		{name: "outdated", state: pagesImageState{outdated: true}, cfg: ready, status: "WARN", has: "outdated", hintHas: "crewship page enable"},
		{name: "record does not verify", state: pagesImageState{problem: "(image 111111111111 is not tagged crewship-pages-build:f00d)"}, cfg: ready, status: "WARN", has: "not tagged", hintHas: "crewship page enable"},
		{name: "enabled", state: enabled, cfg: ready, status: "PASS", has: "enabled (111111111111), runtime origin https://pages.example.com"},
		{name: "projects path missing", state: enabled, cfg: noProjects, status: "WARN", has: "page project storage is not configured", hintHas: "CREWSHIP_PAGE_PROJECTS_PATH"},
		{name: "runtime origin missing", state: enabled, cfg: noOrigin, status: "WARN", has: "no Page runtime origin", hintHas: "CREWSHIP_PAGE_RUNTIME_ORIGIN"},
		{name: "explicit image wins", cfg: explicit, status: "PASS", has: "CREWSHIP_PAGE_BUILD_IMAGE"},
		{name: "unreadable config", state: enabled, cfgErr: errors.New("bad yaml"), status: "WARN", has: "unreadable", hintHas: "bad yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := checkPagesApps(tc.state, tc.cfg, tc.cfgErr)
			if got.name != pagesCheckName || got.status != tc.status || !strings.Contains(got.detail, tc.has) || !strings.Contains(got.hint, tc.hintHas) {
				t.Errorf("got %+v, want status %s detail ~%q hint ~%q", got, tc.status, tc.has, tc.hintHas)
			}
		})
	}
}

func TestPagesRecordState(t *testing.T) {
	const attackerID = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	for _, tc := range []struct {
		name string
		fake fakePageDocker
		want pagesImageState
		has  string
	}{
		{name: "verified", fake: fakePageDocker{images: map[string]string{fakeCurrentID: fakeFingerprint}, recorded: map[string]string{fakeFingerprint: fakeCurrentID}}, want: pagesImageState{recorded: fakeCurrentID}},
		{name: "label-only image is not enabled", fake: fakePageDocker{images: map[string]string{attackerID: fakeFingerprint}, tags: map[string][]string{attackerID: {"crewship-cache:x"}}}, want: pagesImageState{}},
		{name: "other version only", fake: fakePageDocker{images: map[string]string{fakeOldID: "old"}, recorded: map[string]string{"old": fakeOldID}}, want: pagesImageState{outdated: true}},
		{name: "recorded image gone", fake: fakePageDocker{recorded: map[string]string{fakeFingerprint: fakeCurrentID}}, has: "No such image"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.fake
			f.cfg = readyPagesConfig()
			f.install(t)
			got := pagesRecordState(context.Background(), f.cfg.Storage.PageProjectsPath)
			if tc.has != "" {
				if got.recorded != "" || !strings.Contains(got.problem, tc.has) {
					t.Fatalf("got %+v, want a problem naming %q", got, tc.has)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
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

// B10 (G4): a disable that is waiting for confirmation must not overwrite an
// enable of another version that completes meanwhile.
func TestPageDisableKeepsEnableCommittedWhileConfirming(t *testing.T) {
	f := &fakePageDocker{
		images:   map[string]string{fakeCurrentID: fakeFingerprint, fakeOldID: "old"},
		recorded: map[string]string{fakeFingerprint: fakeCurrentID, "old": fakeOldID},
		cfg:      readyPagesConfig(),
	}
	f.install(t)
	const newID = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
	pageConfirm = func(*cobra.Command, string) error {
		return recordPageToolsImage(f.cfg.Storage.PageProjectsPath, "newer", newID)
	}
	if _, _, err := runDisableForTest(t, "--yes"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	rec, err := pagebuild.ReadBuildImageRecord(f.cfg.Storage.PageProjectsPath)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Images["newer"].ID != newID || rec.Images["old"].ID != fakeOldID || len(rec.Images) != 2 {
		t.Fatalf("record = %v, want the concurrent enable and the other version kept", rec.Images)
	}
}

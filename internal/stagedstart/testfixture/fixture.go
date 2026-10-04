//go:build linux

// Package testfixture provides the exercised malicious-image and packet recorder
// used by standalone keeper and staged-provider acceptance tests.
package testfixture

import (
	"context"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

type Fixture struct {
	Client                                        *client.Client
	Context                                       context.Context
	Root, Prefix, Binary, Bootstrap, FaultWrapper string
	BaseID, Architecture, ImageID                 string
	OriginDir, OriginIP                           string
	sourceID, originID                            string
}

func New(t *testing.T, ctx context.Context, bootstrap []byte) *Fixture {
	t.Helper()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	f := &Fixture{Client: cli, Context: ctx, Prefix: "staged-" + strings.ToLower(rand.Text())}
	t.Cleanup(func() { _ = cli.Close() })
	base, err := cli.ImageInspect(ctx, "alpine:3")
	if err != nil {
		t.Fatalf("local pinned alpine:3 required: %v", err)
	}
	f.BaseID, f.Architecture = base.ID, base.Architecture
	f.Root, err = os.MkdirTemp("", "crewship-staged-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.cleanup(t) })
	f.Binary, f.Bootstrap = filepath.Join(f.Root, "sidecar"), filepath.Join(f.Root, "bootstrap.sh")
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	build := exec.CommandContext(ctx, "go", "build", "-o", f.Binary, "./cmd/crewship-sidecar")
	build.Dir = repo
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("sidecar build: %v\n%s", err, out)
	}
	if err := os.Chmod(f.Binary, 0555); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.Bootstrap, bootstrap, 0555); err != nil {
		t.Fatal(err)
	}
	f.FaultWrapper = buildStagedFenceFaultWrapper(t, ctx, f.Root)
	source, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{Image: base.ID}, HostConfig: &container.HostConfig{NetworkMode: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	f.sourceID = source.ID
	image, err := cli.ContainerCommit(ctx, source.ID, client.ContainerCommitOptions{Config: &container.Config{
		Entrypoint:  []string{"/bin/sh", "-ic", "set -eu; echo ran >> /crew/shared/.memory/image-entrypoint-ran-$HOSTNAME; wget -T 2 -qO- $(cat /crew/shared/.memory/origin-url)/$HOSTNAME >/dev/null; exec sleep infinity"},
		Healthcheck: &container.HealthConfig{Test: []string{"CMD-SHELL", "echo ran >> /crew/shared/.memory/image-healthcheck-ran-$HOSTNAME"}, Interval: time.Second},
		Env:         []string{"PATH=/usr/bin:/bin", "ENV=/crew/shared/.memory/inherited-shell"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	f.ImageID = image.ID
	if _, err := cli.NetworkCreate(ctx, f.Prefix, client.NetworkCreateOptions{}); err != nil {
		t.Fatal(err)
	}
	f.OriginDir = filepath.Join(f.Root, "origin")
	if err := os.Mkdir(f.OriginDir, 0777); err != nil {
		t.Fatal(err)
	}
	origin, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{Image: base.ID, Entrypoint: []string{"/fixture-fence-wrapper", "--origin"}, Healthcheck: &container.HealthConfig{Test: []string{"NONE"}}}, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(f.Prefix), ReadonlyRootfs: true, Mounts: []mount.Mount{
		{Type: mount.TypeBind, Source: f.OriginDir, Target: "/origin"},
		{Type: mount.TypeBind, Source: f.FaultWrapper, Target: "/fixture-fence-wrapper", ReadOnly: true},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	f.originID = origin.ID
	if _, err := cli.ContainerStart(ctx, origin.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	inspected, err := cli.ContainerInspect(ctx, origin.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f.OriginIP = inspected.Container.NetworkSettings.Networks[f.Prefix].IPAddress.String()
	return f
}

func (f *Fixture) Arrivals(t *testing.T) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.OriginDir, "arrivals"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("origin evidence: %v", err)
	}
	return strings.Count(string(raw), "ACCEPT ")
}

func (f *Fixture) WriteCanaries(t *testing.T, memoryDir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(memoryDir, "origin-url"), []byte("http://"+f.OriginIP+":8080/image-start"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "inherited-shell"), []byte("echo ran >> /crew/shared/.memory/image-env-ran-$HOSTNAME\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

var Canaries = []string{"image-entrypoint-ran", "image-healthcheck-ran", "image-env-ran"}

// Calibrate preserves actual candidate user, groups, mounts and permissions.
// Only image startup/environment/healthcheck are restored in a distinct unfenced
// namespace. Positive evidence is never reset or confused with candidate data.
func (f *Fixture) Calibrate(t *testing.T, candidate container.InspectResponse, memoryDir string) {
	t.Helper()
	cli, ctx := f.Client, f.Context
	inherited, err := cli.ImageInspect(ctx, f.ImageID)
	if err != nil || inherited.Config == nil {
		t.Fatalf("canary image config: %v", err)
	}
	hostname := candidate.Config.Hostname
	if hostname == "" {
		t.Fatal("candidate hostname missing")
	}
	config := *candidate.Config
	config.Hostname = ""
	config.Entrypoint, config.Cmd = inherited.Config.Entrypoint, inherited.Config.Cmd
	config.Env, config.Healthcheck = inherited.Config.Env, inherited.Config.Healthcheck
	config.Labels = map[string]string{}
	host := *candidate.HostConfig
	host.RestartPolicy = container.RestartPolicy{Name: "no"}
	control, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &config, HostConfig: &host})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := cli.ContainerRemove(cleanup, control.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
			t.Errorf("calibration cleanup: %v", err)
		}
	}()
	observed, err := cli.ContainerInspect(ctx, control.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	controlHostname := observed.Container.Config.Hostname
	if controlHostname == "" || controlHostname == hostname {
		t.Fatal("control/candidate identities overlap")
	}
	before := f.Arrivals(t)
	if _, err := cli.ContainerStart(ctx, control.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		all := f.Arrivals(t) > before
		for _, name := range Canaries {
			info, err := os.Stat(filepath.Join(memoryDir, name+"-"+controlHostname))
			if err == nil && info.Sys().(*syscall.Stat_t).Uid != 1002 {
				t.Fatalf("canary %s not written by UID1002", name)
			}
			all = all && err == nil
		}
		if all {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("image/user/mount execution, ENV, healthcheck or listener calibration did not fire")
		}
		time.Sleep(25 * time.Millisecond)
	}
	raw, err := os.ReadFile(filepath.Join(f.OriginDir, "arrivals"))
	if err != nil || !strings.Contains(string(raw), "GET /image-start/"+controlHostname+" ") {
		t.Fatalf("actual control request absent: %v", err)
	}
}

func (f *Fixture) cleanup(t *testing.T) {
	t.Helper()
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	cli := f.Client
	for _, id := range []string{f.originID, f.sourceID} {
		if id != "" {
			_, _ = cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
		}
	}
	if f.ImageID != "" {
		_, _ = cli.ImageRemove(ctx, f.ImageID, client.ImageRemoveOptions{Force: true})
	}
	_, _ = cli.NetworkRemove(ctx, f.Prefix, client.NetworkRemoveOptions{})
	// Parent tests stop their runtimes first. Only this exact tree is relaxed;
	// fixture-owned UID1001 data must not become an unremovable host orphan.
	helper, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{Image: f.BaseID, User: "0:0", Entrypoint: []string{"/bin/sh", "-c", "find /fixture -type d -exec chmod 0777 {} +"}, Healthcheck: &container.HealthConfig{Test: []string{"NONE"}}}, HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, Mounts: []mount.Mount{{Type: mount.TypeBind, Source: f.Root, Target: "/fixture"}}}})
	if err == nil {
		_, _ = cli.ContainerStart(ctx, helper.ID, client.ContainerStartOptions{})
		wait := cli.ContainerWait(ctx, helper.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
		select {
		case <-wait.Result:
		case <-wait.Error:
		case <-ctx.Done():
		}
		_, _ = cli.ContainerRemove(ctx, helper.ID, client.ContainerRemoveOptions{Force: true})
	}
	if err := os.RemoveAll(f.Root); err != nil {
		t.Errorf("owned fixture cleanup: %v", err)
	}
}

func buildStagedFenceFaultWrapper(t *testing.T, ctx context.Context, root string) string {
	t.Helper()
	source := filepath.Join(root, "fence-fault.go")
	const program = `package main
import (
    "os"
    "net/http"
    "net"
    "fmt"
    "path/filepath"
    "strconv"
    "syscall"
    "time"
)
type recordingListener struct { net.Listener }
func (l recordingListener) Accept() (net.Conn, error) {
    c, err := l.Listener.Accept()
    if err != nil { return nil, err }
    f, err := os.OpenFile("/origin/arrivals", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
    if err == nil {
        _, err = fmt.Fprintf(f, "ACCEPT %s %s\n", c.RemoteAddr(), time.Now().UTC().Format(time.RFC3339Nano))
        closeErr := f.Close()
        if err == nil { err = closeErr }
    }
    if err != nil { c.Close(); return nil, err }
    return c, nil
}
func main() {
    if len(os.Args) == 2 && os.Args[1] == "--origin" {
        http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
            f, err := os.OpenFile("/origin/arrivals", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
            if err != nil { http.Error(w, "evidence unavailable", 500); return }
            _, err = fmt.Fprintf(f, "%s %s HTTP/1.1\n", r.Method, r.URL.RequestURI())
            closeErr := f.Close()
            if err != nil || closeErr != nil { http.Error(w, "evidence unavailable", 500); return }
            w.Header().Set("Connection", "close")
            fmt.Fprint(w, "ok")
        })
        listener, err := net.Listen("tcp", ":8080")
        if err != nil { panic(err) }
        server := http.Server{ReadHeaderTimeout: 2*time.Second}
        if err := server.Serve(recordingListener{listener}); err != nil { panic(err) }
        return
    }
    stamp := func(name string, value int64) {
        if err := os.WriteFile(filepath.Join("/fixture-fence-observation", name), []byte(strconv.FormatInt(value, 10)), 0644); err != nil { panic(err) }
    }
    started := time.Now()
    stamp("started", started.UnixNano())
    if os.Getenv("STAGED_FIXTURE_FAULT") == "fail" { os.Exit(42) }
    time.Sleep(2*time.Second)
    stamp("delay-complete", time.Since(started).Nanoseconds())
    if len(os.Args) < 2 { panic("missing actual helper") }
    if err := syscall.Exec(os.Args[1], append([]string{os.Args[1]}, os.Args[2:]...), os.Environ()); err != nil { panic(err) }
}
`
	if e := os.WriteFile(source, []byte(program), 0600); e != nil {
		t.Fatal(e)
	}
	binary := filepath.Join(root, "fence-fault")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, source)
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatalf("static helper fault wrapper build: %v\n%s", e, out)
	}
	if e := os.Chmod(binary, 0555); e != nil {
		t.Fatal(e)
	}
	return binary
}

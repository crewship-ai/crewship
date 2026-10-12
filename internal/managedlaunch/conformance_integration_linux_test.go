//go:build integration && linux

package managedlaunch

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestNativeLauncherConformanceRealDocker runs the portable component without a
// Crewship server, DB, sidecar proxy, provider adapter or credentials. It fails
// when Docker is absent. C/X coverage limitations are recorded in the spec.
func TestNativeLauncherConformanceRealDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 170*time.Second)
	defer cancel()
	command := func(t *testing.T, args ...string) []byte {
		t.Helper()
		c := exec.CommandContext(ctx, "docker", args...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v %s", args[0], err, out)
		}
		return out
	}
	kernel, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("A1_KERNEL %s", strings.TrimSpace(string(kernel)))
	t.Logf("A1_PLATFORM go=%s os=%s arch=%s docker=%s", runtime.Version(), runtime.GOOS, runtime.GOARCH, strings.TrimSpace(string(command(t, "info", "--format", "{{.ServerVersion}}"))))
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	tag := "crewship-launch-conformance-" + hex.EncodeToString(nonce)
	label := "crewship.test.managedlaunch=" + tag
	dir := t.TempDir()
	// Keep build products on the configured test temp filesystem, never tmpfs.
	source := `package main
import("encoding/json";"os";"strings";"syscall")
func main(){
 var usage syscall.Rusage
 if syscall.Getrusage(syscall.RUSAGE_SELF,&usage)!=nil { os.Exit(93) }
 origin:="image";if strings.HasPrefix(os.Args[0],"/home/"){origin="stale-home"}
 _=json.NewEncoder(os.Stdout).Encode(map[string]any{"argv":os.Args,"env":os.Environ(),"uid":os.Getuid(),"origin":origin,"cpu_ns":usage.Utime.Nano()+usage.Stime.Nano(),"peak_rss_kib":usage.Maxrss})
}`
	if err := os.WriteFile(filepath.Join(dir, "native.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	build := func(t *testing.T, target string, args ...string) {
		t.Helper()
		c := exec.CommandContext(ctx, "go", append([]string{"build", "-o", target}, args...)...)
		c.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("static build: %v %s", err, out)
		}
	}
	build(t, filepath.Join(dir, "native"), filepath.Join(dir, "native.go"))
	// Go's output mode inherits the host umask. Explicitly create a sealed
	// image fixture; do not weaken the production mode/ownership check.
	if err := os.Chmod(filepath.Join(dir, "native"), 0555); err != nil {
		t.Fatal(err)
	}
	build(t, filepath.Join(dir, "launcher"), "../../cmd/crewship-launcher")
	raw, err := os.ReadFile(filepath.Join(dir, "native"))
	if err != nil {
		t.Fatal(err)
	}
	padded := make([]byte, 32<<20)
	copy(padded, raw)
	if err := os.WriteFile(filepath.Join(dir, "padded"), padded, 0555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "launcher"), 0555); err != nil {
		t.Fatal(err)
	}
	dockerfile := `FROM scratch
COPY native /opt/native/plain/codex
COPY padded /opt/native/padded/codex
COPY native /home/agent/bin/codex
COPY --chown=1002:1002 native /opt/native/foreign/codex
ENV LD_PRELOAD=evil ENV=evil BASH_ENV=evil NODE_OPTIONS=evil IMAGE_ONLY=evil PATH=/home/agent/bin
`
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0600); err != nil {
		t.Fatal(err)
	}
	// Reconcile only this test's unique labels, including an uncertain response
	// to build/create. Three settled scans catch late daemon completion.
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 45*time.Second)
		defer stop()
		for scan := 0; scan < 3; scan++ {
			out, err := exec.CommandContext(cleanup, "docker", "ps", "-aq", "--filter", "label="+label).CombinedOutput()
			if err != nil {
				t.Errorf("final container enumeration: %v %s", err, out)
				return
			}
			for _, id := range strings.Fields(string(out)) {
				proof, err := exec.CommandContext(cleanup, "docker", "inspect", "--format", `{{index .Config.Labels "crewship.test.managedlaunch"}}`, id).CombinedOutput()
				if err != nil || strings.TrimSpace(string(proof)) != tag {
					t.Errorf("cannot prove final container ownership: %v", err)
					continue
				}
				if out, err := exec.CommandContext(cleanup, "docker", "rm", "-f", "-v", id).CombinedOutput(); err != nil {
					t.Errorf("final container cleanup: %v %s", err, out)
				}
			}
			if scan < 2 {
				time.Sleep(2 * time.Second)
			}
		}
		out, err := exec.CommandContext(cleanup, "docker", "image", "ls", "-q", "--filter", "label="+label).CombinedOutput()
		if err != nil {
			t.Errorf("image cleanup enumeration: %v %s", err, out)
			return
		}
		for _, id := range strings.Fields(string(out)) {
			proof, err := exec.CommandContext(cleanup, "docker", "image", "inspect", "--format", `{{index .Config.Labels "crewship.test.managedlaunch"}}`, id).CombinedOutput()
			if err != nil || strings.TrimSpace(string(proof)) != tag {
				t.Errorf("cannot prove image cleanup ownership: %v", err)
				continue
			}
			if out, err := exec.CommandContext(cleanup, "docker", "image", "rm", id).CombinedOutput(); err != nil {
				t.Errorf("image cleanup: %v %s", err, out)
			}
		}
	})
	command(t, "build", "--network=none", "--pull=false", "--label", label, "-t", tag, dir)
	image := strings.TrimSpace(string(command(t, "image", "inspect", "--format", "{{.Id}}", tag)))
	type observation struct {
		Args   []string `json:"argv"`
		Env    []string `json:"env"`
		UID    int      `json:"uid"`
		Origin string   `json:"origin"`
		CPU    int64    `json:"cpu_ns"`
		RSS    int64    `json:"peak_rss_kib"`
	}
	serial := 0
	run := func(t *testing.T, entry string, args []string, extra []string) ([]byte, int, time.Duration) {
		t.Helper()
		serial++
		name := fmt.Sprintf("%s-%d", tag, serial)
		// Register name before Create: a disconnected Create response may leave a
		// container, so reconcile exact ownership rather than leaking it silently.
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
			defer stop()
			check := exec.CommandContext(cleanup, "docker", "inspect", "--format", `{{index .Config.Labels "crewship.test.managedlaunch"}}`, name)
			out, err := check.CombinedOutput()
			if err != nil {
				t.Errorf("container cleanup enumeration: %v %s", err, out)
				return
			}
			if strings.TrimSpace(string(out)) != tag {
				t.Errorf("container cleanup ownership mismatch")
				return
			}
			if out, err := exec.CommandContext(cleanup, "docker", "rm", "-f", "-v", name).CombinedOutput(); err != nil {
				t.Errorf("container cleanup: %v %s", err, out)
			}
		}()
		flags := []string{"create", "--name", name, "--label", label, "--network=none", "--read-only", "--user=1001:1001", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=32", "--memory=512m", "--tmpfs=/tmp:rw,nosuid,nodev,mode=1777,size=64m", "--mount=type=bind,src=" + filepath.Join(dir, "launcher") + ",dst=" + LauncherPath + ",readonly", "--entrypoint", entry}
		flags = append(flags, extra...)
		flags = append(flags, image)
		flags = append(flags, args...)
		command(t, flags...)
		start := time.Now()
		out, startErr := exec.CommandContext(ctx, "docker", "start", "-a", name).CombinedOutput()
		elapsed := time.Since(start)
		var state struct {
			Running   bool
			ExitCode  int
			Error     string
			OOMKilled bool
			Status    string
		}
		if err := json.Unmarshal(command(t, "inspect", "--format", "{{json .State}}", name), &state); err != nil {
			t.Fatal(err)
		}
		if state.Running || state.Status != "exited" || state.Error != "" || state.OOMKilled {
			t.Fatalf("Docker failed to run fixture: %+v %v %s", state, startErr, out)
		}
		if startErr != nil {
			var exit *exec.ExitError
			if !errors.As(startErr, &exit) || exit.ExitCode() != state.ExitCode {
				t.Fatalf("Docker client failure, not expected fixture refusal: %v %s", startErr, out)
			}
		}
		code := state.ExitCode
		return out, code, elapsed
	}
	observe := func(t *testing.T, out []byte, code int) observation {
		t.Helper()
		if code != 0 {
			t.Fatalf("native did not start: %d %s", code, out)
		}
		var got observation
		if err := json.Unmarshal(bytes.TrimSpace(out), &got); err != nil {
			t.Fatalf("native output: %v %s", err, out)
		}
		if got.UID != 1001 || got.CPU <= 0 || got.RSS <= 0 {
			t.Fatalf("invalid native observation: %+v", got)
		}
		return got
	}
	literal := "literal $(false); ' argument"
	t.Run("positive_stale_home_calibration", func(t *testing.T) {
		out, code, _ := run(t, "/home/agent/bin/codex", []string{literal}, nil)
		got := observe(t, out, code)
		if got.Origin != "stale-home" {
			t.Fatal("malicious home calibration did not execute")
		}
	})
	descriptor := func(t *testing.T, path string, bytes []byte) Descriptor {
		t.Helper()
		artifact, err := Capture(path, bytes)
		if err != nil {
			t.Fatal(err)
		}
		d := fuzzDescriptor()
		d.Artifact = *artifact
		d.ImageID = image
		d.RunID = fmt.Sprintf("fixture-%d", serial+1)
		d.EnvKeys = []string{"HOME", "HOST_SELECTED"}
		return d
	}
	encode := func(t *testing.T, d Descriptor) string {
		t.Helper()
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	env := []string{"--env=HOME=/home/agent", "--env=HOST_SELECTED=authoritative"}
	for name, body := range map[string][]byte{"plain": raw, "padded": padded} {
		path := "/opt/native/" + name + "/codex"
		t.Run(name+"_literal_argv_and_environment", func(t *testing.T) {
			out, code, _ := run(t, LauncherPath, []string{"--managed-launch", encode(t, descriptor(t, path, body)), literal}, env)
			got := observe(t, out, code)
			expected := []string{"PATH=" + SafePath, "HOME=/home/agent", "HOST_SELECTED=authoritative"}
			slices.Sort(expected)
			slices.Sort(got.Env)
			if !slices.Equal(got.Env, expected) || !slices.Equal(got.Args, []string{path, literal}) || got.Origin != "image" {
				t.Fatalf("untrusted executable/argv/env admitted: %+v", got)
			}
		})
	}
	t.Run("wrong_hash_no_child", func(t *testing.T) {
		d := descriptor(t, "/opt/native/plain/codex", raw)
		d.SHA256 = strings.Repeat("0", 64)
		out, code, _ := run(t, LauncherPath, []string{"--managed-launch", encode(t, d), literal}, env)
		if code != 126 || !bytes.Contains(out, []byte("executable hash mismatch")) || bytes.Contains(out, []byte(`"origin"`)) {
			t.Fatalf("hash refusal: %d %s", code, out)
		}
	})
	t.Run("foreign_owner_no_child", func(t *testing.T) {
		out, code, _ := run(t, LauncherPath, []string{"--managed-launch", encode(t, descriptor(t, "/opt/native/foreign/codex", raw)), literal}, env)
		if code != 126 || !bytes.Contains(out, []byte("unsupported executable ownership")) || bytes.Contains(out, []byte(`"origin"`)) {
			t.Fatalf("ownership refusal: %d %s", code, out)
		}
	})
	// Alternating baselines bound the effect of host/Docker drift. Getrusage is
	// cumulative across exec; record launcher+child process CPU and RSS, not
	// daemon CPU or total cgroup memory. These synthetic sizes do not qualify
	// the much larger real Codex binary or the future full-runtime C10 gate.
	type measurements struct{ start, cpu, rss []float64 }
	summary := func(values []float64) (mean, sd float64) {
		for _, v := range values {
			mean += v
		}
		mean /= float64(len(values))
		for _, v := range values {
			sd += (v - mean) * (v - mean)
		}
		sd = math.Sqrt(sd / float64(len(values)))
		return
	}
	for name, body := range map[string][]byte{"plain": raw, "padded": padded} {
		t.Run(name+"_overhead_30_warm_samples", func(t *testing.T) {
			path := "/opt/native/" + name + "/codex"
			samples := map[string]*measurements{"baseline": {}, "managed": {}}
			for i := 0; i < 30; i++ {
				for _, mode := range []string{"baseline", "managed"} {
					entry, args, extra := path, []string{literal}, []string{"--env=PATH=" + SafePath, "--env=HOME=/home/agent", "--env=HOST_SELECTED=authoritative", "--env=LD_PRELOAD=", "--env=ENV=", "--env=BASH_ENV=", "--env=NODE_OPTIONS=", "--env=IMAGE_ONLY="}
					if mode == "managed" {
						entry = LauncherPath
						args = []string{"--managed-launch", encode(t, descriptor(t, path, body)), literal}
						extra = env
					}
					out, code, elapsed := run(t, entry, args, extra)
					got := observe(t, out, code)
					m := samples[mode]
					m.start = append(m.start, float64(elapsed.Nanoseconds())/1e6)
					m.cpu = append(m.cpu, float64(got.CPU)/1e6)
					m.rss = append(m.rss, float64(got.RSS))
				}
			}
			for _, mode := range []string{"baseline", "managed"} {
				m := samples[mode]
				start, sd := summary(m.start)
				cpu, cpuSD := summary(m.cpu)
				rss, rssSD := summary(m.rss)
				t.Logf("A1_MEASUREMENT artifact_bytes=%d mode=%s n=%d docker_start_ms=%.3f sd=%.3f process_cpu_ms=%.3f sd=%.3f process_peak_rss_kib=%.1f sd=%.1f raw_start_ms=%v raw_cpu_ms=%v raw_rss_kib=%v", len(body), mode, len(m.start), start, sd, cpu, cpuSD, rss, rssSD, m.start, m.cpu, m.rss)
			}
		})
	}
}

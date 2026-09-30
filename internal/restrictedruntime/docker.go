//go:build linux

package restrictedruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Docker uses argument vectors, never a host shell. Only nonsecret identifiers
// enter argv. Output from failed commands is withheld: it can contain task data.
type Docker struct {
	Binary string
	Image  string
}

func (d Docker) command(ctx context.Context, args ...string) *exec.Cmd {
	bin := d.Binary
	if bin == "" {
		bin = "docker"
	}
	return exec.CommandContext(ctx, bin, args...)
}
func (d Docker) call(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	cmd := d.command(ctx, args...)
	cmd.Stdin = bytes.NewReader(input)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, errors.New("restricted Docker operation failed")
	}
	return out.Bytes(), nil
}

// Catalog is a trusted server resource resolver. No host bind paths are accepted.
// Volume labels bind data to workspace/scope/revision; revision is conservative
// provenance: a grant change requires a newly classified storage generation.
type Catalog interface {
	Volume(context.Context, Plan, Mount) (string, error)
}

const labelPrefix = "ai.crewship.restricted."

func resourceLabels(p Plan, r string) map[string]string {
	return map[string]string{labelPrefix + "workspace": p.Workspace, labelPrefix + "scope": p.Scope, labelPrefix + "revision": p.Revision, labelPrefix + "resource": r, labelPrefix + "provenance": p.provenance()}
}
func (d Docker) checkVolume(ctx context.Context, p Plan, m Mount, name string) error {
	if !strings.HasPrefix(name, "crewship-rtest-") || !identifier.MatchString(name) {
		return ErrDenied
	}
	b, e := d.call(ctx, nil, "volume", "inspect", name)
	if e != nil {
		return e
	}
	var v []struct {
		Driver  string
		Options map[string]string
		Labels  map[string]string
	}
	if json.Unmarshal(b, &v) != nil || len(v) != 1 || v[0].Driver != "local" {
		return ErrDenied
	}
	if p.NativeInputs != nil {
		if !validNativeInputPlan(p) || m != p.Mounts[0] || !strings.HasPrefix(name, "crewship-rtest-input-") || len(v[0].Options) != len(nativeInputVolumeOptions) {
			return ErrDenied
		}
		for key, expected := range nativeInputVolumeOptions {
			if v[0].Options[key] != expected {
				return ErrDenied
			}
		}
	} else if len(v[0].Options) != 0 {
		return ErrDenied
	}
	for k, want := range resourceLabels(p, m.Resource) {
		if v[0].Labels[k] != want {
			return ErrDenied
		}
	}
	return nil
}
func (d Docker) image(ctx context.Context) (string, error) {
	b, e := d.call(ctx, nil, "image", "inspect", d.Image)
	if e != nil {
		return "", e
	}
	var im []struct {
		ID     string
		Config struct{ Volumes map[string]any }
	}
	if json.Unmarshal(b, &im) != nil || len(im) != 1 || len(im[0].Config.Volumes) != 0 || !strings.HasPrefix(im[0].ID, "sha256:") {
		return "", ErrDenied
	}
	return im[0].ID, nil
}

type Limits struct {
	MemoryBytes int64
	NanoCPUs    int64
	PIDs        int64
}

func (l Limits) valid() bool {
	return l.MemoryBytes >= 32<<20 && l.MemoryBytes <= 1<<30 && l.NanoCPUs > 0 && l.NanoCPUs <= 2e9 && l.PIDs >= 8 && l.PIDs <= 256
}
func privateTmpfs() map[string]string {
	return map[string]string{
		"/home/agent": "rw,nosuid,nodev,noexec,size=8388608,uid=1001,gid=1001,mode=0700",
		"/secrets":    "rw,nosuid,nodev,noexec,size=1048576,uid=1001,gid=1001,mode=0700",
		"/broker":     "rw,nosuid,nodev,noexec,size=1048576,uid=1002,gid=1002,mode=0700",
		"/tmp":        "rw,nosuid,nodev,noexec,size=4194304,mode=1777",
	}
}

func (d Docker) create(ctx context.Context, p Plan, c Catalog, l Limits, owner string) (string, error) {
	image, e := d.image(ctx)
	if e != nil {
		return "", e
	}
	args := []string{"create", "--pull=never", "--name", "crewship-rtest-" + owner + "-" + p.Attempt, "--label", labelPrefix + "owner=" + owner, "--label", labelPrefix + "attempt=" + p.Attempt, "--label", labelPrefix + "plan=" + p.fingerprint(), "--user", "1002:1002", "--read-only", "--network", "none", "--ipc", "private", "--cgroupns", "private", "--runtime", "runc", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--init", "--restart", "no", "--memory", fmt.Sprint(l.MemoryBytes), "--memory-swap", fmt.Sprint(l.MemoryBytes), "--cpus", fmt.Sprintf("%.9f", float64(l.NanoCPUs)/1e9), "--pids-limit", fmt.Sprint(l.PIDs), "--shm-size", "1048576", "--log-driver", "none", "--ulimit", "nofile=256:256", "--ulimit", "core=0:0"}
	nativeOpts, cleanup, e := nativeSecurityOptions(ctx, p)
	if e != nil {
		return "", e
	}
	defer cleanup()
	args = append(args, nativeOpts...)
	for _, target := range []string{"/home/agent", "/secrets", "/broker", "/tmp"} {
		args = append(args, "--tmpfs", target+":"+privateTmpfs()[target])
	}
	used := map[string]bool{}
	for _, m := range p.Mounts {
		v, e := c.Volume(ctx, p, m)
		if e != nil {
			return "", ErrDenied
		}
		if e = d.checkVolume(ctx, p, m, v); e != nil {
			return "", e
		}
		if used[v] {
			return "", ErrDenied
		}
		used[v] = true
		s := "type=volume,source=" + v + ",target=" + m.Target + ",volume-nocopy"
		if m.ReadOnly {
			s += ",readonly"
		}
		args = append(args, "--mount", s)
	}
	args = append(args, "--entrypoint", "/opt/crewship-runner", image, "hold")
	b, e := d.call(ctx, nil, args...)
	if e != nil {
		return "", e
	}
	return strings.TrimSpace(string(b)), nil
}

func (d Docker) remove(ctx context.Context, id string) error {
	_, e := d.call(ctx, nil, "rm", "-f", id)
	return e
}
func (d Docker) stopped(ctx context.Context, id string) bool {
	b, e := d.call(ctx, nil, "inspect", "--format", "{{.State.Running}}", id)
	return e == nil && strings.TrimSpace(string(b)) == "false"
}

// renew delivers no task content or credentials; the protected init owns expiry.
func (d Docker) renew(ctx context.Context, id string, expires time.Time) error {
	b, e := json.Marshal(expires)
	if e != nil {
		return e
	}
	_, e = d.call(ctx, b, "exec", "-i", "--user", "1002:1002", id, "/opt/crewship-runner", "lease")
	return e
}

package restrictedruntime

import (
	"context"
	"encoding/json"
	"strings"
)

// audit checks the daemon's resulting configuration before any agent code or
// secret delivery. Image-declared volumes and namespace/device overrides must
// not silently broaden the hand-built profile.
func (d Docker) audit(ctx context.Context, id string, p Plan, c Catalog, l Limits) error {
	b, e := d.call(ctx, nil, "inspect", id)
	if e != nil {
		return e
	}
	var rows []struct {
		Config struct {
			User            string
			Entrypoint, Cmd []string
			Labels          map[string]string
		}
		HostConfig struct {
			NetworkMode, PidMode, IpcMode, UTSMode, UsernsMode        string
			Privileged, ReadonlyRootfs, PublishAllPorts               bool
			Init                                                      *bool
			CapAdd, CapDrop, GroupAdd, ExtraHosts, SecurityOpt, Binds []string
			Devices                                                   []any
			Memory, MemorySwap, NanoCpus, PidsLimit                   int64
			Tmpfs                                                     map[string]string
			PortBindings                                              map[string]any
			RestartPolicy                                             struct{ Name string }
			LogConfig                                                 struct{ Type string }
		}
		Mounts []struct {
			Type, Name, Destination string
			RW                      bool
		}
	}
	if json.Unmarshal(b, &rows) != nil || len(rows) != 1 {
		return ErrDenied
	}
	r := rows[0]
	h := r.HostConfig
	if r.Config.User != "1001:1001" || strings.Join(r.Config.Entrypoint, " ") != "/opt/crewship-runner" || strings.Join(r.Config.Cmd, " ") != "hold" || h.NetworkMode != "none" || h.PidMode != "" || h.IpcMode != "private" || h.UTSMode != "" || h.UsernsMode != "" || h.Privileged || !h.ReadonlyRootfs || h.PublishAllPorts || h.Init == nil || !*h.Init || len(h.CapAdd) > 0 || len(h.GroupAdd) > 0 || len(h.ExtraHosts) > 0 || len(h.Devices) > 0 || len(h.Binds) > 0 || len(h.PortBindings) > 0 || h.Memory != l.MemoryBytes || h.MemorySwap != l.MemoryBytes || h.NanoCpus != l.NanoCPUs || h.PidsLimit != l.PIDs || strings.Join(h.CapDrop, ",") != "ALL" || len(h.SecurityOpt) != 1 || (h.SecurityOpt[0] != "no-new-privileges" && h.SecurityOpt[0] != "no-new-privileges=true") || h.RestartPolicy.Name != "no" || h.LogConfig.Type != "none" {
		return ErrDenied
	}
	if len(h.Tmpfs) != 4 {
		return ErrDenied
	}
	for target, options := range privateTmpfs() {
		if h.Tmpfs[target] != options {
			return ErrDenied
		}
	}
	expected := map[string]Mount{}
	for _, m := range p.Mounts {
		expected[m.Target] = m
	}
	for _, actual := range r.Mounts {
		m, ok := expected[actual.Destination]
		if !ok || actual.Type != "volume" || actual.RW == m.ReadOnly {
			return ErrDenied
		}
		v, e := c.Volume(ctx, p, m)
		if e != nil || v != actual.Name {
			return ErrDenied
		}
		if e = d.checkVolume(ctx, p, m, v); e != nil {
			return e
		}
		delete(expected, actual.Destination)
	}
	if len(expected) != 0 {
		return ErrDenied
	}
	return nil
}

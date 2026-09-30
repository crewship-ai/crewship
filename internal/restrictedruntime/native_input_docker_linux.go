//go:build linux

package restrictedruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
)

func (c *FrozenNativeCatalog) matches(p Plan, r nativeSnapshotRecord) bool {
	return validNativeInputPlan(p) && r.Attempt == p.Attempt && r.Resource == NativeInputResource(p) && r.Workspace == p.Workspace && r.Scope == p.Scope && r.Revision == p.Revision && r.Provenance == p.provenance() && r.Fingerprint == p.fingerprint()
}

func (c *FrozenNativeCatalog) Volume(ctx context.Context, p Plan, mount Mount) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !validNativeInputPlan(p) || p.NativeInputs == nil || !identifier.MatchString(p.Attempt) || mount != p.Mounts[0] {
		return "", ErrDenied
	}
	r, err := c.load(p.Attempt)
	if os.IsNotExist(err) {
		r, err = c.stage(ctx, p)
	}
	if err != nil || !c.matches(p, r) || (r.State != "ready" && r.State != "frozen") {
		return "", ErrDenied
	}
	if err := c.docker.checkVolume(ctx, p, mount, r.Volume); err != nil {
		return "", err
	}
	return r.Volume, nil
}

func (c *FrozenNativeCatalog) stage(ctx context.Context, p Plan) (nativeSnapshotRecord, error) {
	suffix := nativeInputObjectSuffix(p.Attempt)
	r := nativeSnapshotRecord{Attempt: p.Attempt, Resource: NativeInputResource(p), Workspace: p.Workspace, Scope: p.Scope, Revision: p.Revision, Provenance: p.provenance(), Fingerprint: p.fingerprint(), Volume: "crewship-rtest-input-" + c.owner + "-" + suffix, Populator: "crewship-rtest-input-writer-" + c.owner + "-" + suffix, State: "allocated"}
	data, err := c.source(ctx, p)
	if err != nil {
		return r, err
	}
	archive, err := nativeInputArchive(p.NativeInputs, data)
	if err != nil {
		return r, err
	}
	// Persist deterministic identities before the first Docker mutation, so a
	// lost response cannot strand an unrecorded writable alias.
	if err := c.save(r); err != nil {
		return r, err
	}
	args := []string{"volume", "create", "--driver", "local"}
	for _, key := range []string{"type", "device", "o"} {
		args = append(args, "--opt", key+"="+nativeInputVolumeOptions[key])
	}
	labels := resourceLabels(p, r.Resource)
	labels[labelPrefix+"input-owner"] = c.owner
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--label", key+"="+labels[key])
	}
	args = append(args, r.Volume)
	if _, err := c.docker.call(ctx, nil, args...); err != nil {
		return r, err
	}
	if err := c.docker.checkVolume(ctx, p, p.Mounts[0], r.Volume); err != nil {
		return r, err
	}
	image, err := c.docker.image(ctx)
	if err != nil {
		return r, err
	}
	args = []string{"create", "--pull=never", "--name", r.Populator, "--label", labelPrefix + "input-owner=" + c.owner, "--label", labelPrefix + "attempt=" + r.Attempt, "--label", labelPrefix + "plan=" + r.Fingerprint, "--user", "1002:1002", "--read-only", "--network", "none", "--ipc", "private", "--cgroupns", "private", "--runtime", "runc", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--init", "--restart", "no", "--memory", "134217728", "--memory-swap", "134217728", "--cpus", "0.250000000", "--pids-limit", "16", "--log-driver", "none", "--tmpfs", "/broker:" + privateTmpfs()["/broker"], "--mount", "type=volume,source=" + r.Volume + ",target=" + NativeInputTarget + ",volume-nocopy", "--entrypoint", "/opt/crewship-runner", image, "hold"}
	if _, err := c.docker.call(ctx, nil, args...); err != nil {
		return r, err
	}
	if err := c.checkWriter(ctx, r, image); err != nil {
		return r, err
	}
	if _, err := c.docker.call(ctx, nil, "start", r.Populator); err != nil {
		return r, err
	}
	if _, err := c.docker.call(ctx, archive, "cp", "-a", "-", r.Populator+":"+NativeInputTarget); err != nil {
		return r, err
	}
	r.State = "ready"
	return r, c.save(r)
}

func (c *FrozenNativeCatalog) checkWriter(ctx context.Context, r nativeSnapshotRecord, image string) error {
	b, err := c.docker.call(ctx, nil, "inspect", r.Populator)
	if err != nil {
		return err
	}
	var rows []struct {
		Image  string
		Config struct {
			User            string
			Entrypoint, Cmd []string
			Labels          map[string]string
		}
		HostConfig struct {
			NetworkMode, PidMode                    string
			Privileged, ReadonlyRootfs              bool
			Binds, CapAdd, CapDrop, SecurityOpt     []string
			Memory, MemorySwap, NanoCpus, PidsLimit int64
			RestartPolicy                           struct{ Name string }
		}
		Mounts []struct {
			Type, Name, Destination string
			RW                      bool
		}
	}
	if json.Unmarshal(b, &rows) != nil || len(rows) != 1 {
		return ErrDenied
	}
	v := rows[0]
	h := v.HostConfig
	if v.Image != image || v.Config.User != "1002:1002" || strings.Join(v.Config.Entrypoint, " ") != "/opt/crewship-runner" || strings.Join(v.Config.Cmd, " ") != "hold" || v.Config.Labels[labelPrefix+"input-owner"] != c.owner || v.Config.Labels[labelPrefix+"attempt"] != r.Attempt || v.Config.Labels[labelPrefix+"plan"] != r.Fingerprint || h.NetworkMode != "none" || h.PidMode != "" || h.Privileged || !h.ReadonlyRootfs || len(h.Binds) != 0 || len(h.CapAdd) != 0 || strings.Join(h.CapDrop, ",") != "ALL" || strings.Join(h.SecurityOpt, ",") != "no-new-privileges" || h.Memory != 134217728 || h.MemorySwap != h.Memory || h.NanoCpus != 250000000 || h.PidsLimit != 16 || h.RestartPolicy.Name != "no" || len(v.Mounts) != 1 || v.Mounts[0].Type != "volume" || v.Mounts[0].Name != r.Volume || v.Mounts[0].Destination != NativeInputTarget || !v.Mounts[0].RW {
		return ErrDenied
	}
	return nil
}

func (c *FrozenNativeCatalog) FreezeNativeInputs(ctx context.Context, p Plan, consumer string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, err := c.load(p.Attempt)
	if err != nil || !c.matches(p, r) || r.State != "ready" || !identifier.MatchString(consumer) {
		return ErrDenied
	}
	if err := c.checkAliases(ctx, r, consumer, true); err != nil {
		return err
	}
	r.Consumer, r.State = consumer, "freezing"
	if err := c.save(r); err != nil {
		return err
	}
	if err := c.removeWriter(ctx, r); err != nil {
		return err
	}
	if err := c.checkAliases(ctx, r, consumer, false); err != nil {
		return err
	}
	// The host-authorized source must still be current after provisioning.
	data, err := c.source(ctx, p)
	if err != nil {
		return err
	}
	if _, err := nativeInputArchive(p.NativeInputs, data); err != nil {
		return err
	}
	manifest, err := json.Marshal(p.NativeInputs)
	if err != nil {
		return err
	}
	if _, err := c.docker.call(ctx, manifest, "exec", "-i", "--user", "1001:1001", consumer, "/opt/crewship-native-runner", "verify-project-inputs"); err != nil {
		return err
	}
	r.State = "frozen"
	return c.save(r)
}

func (c *FrozenNativeCatalog) checkAliases(ctx context.Context, r nativeSnapshotRecord, consumer string, writerAllowed bool) error {
	b, err := c.docker.call(ctx, nil, "ps", "-aq", "--filter", "volume="+r.Volume)
	if err != nil {
		return err
	}
	ids := strings.Fields(string(b))
	consumerSeen, writerSeen := false, false
	for _, id := range ids {
		b, err := c.docker.call(ctx, nil, "inspect", id)
		if err != nil {
			return err
		}
		var rows []struct {
			ID, Name string
			State    struct{ Running bool }
			Config   struct {
				User            string
				Entrypoint, Cmd []string
				Labels          map[string]string
			}
			HostConfig struct{ NetworkMode string }
			Mounts     []struct {
				Name, Destination string
				RW                bool
			}
		}
		if json.Unmarshal(b, &rows) != nil || len(rows) != 1 {
			return ErrDenied
		}
		v := rows[0]
		isConsumer := v.ID == consumer
		isWriter := strings.TrimPrefix(v.Name, "/") == r.Populator
		if (!isConsumer && (!writerAllowed || !isWriter)) || v.Config.Labels[labelPrefix+"attempt"] != r.Attempt || v.Config.Labels[labelPrefix+"plan"] != r.Fingerprint || v.Config.User != "1002:1002" || strings.Join(v.Config.Entrypoint, " ") != "/opt/crewship-runner" || strings.Join(v.Config.Cmd, " ") != "hold" || v.HostConfig.NetworkMode != "none" || !v.State.Running {
			return ErrDenied
		}
		mountSeen := false
		for _, m := range v.Mounts {
			if m.Name == r.Volume {
				if mountSeen || m.Destination != NativeInputTarget || m.RW == isConsumer {
					return ErrDenied
				}
				mountSeen = true
			}
		}
		if !mountSeen {
			return ErrDenied
		}
		if isConsumer {
			consumerSeen = true
		} else {
			if v.Config.Labels[labelPrefix+"input-owner"] != c.owner {
				return ErrDenied
			}
			writerSeen = true
		}
	}
	if !consumerSeen || (writerAllowed && !writerSeen) || (!writerAllowed && writerSeen) {
		return ErrDenied
	}
	return nil
}

func (c *FrozenNativeCatalog) removeWriter(ctx context.Context, r nativeSnapshotRecord) error {
	b, err := c.docker.call(ctx, nil, "ps", "-aq", "--filter", "name=^/"+r.Populator+"$")
	if err != nil {
		return err
	}
	ids := strings.Fields(string(b))
	if len(ids) > 1 {
		return ErrDenied
	}
	if len(ids) == 1 {
		b, err := c.docker.call(ctx, nil, "inspect", ids[0])
		if err != nil {
			return err
		}
		var v []struct {
			Config struct{ Labels map[string]string }
		}
		if json.Unmarshal(b, &v) != nil || len(v) != 1 || v[0].Config.Labels[labelPrefix+"input-owner"] != c.owner || v[0].Config.Labels[labelPrefix+"attempt"] != r.Attempt || v[0].Config.Labels[labelPrefix+"plan"] != r.Fingerprint {
			return ErrDenied
		}
		if err := c.docker.remove(ctx, ids[0]); err != nil {
			return err
		}
	}
	b, err = c.docker.call(ctx, nil, "ps", "-aq", "--filter", "name=^/"+r.Populator+"$")
	if err != nil || len(strings.Fields(string(b))) != 0 {
		return errors.New("snapshot writer removal unconfirmed")
	}
	return nil
}

func (c *FrozenNativeCatalog) ReleaseNativeInputs(ctx context.Context, p Plan) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, err := c.load(p.Attempt)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !c.matches(p, r) {
		return ErrDenied
	}
	return c.release(ctx, r)
}

func (c *FrozenNativeCatalog) release(ctx context.Context, r nativeSnapshotRecord) error {
	if err := c.removeWriter(ctx, r); err != nil {
		return err
	}
	b, err := c.docker.call(ctx, nil, "ps", "-aq", "--filter", "volume="+r.Volume)
	if err != nil {
		return err
	}
	if len(strings.Fields(string(b))) != 0 {
		return errors.New("snapshot still mounted")
	}
	b, err = c.docker.call(ctx, nil, "volume", "ls", "-q", "--filter", "name=^"+r.Volume+"$")
	if err != nil {
		return err
	}
	if len(strings.Fields(string(b))) > 1 {
		return ErrDenied
	}
	if len(strings.Fields(string(b))) == 1 {
		b, err = c.docker.call(ctx, nil, "volume", "inspect", r.Volume)
		if err != nil {
			return err
		}
		var rows []struct{ Labels map[string]string }
		if json.Unmarshal(b, &rows) != nil || len(rows) != 1 || rows[0].Labels[labelPrefix+"input-owner"] != c.owner || rows[0].Labels[labelPrefix+"resource"] != r.Resource || rows[0].Labels[labelPrefix+"scope"] != r.Scope || rows[0].Labels[labelPrefix+"workspace"] != r.Workspace || rows[0].Labels[labelPrefix+"revision"] != r.Revision || rows[0].Labels[labelPrefix+"provenance"] != r.Provenance {
			return ErrDenied
		}
		if _, err := c.docker.call(ctx, nil, "volume", "rm", r.Volume); err != nil {
			return err
		}
	}
	b, err = c.docker.call(ctx, nil, "volume", "ls", "-q", "--filter", "name=^"+r.Volume+"$")
	if err != nil || len(strings.Fields(string(b))) != 0 {
		return errors.New("snapshot volume removal unconfirmed")
	}
	r.State = "retired"
	return c.save(r)
}

func (c *FrozenNativeCatalog) ReconcileNativeInputs(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		r, err := c.load(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return err
		}
		// Manager reconciliation has already removed consumers. Never remove an
		// unexpected alias or infer absence from an unavailable Docker daemon.
		if err := c.release(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

var _ NativeInputCatalog = (*FrozenNativeCatalog)(nil)

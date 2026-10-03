package devcontainer

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/dockerutil"
)

// MiseResolution is a proposed dependency input, not an installed environment.
// The caller must explicitly apply Lock through the normal build path.
type MiseResolution struct {
	ImageID     string          `json:"resolver_image_id"`
	Platform    string          `json:"platform"`
	MiseVersion string          `json:"mise_version"`
	Lock        *MiseLockBundle `json:"lock"`
}

const resolverDir = "/tmp/crewship-resolver"

var miseResolverVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)

// ResolveMiseLock performs trusted build work with registry network access.
// It runs no image entrypoint, passes no environment credentials or host files,
// and never alters a live agent or a crew's selected revision. The caller must
// trust the pinned local image, its mise/plugins and the selected tool sources.
func ResolveMiseLock(ctx context.Context, docker *client.Client, imageID string, cfg *MiseConfig, bump bool) (result *MiseResolution, err error) {
	if docker == nil || !dockerutil.IsLocalImageID(imageID) || cfg == nil || len(cfg.Tools) == 0 {
		return nil, errors.New("mise resolve: a local immutable image and tools are required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	// Environment values do not participate in dependency resolution.
	clean := &MiseConfig{Tools: cfg.Tools, Lock: cfg.Lock}
	input, err := resolverInputArchive(clean)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	inspected, err := docker.ImageInspect(ctx, imageID)
	if err != nil {
		return nil, fmt.Errorf("mise resolve: inspect image: %w", err)
	}
	platform := map[string]string{"amd64": "linux-x64", "arm64": "linux-arm64"}[inspected.Architecture]
	if inspected.ID != imageID || inspected.Os != "linux" || platform == "" || len(inspected.Config.Volumes) != 0 {
		return nil, errors.New("mise resolve: unsupported image identity, platform or declared volumes")
	}
	env := []string{}
	for _, kv := range inspected.Config.Env {
		if key, _, ok := strings.Cut(kv, "="); ok {
			env = append(env, key+"=")
		}
	}
	env = append(env, "PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/tmp")
	init := true
	pids := int64(128)
	name := "crewship-mise-resolver-" + strings.ToLower(rand.Text())
	options := client.ContainerCreateOptions{Name: name,
		Config:     &container.Config{Image: imageID, User: "1002:1002", Entrypoint: []string{"/bin/sleep"}, Cmd: []string{"360"}, Env: env, WorkingDir: "/tmp", Healthcheck: &container.HealthConfig{Test: []string{"NONE"}}, Labels: map[string]string{TempContainerLabelKey: TempContainerLabelValue, "crewship.mise-resolver": name}},
		HostConfig: &container.HostConfig{NetworkMode: "bridge", IpcMode: "private", CgroupnsMode: "private", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges:true"}, Init: &init, Resources: container.Resources{Memory: 512 << 20, MemorySwap: 512 << 20, NanoCPUs: 1e9, PidsLimit: &pids}, Tmpfs: map[string]string{"/tmp": "rw,nosuid,nodev,size=268435456,uid=1001,gid=1001,mode=0700"}},
	}
	created, err := docker.ContainerCreate(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("mise resolve: create builder: %w", err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, cleanupErr := docker.ContainerRemove(cleanup, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); cleanupErr != nil {
			result = nil
			err = errors.Join(err, fmt.Errorf("mise resolve: remove builder %s: %w", created.ID, cleanupErr))
		}
	}()
	actual, err := docker.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("mise resolve: inspect builder: %w", err)
	}
	if err = auditMiseResolver(actual.Container, imageID, name); err != nil {
		return nil, err
	}
	if _, err = docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return nil, fmt.Errorf("mise resolve: start builder: %w", err)
	}
	if _, err = resolverExecWithInput(ctx, docker, created.ID, []string{"/bin/tar", "-xf", "-", "-C", "/tmp"}, 1024, input); err != nil {
		return nil, fmt.Errorf("mise resolve: stage input: %w", err)
	}

	prefix := []string{"/usr/bin/env", "-i", "PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + resolverDir, "MISE_CONFIG_DIR=" + resolverDir + "/config", "MISE_GLOBAL_CONFIG_FILE=" + resolverDir + "/config/config.toml", "MISE_DATA_DIR=" + resolverDir + "/data", "MISE_CACHE_DIR=" + resolverDir + "/cache", "MISE_STATE_DIR=" + resolverDir + "/state", "MISE_YES=1", "/usr/local/bin/mise"}
	version, err := resolverExec(ctx, docker, created.ID, append(slices.Clone(prefix), "--version"), 1024)
	if err != nil {
		return nil, fmt.Errorf("mise resolve: version probe: %w", err)
	}
	fields := strings.Fields(version)
	if len(fields) == 0 || !miseResolverVersion.MatchString(fields[0]) {
		return nil, errors.New("mise resolve: unsupported version output")
	}
	command := append(slices.Clone(prefix), "lock", "--global", "--platform", platform, "--json")
	if bump {
		command = append(command, "--bump")
	}
	if _, err = resolverExec(ctx, docker, created.ID, command, 64<<10); err != nil {
		return nil, fmt.Errorf("mise resolve: lock operation: %w", err)
	}
	archive, err := resolverExec(ctx, docker, created.ID, []string{"/bin/tar", "-cf", "-", "-C", resolverDir, "config"}, 1<<20)
	if err != nil {
		return nil, fmt.Errorf("mise resolve: read lock: %w", err)
	}
	lock, err := readResolverLockArchive(strings.NewReader(archive))
	if err != nil {
		return nil, err
	}
	return &MiseResolution{ImageID: imageID, Platform: platform, MiseVersion: fields[0], Lock: lock}, nil
}

func auditMiseResolver(c container.InspectResponse, imageID, name string) error {
	h := c.HostConfig
	if c.Config == nil || h == nil || c.Image != imageID || c.Config.User != "1002:1002" || c.Config.Labels["crewship.mise-resolver"] != name || !slices.Equal(c.Config.Entrypoint, []string{"/bin/sleep"}) || !slices.Equal(c.Config.Cmd, []string{"360"}) || c.Config.Healthcheck == nil || !slices.Equal(c.Config.Healthcheck.Test, []string{"NONE"}) || h.Privileged || !h.ReadonlyRootfs || h.NetworkMode != "bridge" || h.PidMode != "" || h.IpcMode != "private" || h.UTSMode != "" || h.CgroupnsMode != "private" || len(h.CapAdd) != 0 || !slices.Contains(h.CapDrop, "ALL") || !slices.Contains(h.SecurityOpt, "no-new-privileges:true") || len(h.Binds) != 0 || len(h.Mounts) != 0 || len(c.Mounts) != 0 || len(h.VolumesFrom) != 0 || len(h.Devices) != 0 || len(h.DeviceRequests) != 0 || h.Memory != 512<<20 || h.MemorySwap != 512<<20 || h.NanoCPUs != 1e9 || h.PidsLimit == nil || *h.PidsLimit != 128 || len(h.Tmpfs) != 1 || h.Tmpfs["/tmp"] != "rw,nosuid,nodev,size=268435456,uid=1001,gid=1001,mode=0700" {
		return errors.New("mise resolve: builder configuration rejected")
	}
	return nil
}

func resolverInputArchive(cfg *MiseConfig) ([]byte, error) {
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	files := map[string]string{"config.toml": (&MiseConfig{Tools: cfg.Tools}).ToTOML()}
	if cfg.Lock != nil {
		for n, s := range cfg.Lock.Files {
			files[n] = s
		}
	}
	dirs := map[string]bool{"crewship-resolver": true, "crewship-resolver/config": true}
	for n := range files {
		for parent := path.Dir("crewship-resolver/config/" + n); parent != "."; parent = path.Dir(parent) {
			dirs[parent] = true
		}
	}
	names := make([]string, 0, len(dirs))
	for n := range dirs {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		if err := tw.WriteHeader(&tar.Header{Name: n + "/", Typeflag: tar.TypeDir, Mode: 0700, Uid: 1001, Gid: 1001}); err != nil {
			return nil, err
		}
	}
	names = names[:0]
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		s := files[n]
		if err := tw.WriteHeader(&tar.Header{Name: "crewship-resolver/config/" + n, Typeflag: tar.TypeReg, Mode: 0600, Uid: 1001, Gid: 1001, Size: int64(len(s))}); err != nil {
			return nil, err
		}
		if _, err := io.WriteString(tw, s); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func readResolverLockArchive(r io.Reader) (*MiseLockBundle, error) {
	limited := &io.LimitedReader{R: r, N: 1 << 20}
	tr := tar.NewReader(limited)
	b := &MiseLockBundle{SchemaVersion: 1, Files: map[string]string{}}
	seen := map[string]bool{}
	count := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("mise resolve: invalid lock archive")
		}
		count++
		name := strings.TrimSuffix(h.Name, "/")
		if count > 512 || limited.N <= 0 || name != path.Clean(name) || (name != "config" && !strings.HasPrefix(name, "config/")) || len(name) > 270 || seen[name] {
			return nil, errors.New("mise resolve: invalid archive path or limit")
		}
		seen[name] = true
		if h.Typeflag == tar.TypeDir {
			continue
		}
		rel := strings.TrimPrefix(name, "config/")
		if h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > maxMiseLockFileBytes {
			return nil, errors.New("mise resolve: unsupported archive entry")
		}
		if rel == "config.toml" {
			continue
		}
		if !validMiseLockPath(rel) {
			return nil, errors.New("mise resolve: unexpected resolver output")
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		b.Files[rel] = string(data)
	}
	if limited.N <= 0 {
		return nil, errors.New("mise resolve: archive exceeds limit")
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return b, nil
}

type resolverOutput struct {
	bytes.Buffer
	limit int
}

func (b *resolverOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, errors.New("output limit exceeded")
	}
	return b.Buffer.Write(p)
}
func resolverExec(ctx context.Context, docker *client.Client, id string, cmd []string, limit int) (string, error) {
	return resolverExecWithInput(ctx, docker, id, cmd, limit, nil)
}
func resolverExecWithInput(ctx context.Context, docker *client.Client, id string, cmd []string, limit int, input []byte) (string, error) {
	created, err := docker.ExecCreate(ctx, id, client.ExecCreateOptions{Cmd: cmd, User: "1001:1001", WorkingDir: "/tmp", AttachStdin: input != nil, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return "", err
	}
	attached, err := docker.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return "", err
	}
	defer attached.Close()
	var writeDone chan error
	if input != nil {
		writeDone = make(chan error, 1)
		go func() {
			_, e := io.Copy(attached.Conn, bytes.NewReader(input))
			if closeErr := attached.CloseWrite(); e == nil {
				e = closeErr
			}
			writeDone <- e
		}()
		defer func() {
			attached.Close()
			if writeDone != nil {
				<-writeDone
			}
		}()
	}
	out := &resolverOutput{limit: limit}
	done := make(chan error, 1)
	go func() { _, e := stdcopy.StdCopy(out, out, attached.Reader); done <- e }()
	select {
	case <-ctx.Done():
		attached.Close()
		<-done
		return "", ctx.Err()
	case err = <-done:
		if err != nil {
			return "", errors.New("bounded resolver output failed")
		}
	}
	if writeDone != nil {
		writeErr := <-writeDone
		writeDone = nil
		if writeErr != nil {
			return "", fmt.Errorf("resolver input delivery failed: %w", writeErr)
		}
	}
	state, err := docker.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return "", err
	}
	if state.Running || state.ExitCode != 0 {
		return "", fmt.Errorf("resolver process failed (exit %d)", state.ExitCode)
	}
	return out.String(), nil
}

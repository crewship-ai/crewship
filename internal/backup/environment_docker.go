package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// EnvironmentOps is the Docker access a complete container environment needs
// on top of DockerOps: read the container's configuration, commit it, move
// its image in and out of the daemon, and recreate a container from it.
//
// It is a separate interface rather than more methods on DockerOps so the
// many file-level fakes of DockerOps stay as they are; the collector asks
// for it with a type assertion and reports "environments need a Docker
// client that can save images" when the ops it was given cannot. The one
// production implementation is still MobyDockerOps — this package remains
// the only place the backup flow reaches the daemon.
type EnvironmentOps interface {
	DockerOps

	// InspectContainer reads what a restore needs to recreate the
	// container: its configuration, host settings and mounts.
	InspectContainer(ctx context.Context, containerID string) (ContainerSnapshot, error)

	// CommitContainer writes the container's filesystem (image plus its
	// local changes, volumes and binds excluded) to a new image tagged ref.
	// It never pauses on its own: the caller already holds the pause.
	//
	// env, when non-nil, replaces the environment the image records.
	// Docker bakes the container's whole environment into a committed
	// image's config and merges any override by NAME, so a secret is only
	// kept out by overriding it — the collector passes "NAME=" for every
	// variable it drops.
	CommitContainer(ctx context.Context, containerID, ref string, env []string) (imageID string, err error)

	// ImagePlatform reads os/architecture/variant of a local image.
	ImagePlatform(ctx context.Context, ref string) (Platform, error)

	// SaveImage streams `docker save` of one image.
	SaveImage(ctx context.Context, ref string) (io.ReadCloser, error)

	// LoadImage feeds an image archive to `docker load` and returns the
	// daemon's messages ("Loaded image: …").
	LoadImage(ctx context.Context, archive io.Reader) (string, error)

	// ImageExists reports whether a reference resolves locally.
	ImageExists(ctx context.Context, ref string) (bool, error)

	// TagImage adds ref to a local image.
	TagImage(ctx context.Context, source, ref string) error

	// RemoveImage untags (and, when nothing else uses its layers, deletes)
	// a local image. Never forced.
	RemoveImage(ctx context.Context, ref string) error

	// CreateContainer creates (does not start) a container.
	CreateContainer(ctx context.Context, spec ContainerCreateSpec) (string, error)

	// StartContainer starts a created container.
	StartContainer(ctx context.Context, containerID string) error

	// RemoveContainer force-removes a container, leaving its volumes.
	RemoveContainer(ctx context.Context, containerID string) error

	// RuntimeInfo reads the daemon's version, API version and platform.
	RuntimeInfo(ctx context.Context) (RuntimeInfo, error)
}

// ContainerSnapshot is the subset of `docker inspect` an environment
// records. It is this package's own type so fakes need no moby structs.
type ContainerSnapshot struct {
	ID       string
	Name     string
	Image    string // the reference the container was created from
	ImageID  string
	Running  bool
	Paused   bool
	Platform string // container OS as the daemon reports it

	User         string
	WorkingDir   string
	Hostname     string
	Entrypoint   []string
	Cmd          []string
	Env          []string
	Labels       map[string]string
	ExposedPorts []string
	StopSignal   string
	Tty          bool
	OpenStdin    bool

	NetworkMode    string
	Privileged     bool
	PidMode        string
	IpcMode        string
	UTSMode        string
	UsernsMode     string
	CapAdd         []string
	CapDrop        []string
	SecurityOpt    []string
	Devices        []string // host:container:perms
	DeviceRequests int
	Binds          []string
	PortBindings   []string // container-port -> host-ip:host-port
	ReadonlyRootfs bool
	Tmpfs          map[string]string
	GroupAdd       []string
	ExtraHosts     []string
	Init           *bool
	Runtime        string
	RestartPolicy  string
	Memory         int64
	NanoCPUs       int64
	CPUShares      int64
	PidsLimit      *int64
	ShmSize        int64

	Mounts []MountSnapshot
}

// MountSnapshot is one entry of the container's Mounts.
type MountSnapshot struct {
	Type        string // volume | bind | tmpfs | …
	Name        string // volume name
	Source      string // host path (bind) or volume data dir
	Destination string
	RW          bool
	Driver      string
}

// ContainerCreateSpec is what RestoreEnvironment asks the daemon to create.
type ContainerCreateSpec struct {
	Name   string
	Image  string
	Config EnvironmentConfig
	// Volumes are named volumes to mount: destination -> volume name. An
	// empty name makes an anonymous volume.
	Volumes map[string]string
	// ReadOnly lists destinations mounted read-only.
	ReadOnly map[string]bool
}

// RuntimeInfo is what the daemon says about itself.
type RuntimeInfo struct {
	DockerVersion string `json:"docker_version"`
	APIVersion    string `json:"api_version"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
}

// Platform returns os/arch.
func (r RuntimeInfo) Platform() string {
	if r.OS == "" && r.Arch == "" {
		return ""
	}
	return r.OS + "/" + r.Arch
}

// Compile-time check: the production ops can capture environments.
var _ EnvironmentOps = (*MobyDockerOps)(nil)

// InspectContainer implements EnvironmentOps.
func (m *MobyDockerOps) InspectContainer(ctx context.Context, containerID string) (ContainerSnapshot, error) {
	res, err := m.Client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return ContainerSnapshot{}, fmt.Errorf("backup: docker inspect %s: %w", containerID, err)
	}
	return snapshotFromInspect(res.Container), nil
}

func snapshotFromInspect(c container.InspectResponse) ContainerSnapshot {
	s := ContainerSnapshot{
		ID: c.ID, Name: strings.TrimPrefix(c.Name, "/"), ImageID: c.Image, Platform: c.Platform,
	}
	if c.State != nil {
		s.Running, s.Paused = c.State.Running, c.State.Paused
	}
	if cfg := c.Config; cfg != nil {
		s.Image, s.User, s.WorkingDir, s.Hostname = cfg.Image, cfg.User, cfg.WorkingDir, cfg.Hostname
		s.Entrypoint, s.Cmd, s.Env = cfg.Entrypoint, cfg.Cmd, cfg.Env
		s.Labels, s.StopSignal, s.Tty, s.OpenStdin = cfg.Labels, cfg.StopSignal, cfg.Tty, cfg.OpenStdin
		for p := range cfg.ExposedPorts {
			s.ExposedPorts = append(s.ExposedPorts, p.String())
		}
	}
	if h := c.HostConfig; h != nil {
		s.NetworkMode = string(h.NetworkMode)
		s.Privileged = h.Privileged
		s.PidMode, s.IpcMode, s.UTSMode, s.UsernsMode = string(h.PidMode), string(h.IpcMode), string(h.UTSMode), string(h.UsernsMode)
		s.CapAdd, s.CapDrop, s.SecurityOpt = h.CapAdd, h.CapDrop, h.SecurityOpt
		for _, d := range h.Devices {
			s.Devices = append(s.Devices, d.PathOnHost+":"+d.PathInContainer+":"+d.CgroupPermissions)
		}
		s.DeviceRequests = len(h.DeviceRequests)
		s.Binds = h.Binds
		for p, bs := range h.PortBindings {
			for _, b := range bs {
				host := b.HostPort
				if b.HostIP.IsValid() {
					host = b.HostIP.String() + ":" + host
				}
				s.PortBindings = append(s.PortBindings, p.String()+"->"+host)
			}
		}
		s.ReadonlyRootfs, s.Tmpfs, s.GroupAdd, s.ExtraHosts = h.ReadonlyRootfs, h.Tmpfs, h.GroupAdd, h.ExtraHosts
		s.Init, s.Runtime, s.RestartPolicy = h.Init, h.Runtime, string(h.RestartPolicy.Name)
		s.Memory, s.NanoCPUs, s.CPUShares, s.PidsLimit, s.ShmSize = h.Memory, h.NanoCPUs, h.CPUShares, h.PidsLimit, h.ShmSize
	}
	for _, mp := range c.Mounts {
		s.Mounts = append(s.Mounts, MountSnapshot{
			Type: string(mp.Type), Name: mp.Name, Source: mp.Source, Destination: mp.Destination, RW: mp.RW, Driver: mp.Driver,
		})
	}
	return s
}

// CommitContainer implements EnvironmentOps.
func (m *MobyDockerOps) CommitContainer(ctx context.Context, containerID, ref string, env []string) (string, error) {
	opts := client.ContainerCommitOptions{
		Reference: ref,
		Comment:   "crewship complete environment",
		NoPause:   true,
	}
	if env != nil {
		opts.Config = &container.Config{Env: env}
	}
	res, err := m.Client.ContainerCommit(ctx, containerID, opts)
	if err != nil {
		return "", fmt.Errorf("backup: docker commit %s: %w", containerID, err)
	}
	return res.ID, nil
}

// ImagePlatform implements EnvironmentOps.
func (m *MobyDockerOps) ImagePlatform(ctx context.Context, ref string) (Platform, error) {
	res, err := m.Client.ImageInspect(ctx, ref)
	if err != nil {
		return Platform{}, fmt.Errorf("backup: docker image inspect %s: %w", ref, err)
	}
	return Platform{OS: res.Os, Architecture: res.Architecture, Variant: res.Variant}, nil
}

// ImageExists implements EnvironmentOps.
func (m *MobyDockerOps) ImageExists(ctx context.Context, ref string) (bool, error) {
	_, err := m.Client.ImageInspect(ctx, ref)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "No such image") || strings.Contains(err.Error(), "not found") {
		return false, nil
	}
	return false, fmt.Errorf("backup: docker image inspect %s: %w", ref, err)
}

// SaveImage implements EnvironmentOps.
func (m *MobyDockerOps) SaveImage(ctx context.Context, ref string) (io.ReadCloser, error) {
	rc, err := m.Client.ImageSave(ctx, []string{ref})
	if err != nil {
		return nil, fmt.Errorf("backup: docker save %s: %w", ref, err)
	}
	return rc, nil
}

// LoadImage implements EnvironmentOps. The daemon answers with a JSON
// message stream; an errorDetail in it is a failed load even though the
// HTTP call succeeded.
func (m *MobyDockerOps) LoadImage(ctx context.Context, archive io.Reader) (string, error) {
	rc, err := m.Client.ImageLoad(ctx, archive, client.ImageLoadWithQuiet(true))
	if err != nil {
		return "", fmt.Errorf("backup: docker load: %w", err)
	}
	defer func() { _ = rc.Close() }()
	return readLoadMessages(rc)
}

// readLoadMessages flattens docker load's JSON stream into text and turns
// an error message into an error.
func readLoadMessages(r io.Reader) (string, error) {
	dec := json.NewDecoder(r)
	var out strings.Builder
	for {
		var msg struct {
			Stream      string `json:"stream"`
			Error       string `json:"error"`
			ErrorDetail *struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return out.String(), nil
			}
			return out.String(), fmt.Errorf("backup: docker load: read response: %w", err)
		}
		if msg.ErrorDetail != nil && msg.ErrorDetail.Message != "" {
			return out.String(), fmt.Errorf("backup: docker load: %s", msg.ErrorDetail.Message)
		}
		if msg.Error != "" {
			return out.String(), fmt.Errorf("backup: docker load: %s", msg.Error)
		}
		out.WriteString(msg.Stream)
	}
}

// TagImage implements EnvironmentOps.
func (m *MobyDockerOps) TagImage(ctx context.Context, source, ref string) error {
	if _, err := m.Client.ImageTag(ctx, client.ImageTagOptions{Source: source, Target: ref}); err != nil {
		return fmt.Errorf("backup: docker tag %s %s: %w", source, ref, err)
	}
	return nil
}

// RemoveImage implements EnvironmentOps.
func (m *MobyDockerOps) RemoveImage(ctx context.Context, ref string) error {
	if _, err := m.Client.ImageRemove(ctx, ref, client.ImageRemoveOptions{}); err != nil {
		if strings.Contains(err.Error(), "No such image") {
			return nil
		}
		return fmt.Errorf("backup: docker rmi %s: %w", ref, err)
	}
	return nil
}

// CreateContainer implements EnvironmentOps.
func (m *MobyDockerOps) CreateContainer(ctx context.Context, spec ContainerCreateSpec) (string, error) {
	cfg, host, err := spec.dockerConfig()
	if err != nil {
		return "", err
	}
	res, err := m.Client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: spec.Name, Config: cfg, HostConfig: host,
	})
	if err != nil {
		return "", fmt.Errorf("backup: docker create %s: %w", spec.Name, err)
	}
	return res.ID, nil
}

// dockerConfig turns the sanitized configuration into moby structs. Only
// what EnvironmentConfig carries reaches the daemon — which is the point:
// nothing recorded as unsafe has a field here.
func (spec ContainerCreateSpec) dockerConfig() (*container.Config, *container.HostConfig, error) {
	c := spec.Config
	cfg := &container.Config{
		Image: spec.Image, User: c.User, WorkingDir: c.WorkingDir, Hostname: c.Hostname,
		Entrypoint: c.Entrypoint, Cmd: c.Cmd, Env: c.Env, Labels: c.Labels, StopSignal: c.StopSignal,
		Tty: c.Tty, OpenStdin: c.OpenStdin,
	}
	if len(c.ExposedPorts) > 0 {
		cfg.ExposedPorts = network.PortSet{}
		for _, p := range c.ExposedPorts {
			port, err := network.ParsePort(p)
			if err != nil {
				return nil, nil, fmt.Errorf("backup: exposed port %q: %w", p, err)
			}
			cfg.ExposedPorts[port] = struct{}{}
		}
	}
	host := &container.HostConfig{
		NetworkMode: container.NetworkMode(c.NetworkMode), ReadonlyRootfs: c.ReadonlyRootfs,
		CapDrop: c.CapDrop, SecurityOpt: c.SecurityOpt, Tmpfs: c.Tmpfs, GroupAdd: c.GroupAdd,
		ExtraHosts: c.ExtraHosts, Init: c.Init, Runtime: c.Runtime, ShmSize: c.ShmSize,
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode(c.RestartPolicy)},
	}
	host.Memory, host.NanoCPUs, host.CPUShares, host.PidsLimit = c.Memory, c.NanoCPUs, c.CPUShares, c.PidsLimit
	for dest, name := range spec.Volumes {
		host.Mounts = append(host.Mounts, mount.Mount{Type: mount.TypeVolume, Source: name, Target: dest, ReadOnly: spec.ReadOnly[dest]})
	}
	return cfg, host, nil
}

// StartContainer implements EnvironmentOps.
func (m *MobyDockerOps) StartContainer(ctx context.Context, containerID string) error {
	if _, err := m.Client.ContainerStart(ctx, containerID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("backup: docker start %s: %w", containerID, err)
	}
	return nil
}

// RemoveContainer implements EnvironmentOps.
func (m *MobyDockerOps) RemoveContainer(ctx context.Context, containerID string) error {
	if _, err := m.Client.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true}); err != nil {
		if strings.Contains(err.Error(), "No such container") {
			return nil
		}
		return fmt.Errorf("backup: docker rm %s: %w", containerID, err)
	}
	return nil
}

// RuntimeInfo implements EnvironmentOps.
func (m *MobyDockerOps) RuntimeInfo(ctx context.Context) (RuntimeInfo, error) {
	v, err := m.Client.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return RuntimeInfo{}, fmt.Errorf("backup: docker version: %w", err)
	}
	return RuntimeInfo{DockerVersion: v.Version, APIVersion: v.APIVersion, OS: v.Os, Arch: v.Arch}, nil
}

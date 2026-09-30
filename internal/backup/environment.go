package backup

// Complete container environments (Backups redesign, Track E).
//
// Today's Full level adds /var/lib to the crew's files; a restore still needs
// the original image to exist, and crew images are built locally
// (crewship-cache:*), so a lost host loses them. A complete environment is:
//
//  1. The image content INCLUDING the container's local changes: the
//     container is committed to a temporary image and `docker save`d; every
//     file of that archive (layers, configs, manifests) goes into the
//     environment store once, by sha256, shared between backups.
//  2. Every volume and bind mount: the crew's standard mounts ride the
//     bundle's existing sections; any other mount's content rides
//     environments/<crew>/mounts/<n>/.
//  3. The configuration needed to recreate the container safely (below).
//  4. Architecture and runtime requirements.
//  5. A consistent state: an optional flush hook runs before the pause, and
//     the commit and the extra mounts are taken under the same pause as the
//     crew's files.
//
// What a process held in memory is never captured: a restored environment
// starts its processes fresh.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// EnvironmentFormat versions environments/<crew>.json.
const EnvironmentFormat = 1

// IncompleteEnvironmentFailed: a complete environment was asked for and
// could not be captured for a crew; its files are in the bundle, its
// environment is not.
const IncompleteEnvironmentFailed = "environment_failed"

// Environment is one crew container's complete environment, as recorded in
// the bundle (environments/<crew>.json) and in the store's index.
type Environment struct {
	Format    int       `json:"format"`
	ID        string    `json:"id"`
	Crew      string    `json:"crew"`
	CrewID    string    `json:"crew_id,omitempty"`
	Container string    `json:"container"`
	CreatedAt time.Time `json:"created_at"`
	// Managed: the container is one Crewship created for a crew (label
	// managed-by=crewship). Crewship recreates such a container itself from
	// the restored image, with the runtime settings it owns.
	Managed bool `json:"managed"`

	// SourceImage is the reference the container was created from;
	// SourceImageID its image id. ImageRef is the tag the saved image
	// carries (crewship-env/<crew>:<id>).
	SourceImage   string       `json:"source_image"`
	SourceImageID string       `json:"source_image_id,omitempty"`
	ImageRef      string       `json:"image_ref"`
	Image         ImageArchive `json:"image"`
	// StoreKey is the environment store generation whose key encrypts the
	// image files (environment_crypto.go). Empty for a record written before
	// the store was encrypted: its files are stored as they are.
	StoreKey string `json:"store_key,omitempty"`

	Platform Platform            `json:"platform"`
	Runtime  RuntimeRequirements `json:"runtime"`

	Config EnvironmentConfig  `json:"config"`
	Unsafe []UnsafeSetting    `json:"unsafe"`
	Mounts []EnvironmentMount `json:"mounts"`
	Flush  FlushResult        `json:"flush"`
	Notes  []string           `json:"notes,omitempty"`
}

// ImageArchive lists every entry of the `docker save` archive, in order.
// Regular files are stored by digest in the environment store; the archive
// is reassembled from this list on restore.
type ImageArchive struct {
	Entries []ArchiveEntry `json:"entries"`
	Bytes   int64          `json:"bytes"`
}

// ArchiveEntry is one tar entry of the saved image.
type ArchiveEntry struct {
	Path string `json:"path"`
	Type string `json:"type"` // file | dir | symlink
	// Digest is the file's plaintext SHA-256 and Size its plaintext size.
	Digest string `json:"digest,omitempty"`
	// Object is the SHA-256 of the encrypted object the store keeps for
	// the file — what the store, the reference counts and an off-site copy
	// name it by. Empty in a record written before the store was encrypted
	// (the file is then stored under Digest, as it is).
	Object string `json:"object,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Mode   int64  `json:"mode"`
	Link   string `json:"link,omitempty"`
}

// stored is the name the store keeps the entry's file under.
func (a ArchiveEntry) stored() string {
	if a.Object != "" {
		return a.Object
	}
	return a.Digest
}

// Blobs returns the distinct stored objects the environment needs, sorted.
func (e *Environment) Blobs() []string {
	if e == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, a := range e.Image.Entries {
		if d := a.stored(); d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// BlobBytes is the total plaintext size of the distinct blobs.
func (e *Environment) BlobBytes() int64 {
	seen := map[string]bool{}
	var n int64
	for _, a := range e.Image.Entries {
		if d := a.stored(); d != "" && !seen[d] {
			seen[d] = true
			n += a.Size
		}
	}
	return n
}

// Platform is an image platform.
type Platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
}

func (p Platform) String() string {
	s := p.OS + "/" + p.Architecture
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

// RuntimeRequirements: what the environment was captured on and needs.
type RuntimeRequirements struct {
	DockerVersion string `json:"docker_version,omitempty"`
	APIVersion    string `json:"api_version,omitempty"`
	HostPlatform  string `json:"host_platform,omitempty"`
	// Runtime is the container's OCI runtime ("runc" when unset).
	Runtime string `json:"runtime,omitempty"`
}

// EnvironmentConfig is the part of the container's configuration a restore
// applies. Every field is safe to switch back on by itself; what is not
// lives in Environment.Unsafe instead and has no field here.
type EnvironmentConfig struct {
	User       string            `json:"user,omitempty"`
	WorkingDir string            `json:"working_dir,omitempty"`
	Hostname   string            `json:"hostname,omitempty"`
	Entrypoint []string          `json:"entrypoint,omitempty"`
	Cmd        []string          `json:"cmd,omitempty"`
	Env        []string          `json:"env,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	StopSignal string            `json:"stop_signal,omitempty"`
	Tty        bool              `json:"tty,omitempty"`
	OpenStdin  bool              `json:"open_stdin,omitempty"`
	// ExposedPorts are declared ports; publishing them on the host is a
	// host setting (Unsafe host_port).
	ExposedPorts []string `json:"exposed_ports,omitempty"`
	// DroppedEnv names environment variables left out because they look
	// like secrets. Their values are never recorded.
	DroppedEnv []string `json:"dropped_env,omitempty"`

	NetworkMode    string            `json:"network_mode,omitempty"`
	ReadonlyRootfs bool              `json:"readonly_rootfs,omitempty"`
	CapDrop        []string          `json:"cap_drop,omitempty"`
	SecurityOpt    []string          `json:"security_opt,omitempty"`
	Tmpfs          map[string]string `json:"tmpfs,omitempty"`
	GroupAdd       []string          `json:"group_add,omitempty"`
	ExtraHosts     []string          `json:"extra_hosts,omitempty"`
	Init           *bool             `json:"init,omitempty"`
	Runtime        string            `json:"runtime,omitempty"`
	RestartPolicy  string            `json:"restart_policy,omitempty"`
	Memory         int64             `json:"memory,omitempty"`
	NanoCPUs       int64             `json:"nano_cpus,omitempty"`
	CPUShares      int64             `json:"cpu_shares,omitempty"`
	PidsLimit      *int64            `json:"pids_limit,omitempty"`
	ShmSize        int64             `json:"shm_size,omitempty"`
}

// Unsafe setting kinds.
const (
	UnsafeDockerSocket = "docker_socket"
	UnsafePrivileged   = "privileged"
	UnsafeHostPID      = "host_pid"
	UnsafeHostNetwork  = "host_network"
	UnsafeHostIPC      = "host_ipc"
	UnsafeHostUTS      = "host_uts"
	UnsafeHostUserns   = "host_userns"
	UnsafeCapAdd       = "cap_add"
	UnsafeDevice       = "device"
	UnsafeHostBind     = "host_bind"
	UnsafeHostPort     = "host_port"
	UnsafeSecurityOpt  = "security_opt"
)

// UnsafeSetting is a host-level setting the container had. It is recorded
// and restored switched OFF; an admin turns it back on by hand after review.
type UnsafeSetting struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// Sentence renders the setting for the Recovery page, e.g. "Docker socket
// mount on ops".
func (u UnsafeSetting) Sentence(crew string) string {
	switch u.Kind {
	case UnsafeDockerSocket:
		return "Docker socket mount on " + crew
	case UnsafePrivileged:
		return "privileged mode on " + crew
	case UnsafeHostPID:
		return "host PID namespace on " + crew
	case UnsafeHostNetwork:
		return "host network on " + crew
	case UnsafeHostIPC:
		return "host IPC namespace on " + crew
	case UnsafeHostUTS:
		return "host UTS namespace on " + crew
	case UnsafeHostUserns:
		return "host user namespace on " + crew
	case UnsafeCapAdd:
		return "added capability " + u.Detail + " on " + crew
	case UnsafeDevice:
		return "device " + u.Detail + " on " + crew
	case UnsafeHostBind:
		return "host path " + u.Detail + " on " + crew
	case UnsafeHostPort:
		return "host port " + u.Detail + " on " + crew
	case UnsafeSecurityOpt:
		return "security option " + u.Detail + " on " + crew
	}
	return u.Kind + " " + u.Detail + " on " + crew
}

// Mount restore modes.
const (
	// MountRestoreVolume: the mount comes back as a named volume holding
	// the captured content.
	MountRestoreVolume = "volume"
	// MountRestoreManaged: a bind Crewship supplies itself when it creates
	// a crew container (sidecar binary, entrypoint); never restored from
	// the bundle.
	MountRestoreManaged = "managed"
	// MountRestoreOff: not re-created (a Docker socket); listed as unsafe.
	MountRestoreOff = "off"
	// MountRestoreTmpfs: a tmpfs; recreated empty from the configuration.
	MountRestoreTmpfs = "tmpfs"
)

// EnvironmentMount is one mount of the container and where its content is.
type EnvironmentMount struct {
	Type        string `json:"type"`
	Name        string `json:"name,omitempty"`
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination"`
	RW          bool   `json:"rw"`
	// Data names where the content is in the bundle: "section:<name>" for
	// a crew section the collector already writes (workspace, crew,
	// memory, home, tools), "environments/<crew>/mounts/<n>" for any other
	// mount, "" for none.
	Data    string `json:"data,omitempty"`
	Files   int    `json:"files,omitempty"`
	Restore string `json:"restore"`
}

// FlushResult records the pre-pause flush hook.
type FlushResult struct {
	Ran     bool     `json:"ran"`
	OK      bool     `json:"ok"`
	Command []string `json:"command,omitempty"`
	Detail  string   `json:"detail,omitempty"`
}

// EnvironmentSummary is what the manifest records per captured environment:
// enough for retention, off-site upload and the restore checks to work
// without the key. The configuration, env and unsafe list stay inside the
// encrypted payload.
type EnvironmentSummary struct {
	Crew     string   `json:"crew" yaml:"crew"`
	ID       string   `json:"id" yaml:"id"`
	Platform string   `json:"platform" yaml:"platform"`
	Blobs    []string `json:"blobs" yaml:"blobs"`
	Bytes    int64    `json:"bytes" yaml:"bytes"`
	// Inline: the blobs ride in the payload (environment-blobs/); else they
	// are in the environment store next to the bundle.
	Inline bool `json:"inline,omitempty" yaml:"inline,omitempty"`
}

// Summary builds the manifest entry.
func (e *Environment) Summary(inline bool) EnvironmentSummary {
	return EnvironmentSummary{Crew: e.Crew, ID: e.ID, Platform: e.Platform.String(), Blobs: e.Blobs(), Bytes: e.BlobBytes(), Inline: inline}
}

// BundleEnvironmentBlobs lists every blob digest a bundle needs to restore
// its environments — what an off-site upload must ship alongside the
// bundle when the bundle is not self-contained.
func BundleEnvironmentBlobs(m *Manifest) []string {
	if m == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range m.Contents.Environments {
		for _, d := range s.Blobs {
			if !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Crew containers' standard mounts and the bundle section that carries each.
var standardMountSections = map[string]string{
	ContainerWorkspacePath: SectionCrewWorkspace,
	ContainerCrewPath:      SectionCrewMemory,
	ContainerOutputPath:    SectionCrewOutput,
	ContainerHomePath:      SectionCrewHome,
	ContainerToolsPath:     SectionCrewTools,
}

// managedBindTargets are binds the Docker provider adds to every crew
// container from its runtime directory.
var managedBindTargets = map[string]bool{
	"/usr/local/bin/crewship-sidecar": true,
	"/usr/local/bin/entrypoint.sh":    true,
}

// isManagedContainer: Crewship created it for a crew.
func isManagedContainer(s ContainerSnapshot) bool {
	return s.Labels["managed-by"] == "crewship"
}

func isSocketPath(p string) bool {
	base := path.Base(p)
	return strings.HasSuffix(base, ".sock") && (strings.Contains(base, "docker") || strings.Contains(base, "containerd") || strings.Contains(base, "podman"))
}

// secretEnvName matches environment variable names that carry credentials.
// Crewship itself never puts secrets in a crew container's environment (they
// are written per run to /secrets, a tmpfs, and to the exec environment of
// that run — neither is captured); this catches anything a crew or an
// operator added by hand.
var secretEnvName = regexp.MustCompile(`(?i)(TOKEN|SECRET|PASSWORD|PASSWD|PASSPHRASE|CREDENTIAL|PRIVATE_KEY|API_KEY|APIKEY|ACCESS_KEY|AUTH|COOKIE|SESSION)`)

// SanitizeContainer splits a container's configuration into what a restore
// applies (EnvironmentConfig) and what it records but restores switched off
// (UnsafeSetting). It also classifies every mount.
func SanitizeContainer(s ContainerSnapshot) (EnvironmentConfig, []UnsafeSetting, []EnvironmentMount) {
	cfg := EnvironmentConfig{
		User: s.User, WorkingDir: s.WorkingDir, Entrypoint: s.Entrypoint, Cmd: s.Cmd,
		StopSignal: s.StopSignal, Tty: s.Tty, OpenStdin: s.OpenStdin,
		ReadonlyRootfs: s.ReadonlyRootfs, CapDrop: s.CapDrop, Tmpfs: s.Tmpfs, GroupAdd: s.GroupAdd,
		ExtraHosts: s.ExtraHosts, Init: s.Init, Runtime: s.Runtime, RestartPolicy: s.RestartPolicy,
		Memory: s.Memory, NanoCPUs: s.NanoCPUs, CPUShares: s.CPUShares, PidsLimit: s.PidsLimit, ShmSize: s.ShmSize,
	}
	// A hostname Docker derived from the container id means nothing for a
	// new container; one somebody chose is kept.
	if s.Hostname != "" && !strings.HasPrefix(s.ID, s.Hostname) {
		cfg.Hostname = s.Hostname
	}
	unsafe := []UnsafeSetting{}
	add := func(kind, detail string) { unsafe = append(unsafe, UnsafeSetting{Kind: kind, Detail: detail}) }

	for _, e := range s.Env {
		name, _, _ := strings.Cut(e, "=")
		if secretEnvName.MatchString(name) {
			cfg.DroppedEnv = append(cfg.DroppedEnv, name)
			continue
		}
		cfg.Env = append(cfg.Env, e)
	}
	if len(s.Labels) > 0 {
		cfg.Labels = map[string]string{}
		for k, v := range s.Labels {
			cfg.Labels[k] = v
		}
	}
	cfg.ExposedPorts = append([]string(nil), s.ExposedPorts...)
	sort.Strings(cfg.ExposedPorts)

	if s.Privileged {
		add(UnsafePrivileged, "")
	}
	switch mode := s.NetworkMode; {
	case mode == "host":
		add(UnsafeHostNetwork, "")
		cfg.NetworkMode = "bridge"
	case strings.HasPrefix(mode, "container:"):
		add(UnsafeHostNetwork, mode)
		cfg.NetworkMode = "bridge"
	default:
		cfg.NetworkMode = mode
	}
	if s.PidMode == "host" || strings.HasPrefix(s.PidMode, "container:") {
		add(UnsafeHostPID, s.PidMode)
	}
	if s.IpcMode == "host" || strings.HasPrefix(s.IpcMode, "container:") {
		add(UnsafeHostIPC, s.IpcMode)
	}
	if s.UTSMode == "host" {
		add(UnsafeHostUTS, "")
	}
	if s.UsernsMode == "host" {
		add(UnsafeHostUserns, "")
	}
	for _, c := range s.CapAdd {
		add(UnsafeCapAdd, c)
	}
	for _, d := range s.Devices {
		add(UnsafeDevice, d)
	}
	if s.DeviceRequests > 0 {
		add(UnsafeDevice, fmt.Sprintf("%d device request(s) (GPU)", s.DeviceRequests))
	}
	for _, p := range s.PortBindings {
		add(UnsafeHostPort, p)
	}
	for _, o := range s.SecurityOpt {
		lo := strings.ToLower(o)
		if strings.Contains(lo, "unconfined") || lo == "label=disable" || lo == "label:disable" {
			add(UnsafeSecurityOpt, o)
			continue
		}
		cfg.SecurityOpt = append(cfg.SecurityOpt, o)
	}

	var mounts []EnvironmentMount
	managed := isManagedContainer(s)
	for _, m := range s.Mounts {
		em := EnvironmentMount{Type: m.Type, Name: m.Name, Destination: m.Destination, RW: m.RW}
		switch {
		case m.Type == "tmpfs":
			em.Restore = MountRestoreTmpfs
		case isSocketPath(m.Source) || isSocketPath(m.Destination):
			em.Source, em.Restore = m.Source, MountRestoreOff
			add(UnsafeDockerSocket, m.Source)
		case m.Type == "bind" && managed && (managedBindTargets[m.Destination] || strings.Contains(m.Source, "/.runtime/")):
			em.Source, em.Restore = m.Source, MountRestoreManaged
		case m.Type == "bind":
			// The host path itself is not bound again; its content comes
			// back in a volume at the same place.
			em.Source, em.Restore = m.Source, MountRestoreVolume
			add(UnsafeHostBind, m.Source+" at "+m.Destination)
		default:
			em.Restore = MountRestoreVolume
		}
		mounts = append(mounts, em)
	}
	sort.SliceStable(mounts, func(i, j int) bool { return mounts[i].Destination < mounts[j].Destination })
	return cfg, unsafe, mounts
}

// newEnvironmentID is a sortable, unique id: UTC time plus randomness.
func newEnvironmentID(now time.Time) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return now.UTC().Format("20060102t150405") + "-" + hex.EncodeToString(b[:])
}

var refUnsafe = regexp.MustCompile(`[^a-z0-9._-]+`)

// environmentImageRef is the tag a captured environment's image carries.
func environmentImageRef(crew, id string) string {
	name := strings.Trim(refUnsafe.ReplaceAllString(strings.ToLower(crew), "-"), "-._")
	if name == "" {
		name = "crew"
	}
	return "crewship-env/" + name + ":" + id
}

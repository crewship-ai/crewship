package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup/offsite"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// fakeEnvOps adds the environment methods to fakeDockerOps. Its "image" is
// a docker-save-shaped archive: blobs/sha256/<digest> files plus
// index.json, manifest.json and oci-layout.
type fakeEnvOps struct {
	*fakeDockerOps
	snap      ContainerSnapshot
	platform  Platform
	runtime   RuntimeInfo
	layers    [][]byte // image files, the first one shared by every commit
	images    map[string]bool
	committed []string
	commitEnv [][]string
	removed   []string
	tagged    map[string]string
	loaded    [][]string // entry names of each loaded archive
	created   []ContainerCreateSpec
	copied    map[string][]string // container dest -> file names
	started   []string
	commitErr error
	loadErr   error
	createErr []error
	rtErr     error
}

func newFakeEnvOps() *fakeEnvOps {
	return &fakeEnvOps{
		fakeDockerOps: newFakeDockerOps(),
		platform:      Platform{OS: "linux", Architecture: "amd64"},
		runtime:       RuntimeInfo{DockerVersion: "29.0.0", APIVersion: "1.52", OS: "linux", Arch: "amd64"},
		layers:        [][]byte{[]byte("shared base layer"), []byte("image config")},
		images:        map[string]bool{},
		tagged:        map[string]string{},
		copied:        map[string][]string{},
	}
}

func (f *fakeEnvOps) InspectContainer(context.Context, string) (ContainerSnapshot, error) {
	return f.snap, nil
}

func (f *fakeEnvOps) CommitContainer(_ context.Context, _ string, ref string, env []string) (string, error) {
	if f.commitErr != nil {
		return "", f.commitErr
	}
	f.committed = append(f.committed, ref)
	f.commitEnv = append(f.commitEnv, env)
	f.images[ref] = true
	return "sha256:commit", nil
}

func (f *fakeEnvOps) ImagePlatform(context.Context, string) (Platform, error) { return f.platform, nil }

func (f *fakeEnvOps) ImageExists(_ context.Context, ref string) (bool, error) {
	return f.images[ref], nil
}

// SaveImage emits the shared layers plus one layer unique to the ref (the
// container's changes).
func (f *fakeEnvOps) SaveImage(_ context.Context, ref string) (io.ReadCloser, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "blobs/", Typeflag: tar.TypeDir, Mode: 0o755})
	_ = tw.WriteHeader(&tar.Header{Name: "blobs/sha256/", Typeflag: tar.TypeDir, Mode: 0o755})
	files := append(append([][]byte{}, f.layers...), []byte("changes of "+ref))
	for _, b := range files {
		sum := sha256.Sum256(b)
		_ = tw.WriteHeader(&tar.Header{Name: "blobs/sha256/" + hex.EncodeToString(sum[:]), Typeflag: tar.TypeReg, Mode: 0o444, Size: int64(len(b))})
		_, _ = tw.Write(b)
	}
	idx := []byte(`{"schemaVersion":2}`)
	_ = tw.WriteHeader(&tar.Header{Name: "index.json", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(idx))})
	_, _ = tw.Write(idx)
	_ = tw.WriteHeader(&tar.Header{Name: "latest", Typeflag: tar.TypeSymlink, Linkname: "index.json"})
	_ = tw.Close()
	return io.NopCloser(&buf), nil
}

func (f *fakeEnvOps) LoadImage(_ context.Context, r io.Reader) (string, error) {
	if f.loadErr != nil {
		_, _ = io.Copy(io.Discard, r)
		return "", f.loadErr
	}
	tr := tar.NewReader(r)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if h.Typeflag == tar.TypeReg {
			b, _ := io.ReadAll(tr)
			if strings.HasPrefix(h.Name, "blobs/sha256/") {
				sum := sha256.Sum256(b)
				if hex.EncodeToString(sum[:]) != strings.TrimPrefix(h.Name, "blobs/sha256/") {
					return "", fmt.Errorf("blob %s does not match its content", h.Name)
				}
			}
		}
		names = append(names, h.Name)
	}
	f.loaded = append(f.loaded, names)
	for ref := range f.pendingLoad() {
		f.images[ref] = true
	}
	return "Loaded image", nil
}

// pendingLoad: the fake does not parse refs out of index.json; every ref it
// was ever asked to commit counts as loaded.
func (f *fakeEnvOps) pendingLoad() map[string]bool {
	out := map[string]bool{}
	for _, r := range f.committed {
		out[r] = true
	}
	return out
}

func (f *fakeEnvOps) TagImage(_ context.Context, src, ref string) error {
	f.tagged[ref] = src
	f.images[ref] = true
	return nil
}

func (f *fakeEnvOps) RemoveImage(_ context.Context, ref string) error {
	f.removed = append(f.removed, ref)
	delete(f.images, ref)
	return nil
}

func (f *fakeEnvOps) CreateContainer(_ context.Context, spec ContainerCreateSpec) (string, error) {
	f.created = append(f.created, spec)
	if len(f.createErr) > 0 {
		err := f.createErr[0]
		f.createErr = f.createErr[1:]
		if err != nil {
			return "", err
		}
	}
	return "new-" + spec.Name, nil
}

func (f *fakeEnvOps) StartContainer(_ context.Context, id string) error {
	f.started = append(f.started, id)
	return nil
}

func (f *fakeEnvOps) RemoveContainer(context.Context, string) error { return nil }

func (f *fakeEnvOps) RuntimeInfo(context.Context) (RuntimeInfo, error) {
	return f.runtime, f.rtErr
}

// CopyTo records what lands where (the embedded fake models /workspace only).
func (f *fakeEnvOps) CopyTo(_ context.Context, _ string, dst string, r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		f.copied[dst] = append(f.copied[dst], h.Name)
	}
}

func crewSnapshot() ContainerSnapshot {
	return ContainerSnapshot{
		ID: "abcdef123456", Name: "crewship-team-ops", Image: "crewship-cache:1234", Running: true,
		User: "1001:1001", Entrypoint: []string{"/usr/local/bin/entrypoint.sh"},
		Env:         []string{"PATH=/usr/bin", "OPENAI_API_KEY=sk-live", "TZ=UTC"},
		Labels:      map[string]string{"managed-by": "crewship", "crewship.crew": "ops"},
		NetworkMode: "crewship-agents", CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
		ReadonlyRootfs: true, Tmpfs: map[string]string{"/tmp": "rw"},
		Mounts: []MountSnapshot{
			{Type: "volume", Name: "ws-vol", Destination: "/workspace", RW: true},
			{Type: "volume", Name: "home-vol", Destination: "/home/agent", RW: true},
			{Type: "volume", Name: "db-vol", Destination: "/var/lib/postgresql", RW: true},
			{Type: "bind", Source: "/data/.runtime/crewship-sidecar", Destination: "/usr/local/bin/crewship-sidecar"},
			{Type: "bind", Source: "/srv/data", Destination: "/data", RW: true},
			{Type: "bind", Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock", RW: true},
		},
	}
}

func unsafeKinds(u []UnsafeSetting) []string {
	var out []string
	for _, x := range u {
		out = append(out, x.Kind+":"+x.Detail)
	}
	sort.Strings(out)
	return out
}

func TestSanitizeContainer(t *testing.T) {
	pids := int64(200)
	cases := []struct {
		name       string
		mut        func(*ContainerSnapshot)
		wantUnsafe []string
		check      func(t *testing.T, cfg EnvironmentConfig, mounts []EnvironmentMount)
	}{
		{
			name:       "plain crew keeps its safe settings",
			mut:        func(s *ContainerSnapshot) { s.Mounts = nil; s.PidsLimit = &pids; s.Memory = 1 << 30 },
			wantUnsafe: nil,
			check: func(t *testing.T, cfg EnvironmentConfig, _ []EnvironmentMount) {
				if cfg.NetworkMode != "crewship-agents" || !cfg.ReadonlyRootfs || cfg.Memory != 1<<30 || cfg.PidsLimit == nil || *cfg.PidsLimit != 200 {
					t.Errorf("safe settings lost: %+v", cfg)
				}
				if len(cfg.CapDrop) != 1 || cfg.SecurityOpt[0] != "no-new-privileges" {
					t.Errorf("hardening lost: %+v", cfg)
				}
			},
		},
		{
			name:       "docker socket mount",
			mut:        func(s *ContainerSnapshot) { s.Mounts = s.Mounts[5:] },
			wantUnsafe: []string{"docker_socket:/var/run/docker.sock"},
			check: func(t *testing.T, _ EnvironmentConfig, m []EnvironmentMount) {
				if m[0].Restore != MountRestoreOff {
					t.Errorf("socket restore = %s", m[0].Restore)
				}
			},
		},
		{
			name:       "privileged",
			mut:        func(s *ContainerSnapshot) { s.Mounts = nil; s.Privileged = true },
			wantUnsafe: []string{"privileged:"},
		},
		{
			name:       "added capabilities",
			mut:        func(s *ContainerSnapshot) { s.Mounts = nil; s.CapAdd = []string{"SYS_ADMIN", "NET_ADMIN"} },
			wantUnsafe: []string{"cap_add:NET_ADMIN", "cap_add:SYS_ADMIN"},
		},
		{
			name: "devices and GPU requests",
			mut: func(s *ContainerSnapshot) {
				s.Mounts = nil
				s.Devices = []string{"/dev/fuse:/dev/fuse:rwm"}
				s.DeviceRequests = 1
			},
			wantUnsafe: []string{"device:/dev/fuse:/dev/fuse:rwm", "device:1 device request(s) (GPU)"},
		},
		{
			name:       "host bind comes back as a volume",
			mut:        func(s *ContainerSnapshot) { s.Mounts = s.Mounts[4:5] },
			wantUnsafe: []string{"host_bind:/srv/data at /data"},
			check: func(t *testing.T, _ EnvironmentConfig, m []EnvironmentMount) {
				if m[0].Restore != MountRestoreVolume || m[0].Source != "/srv/data" {
					t.Errorf("bind mount = %+v", m[0])
				}
			},
		},
		{
			name:       "host network falls back to bridge",
			mut:        func(s *ContainerSnapshot) { s.Mounts = nil; s.NetworkMode = "host" },
			wantUnsafe: []string{"host_network:"},
			check: func(t *testing.T, cfg EnvironmentConfig, _ []EnvironmentMount) {
				if cfg.NetworkMode != "bridge" {
					t.Errorf("network = %q", cfg.NetworkMode)
				}
			},
		},
		{
			name: "host namespaces, published ports, unconfined profiles",
			mut: func(s *ContainerSnapshot) {
				s.Mounts = nil
				s.PidMode, s.IpcMode, s.UTSMode, s.UsernsMode = "host", "host", "host", "host"
				s.PortBindings = []string{"8080/tcp->0.0.0.0:80"}
				s.SecurityOpt = []string{"no-new-privileges", "seccomp=unconfined", "apparmor=unconfined"}
			},
			wantUnsafe: []string{"host_ipc:host", "host_pid:host", "host_port:8080/tcp->0.0.0.0:80", "host_userns:", "host_uts:",
				"security_opt:apparmor=unconfined", "security_opt:seccomp=unconfined"},
			check: func(t *testing.T, cfg EnvironmentConfig, _ []EnvironmentMount) {
				if len(cfg.SecurityOpt) != 1 || cfg.SecurityOpt[0] != "no-new-privileges" {
					t.Errorf("security opts = %v", cfg.SecurityOpt)
				}
			},
		},
		{
			name:       "crewship's own binds are managed, standard volumes are volumes",
			mut:        func(s *ContainerSnapshot) { s.Mounts = s.Mounts[:4] },
			wantUnsafe: nil,
			check: func(t *testing.T, _ EnvironmentConfig, m []EnvironmentMount) {
				got := map[string]string{}
				for _, x := range m {
					got[x.Destination] = x.Restore
				}
				want := map[string]string{"/workspace": "volume", "/home/agent": "volume", "/var/lib/postgresql": "volume", "/usr/local/bin/crewship-sidecar": "managed"}
				for k, v := range want {
					if got[k] != v {
						t.Errorf("%s restore = %q, want %q", k, got[k], v)
					}
				}
			},
		},
		{
			name:       "secret-looking env is dropped by name",
			mut:        func(s *ContainerSnapshot) { s.Mounts = nil },
			wantUnsafe: nil,
			check: func(t *testing.T, cfg EnvironmentConfig, _ []EnvironmentMount) {
				if strings.Contains(strings.Join(cfg.Env, ","), "sk-live") {
					t.Errorf("secret value kept: %v", cfg.Env)
				}
				if len(cfg.DroppedEnv) != 1 || cfg.DroppedEnv[0] != "OPENAI_API_KEY" {
					t.Errorf("dropped = %v", cfg.DroppedEnv)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := crewSnapshot()
			tc.mut(&s)
			cfg, unsafe, mounts := SanitizeContainer(s)
			got := unsafeKinds(unsafe)
			want := append([]string(nil), tc.wantUnsafe...)
			sort.Strings(want)
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("unsafe = %v, want %v", got, want)
			}
			if tc.check != nil {
				tc.check(t, cfg, mounts)
			}
		})
	}
}

func TestUnsafeSentences(t *testing.T) {
	env := &Environment{Crew: "ops", Unsafe: []UnsafeSetting{{Kind: UnsafeDockerSocket}, {Kind: UnsafePrivileged}, {Kind: UnsafeHostBind, Detail: "/srv/data at /data"}}}
	got := strings.Join(env.UnsafeSentences(), " · ")
	want := "Docker socket mount on ops · privileged mode on ops · host path /srv/data at /data on ops"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestEnvironmentStorePutVerifiesDigest(t *testing.T) {
	s := &EnvironmentStore{Dir: t.TempDir()}
	d, n, err := s.Put(strings.NewReader("hello"), "")
	if err != nil || n != 5 || !s.Has(d) {
		t.Fatalf("put: %s %d %v", d, n, err)
	}
	if _, _, err := s.Put(strings.NewReader("tampered"), d); !errors.Is(err, ErrBadDigest) {
		t.Errorf("mismatched content accepted: %v", err)
	}
	if _, err := s.BlobPath("sha256:../../etc"); !errors.Is(err, ErrBadDigest) {
		t.Errorf("bad digest accepted: %v", err)
	}
	// Re-putting the same content is a no-op, not an error.
	if d2, _, err := s.Put(strings.NewReader("hello"), d); err != nil || d2 != d {
		t.Errorf("re-put: %s %v", d2, err)
	}
}

func captureWithFake(t *testing.T, ops *fakeEnvOps, store *EnvironmentStore, db *sql.DB, ref string) (*Environment, []byte) {
	t.Helper()
	var buf bytes.Buffer
	tw, err := NewTarZstWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	env, err := CollectEnvironment(context.Background(), ops, CrewTarget{ID: "c1", Slug: "ops", ContainerID: "ctr-ops"}, store,
		EnvironmentOptions{DB: db, PendingRef: ref, Payload: tw, Covered: map[string]bool{SectionCrewWorkspace: true, SectionCrewHome: true}})
	if err != nil {
		t.Fatalf("CollectEnvironment: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return env, buf.Bytes()
}

func TestCollectEnvironmentWithFake(t *testing.T) {
	ops := newFakeEnvOps()
	ops.snap = crewSnapshot()
	ops.snap.Labels[FlushHookLabel] = "redis-cli save"
	ops.otherPaths = map[string]map[string][]byte{
		"/var/lib/postgresql": {"PG_VERSION": []byte("16")},
		"/data":               {"report.csv": []byte("a,b")},
	}
	store := &EnvironmentStore{Dir: t.TempDir()}
	env, payload := captureWithFake(t, ops, store, nil, "")

	if len(ops.committed) != 1 || ops.committed[0] != env.ImageRef || !strings.HasPrefix(env.ImageRef, "crewship-env/ops:") {
		t.Errorf("committed %v, image ref %s", ops.committed, env.ImageRef)
	}
	if len(ops.removed) != 1 || ops.removed[0] != env.ImageRef {
		t.Errorf("temporary image not removed: %v", ops.removed)
	}
	// The dropped secret is overridden in the committed image's env.
	if got := strings.Join(ops.commitEnv[0], ","); !strings.Contains(got, "OPENAI_API_KEY=") || strings.Contains(got, "sk-live") {
		t.Errorf("commit env = %s", got)
	}
	if ops.unpauseCalls != 1 {
		t.Errorf("unpause calls = %d", ops.unpauseCalls)
	}
	if !env.Flush.Ran || strings.Join(env.Flush.Command, " ") != "sh -c redis-cli save" || ops.execAsUsers[0] != "1001:1001" {
		t.Errorf("flush = %+v users %v", env.Flush, ops.execAsUsers)
	}
	if env.Platform.String() != "linux/amd64" || env.Runtime.DockerVersion != "29.0.0" || !env.Managed {
		t.Errorf("platform/runtime/managed: %+v %+v %v", env.Platform, env.Runtime, env.Managed)
	}
	for _, d := range env.Blobs() {
		if !store.Has(d) {
			t.Errorf("store lacks %s", d)
		}
	}
	if _, err := store.ReadIndex(env.ID); err != nil {
		t.Errorf("index: %v", err)
	}
	data := map[string]string{}
	for _, m := range env.Mounts {
		data[m.Destination] = m.Data
	}
	if data["/workspace"] != "section:workspace" || data["/home/agent"] != "section:home" {
		t.Errorf("covered mounts not referenced: %v", data)
	}
	if !strings.HasPrefix(data["/var/lib/postgresql"], "environments/ops/mounts/") || !strings.HasPrefix(data["/data"], "environments/ops/mounts/") {
		t.Errorf("extra mounts not captured: %v", data)
	}
	if data["/var/run/docker.sock"] != "" {
		t.Errorf("socket content captured: %v", data)
	}

	ex, err := ExtractPayload(context.Background(), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	got := ex.EnvironmentBySlug["ops"]
	if got == nil || got.ID != env.ID || len(got.Image.Entries) != len(env.Image.Entries) {
		t.Fatalf("payload record = %+v", got)
	}
	for _, m := range got.Mounts {
		if m.Destination != "/data" {
			continue
		}
		rc, ok, err := ex.OpenEnvironmentMount(context.Background(), "ops", m)
		if err != nil || !ok {
			t.Fatalf("open /data content: %v %v", ok, err)
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		if !bytes.Contains(b, []byte("a,b")) {
			t.Errorf("/data content missing from payload")
		}
	}
}

func TestCollectEnvironmentNeedsEnvironmentOps(t *testing.T) {
	_, err := CollectEnvironment(context.Background(), newFakeDockerOps(), CrewTarget{Slug: "ops", ContainerID: "c"}, &EnvironmentStore{Dir: t.TempDir()}, EnvironmentOptions{})
	if !errors.Is(err, ErrEnvironmentUnsupported) {
		t.Errorf("err = %v", err)
	}
}

func TestCheckEnvironment(t *testing.T) {
	sum := EnvironmentSummary{Crew: "ops", Platform: "linux/arm64/v8", Blobs: []string{"sha256:a", "sha256:b"}, Bytes: 2048}
	all := func(string) bool { return true }
	none := func(string) bool { return false }
	cases := []struct {
		name   string
		host   string
		docker error
		has    func(string) bool
		want   string
	}{
		{"matching arch, layers present", "linux/arm64", nil, all, EnvActionRestore},
		{"aarch64 is arm64", "linux/aarch64", nil, all, EnvActionRestore},
		{"arch mismatch rebuilds", "linux/amd64", nil, all, EnvActionRebuild},
		{"missing layers rebuild", "linux/arm64", nil, none, EnvActionRebuild},
		{"no docker skips", "", errors.New("no socket"), all, EnvActionSkip},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := CheckEnvironment(sum, tc.host, tc.docker, tc.has)
			if c.Action != tc.want {
				t.Errorf("action = %s (%s), want %s", c.Action, c.Detail, tc.want)
			}
		})
	}
}

func TestRestoreEnvironmentPaths(t *testing.T) {
	ctx := context.Background()
	newEnv := func(t *testing.T, managed bool) (*fakeEnvOps, *Environment, *EnvironmentStore) {
		ops := newFakeEnvOps()
		ops.snap = crewSnapshot()
		if !managed {
			delete(ops.snap.Labels, "managed-by")
			ops.snap.Mounts = ops.snap.Mounts[2:3] // one extra volume
			ops.otherPaths = map[string]map[string][]byte{"/var/lib/postgresql": {"PG_VERSION": []byte("16")}}
		}
		store := &EnvironmentStore{Dir: t.TempDir()}
		env, _ := captureWithFake(t, ops, store, nil, "")
		return ops, env, store
	}
	t.Run("architecture mismatch rebuilds without loading", func(t *testing.T) {
		ops, env, store := newEnv(t, true)
		ops.runtime.Arch = "arm64"
		open, has := BlobSources(store)
		out := RestoreEnvironment(ctx, ops, env, EnvironmentRestoreOptions{Open: open, Has: has})
		if out.Result != EnvRebuilt || len(ops.loaded) != 0 {
			t.Errorf("out = %+v, loads %d", out, len(ops.loaded))
		}
	})
	t.Run("missing layers rebuild", func(t *testing.T) {
		ops, env, _ := newEnv(t, true)
		open, has := BlobSources(&EnvironmentStore{Dir: t.TempDir()})
		out := RestoreEnvironment(ctx, ops, env, EnvironmentRestoreOptions{Open: open, Has: has})
		if out.Result != EnvRebuilt || !strings.Contains(out.Reason, "not available") {
			t.Errorf("out = %+v", out)
		}
	})
	t.Run("no docker skips", func(t *testing.T) {
		ops, env, store := newEnv(t, true)
		ops.rtErr = errors.New("daemon down")
		open, has := BlobSources(store)
		out := RestoreEnvironment(ctx, ops, env, EnvironmentRestoreOptions{Open: open, Has: has})
		if out.Result != EnvSkipped {
			t.Errorf("out = %+v", out)
		}
	})
	t.Run("managed crew: image loaded and tagged as the crew image", func(t *testing.T) {
		ops, env, store := newEnv(t, true)
		open, has := BlobSources(store)
		out := RestoreEnvironment(ctx, ops, env, EnvironmentRestoreOptions{Open: open, Has: has, Recreate: true, Name: "x"})
		if out.Result != EnvRestored || len(ops.loaded) != 1 {
			t.Fatalf("out = %+v", out)
		}
		if ops.tagged["crewship-cache:1234"] != env.ImageRef {
			t.Errorf("not tagged as the crew image: %v", ops.tagged)
		}
		if len(ops.created) != 0 {
			t.Errorf("a managed crew's container was created by the restore: %+v", ops.created)
		}
		if len(out.Unsafe) != 2 {
			t.Errorf("unsafe = %v", out.Unsafe)
		}
	})
	t.Run("unmanaged container recreated with content, off network falls back", func(t *testing.T) {
		ops, env, store := newEnv(t, false)
		ops.createErr = []error{errors.New("network crewship-agents not found"), nil}
		var buf bytes.Buffer
		tw, _ := NewTarZstWriter(&buf)
		_ = tw.WriteFile("environments/ops/mounts/0/PG_VERSION", 0o644, time.Now(), []byte("16"))
		_ = tw.Close()
		ex, err := ExtractPayload(ctx, bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		defer ex.Close()
		env.Mounts[0].Data = "environments/ops/mounts/0"
		open, has := BlobSources(store)
		out := RestoreEnvironment(ctx, ops, env, EnvironmentRestoreOptions{
			Open: open, Has: has, Recreate: true, Name: "restored", VolumePrefix: "rv",
			MountData: func(ctx context.Context, m EnvironmentMount) (io.ReadCloser, bool, error) {
				return ex.OpenEnvironmentMount(ctx, "ops", m)
			},
		})
		if out.Result != EnvRestored || out.Container != "new-restored" {
			t.Fatalf("out = %+v", out)
		}
		last := ops.created[len(ops.created)-1]
		if last.Config.NetworkMode != "bridge" || last.Volumes["/var/lib/postgresql"] != "rv-0" {
			t.Errorf("create spec = %+v", last)
		}
		if last.Config.Labels["crewship.restored-environment"] != env.ID {
			t.Errorf("labels = %v", last.Config.Labels)
		}
		if got := ops.copied["/var/lib/postgresql"]; len(got) != 1 || got[0] != "PG_VERSION" {
			t.Errorf("content landed = %v", got)
		}
		if len(ops.started) != 1 {
			t.Errorf("not started")
		}
	})
}

func openEnvDB(t *testing.T) *sql.DB {
	t.Helper()
	return testutil.MigratedSQLDB(t)
}

func refCount(t *testing.T, db *sql.DB, ref string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM bundle_environment_refs WHERE bundle_ref = ?`, ref).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEnvironmentGCKeepsSharedLayers(t *testing.T) {
	ctx := context.Background()
	db := openEnvDB(t)
	dir := t.TempDir()
	store := EnvironmentStoreFor(dir)
	ops := newFakeEnvOps()
	ops.snap = crewSnapshot()
	ops.snap.Mounts = nil

	older, _ := captureWithFake(t, ops, store, db, "pending:a")
	if err := commitEnvironmentRefs(ctx, db, "pending:a", filepath.Join(dir, "old.tar.zst")); err != nil {
		t.Fatal(err)
	}
	newer, _ := captureWithFake(t, ops, store, db, "pending:b")
	if err := commitEnvironmentRefs(ctx, db, "pending:b", filepath.Join(dir, "new.tar.zst")); err != nil {
		t.Fatal(err)
	}
	if refCount(t, db, "pending:a")+refCount(t, db, "pending:b") != 0 {
		t.Fatalf("pending refs survived the commit")
	}
	shared := map[string]bool{}
	for _, d := range older.Blobs() {
		shared[d] = true
	}
	var common, onlyOld []string
	for _, d := range newer.Blobs() {
		if shared[d] {
			common = append(common, d)
		}
	}
	for _, d := range older.Blobs() {
		found := false
		for _, n := range newer.Blobs() {
			found = found || n == d
		}
		if !found {
			onlyOld = append(onlyOld, d)
		}
	}
	if len(common) == 0 || len(onlyOld) == 0 {
		t.Fatalf("fixture wants shared and unique blobs: common %v only-old %v", common, onlyOld)
	}

	// Deleting the older bundle keeps every layer the newer one needs.
	if err := ReleaseEnvironmentRefs(ctx, db, filepath.Join(dir, "old.tar.zst")); err != nil {
		t.Fatal(err)
	}
	gc, err := CollectEnvironmentGarbage(ctx, db, store, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range common {
		if !store.Has(d) {
			t.Errorf("shared layer %s deleted while the newer bundle needs it", d)
		}
	}
	for _, d := range onlyOld {
		if store.Has(d) {
			t.Errorf("layer %s only the deleted bundle needed survived", d)
		}
	}
	if len(gc.Removed) != len(onlyOld) || gc.FreedBytes == 0 {
		t.Errorf("gc = %+v", gc)
	}

	// A capture in progress (pending ref, fresh) protects its blobs too.
	pendingEnv, _ := captureWithFake(t, ops, store, db, "pending:c")
	if err := ReleaseEnvironmentRefs(ctx, db, filepath.Join(dir, "new.tar.zst")); err != nil {
		t.Fatal(err)
	}
	if _, err := CollectEnvironmentGarbage(ctx, db, store, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, d := range pendingEnv.Blobs() {
		if !store.Has(d) {
			t.Errorf("a pending capture's blob %s was collected", d)
		}
	}
	// A day later the pending ref is a dead capture and goes.
	if _, err := CollectEnvironmentGarbage(ctx, db, store, time.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(filepath.Join(store.Dir, "blobs", "sha256", "*"))
	if len(left) != 0 {
		t.Errorf("blobs left after every ref is gone: %d", len(left))
	}
}

func TestEnvironmentGCRemovesOldOrphanFilesOnly(t *testing.T) {
	ctx := context.Background()
	db := openEnvDB(t)
	store := &EnvironmentStore{Dir: t.TempDir()}
	d, _, err := store.Put(strings.NewReader("orphan"), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CollectEnvironmentGarbage(ctx, db, store, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !store.Has(d) {
		t.Fatalf("a fresh unrecorded blob was collected (a capture may be about to record it)")
	}
	p, _ := store.BlobPath(d)
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(p, old, old)
	if _, err := CollectEnvironmentGarbage(ctx, db, store, time.Now()); err != nil {
		t.Fatal(err)
	}
	if store.Has(d) {
		t.Errorf("an old orphan blob survived")
	}
}

func TestAssembleImageArchiveRoundTrip(t *testing.T) {
	ops := newFakeEnvOps()
	ops.snap = crewSnapshot()
	ops.snap.Mounts = nil
	store := &EnvironmentStore{Dir: t.TempDir()}
	env, _ := captureWithFake(t, ops, store, nil, "")
	open, _ := BlobSources(store)
	var buf bytes.Buffer
	if err := AssembleImageArchive(context.Background(), env, &buf, open); err != nil {
		t.Fatal(err)
	}
	orig, _ := ops.SaveImage(context.Background(), env.ImageRef)
	origBytes, _ := io.ReadAll(orig)
	names := func(b []byte) string {
		tr := tar.NewReader(bytes.NewReader(b))
		var out []string
		for {
			h, err := tr.Next()
			if err != nil {
				break
			}
			body, _ := io.ReadAll(tr)
			out = append(out, fmt.Sprintf("%s:%d:%s:%x", h.Name, h.Typeflag, h.Linkname, sha256.Sum256(body)))
		}
		return strings.Join(out, "\n")
	}
	if names(buf.Bytes()) != names(origBytes) {
		t.Errorf("reassembled archive differs:\n%s\n---\n%s", names(buf.Bytes()), names(origBytes))
	}
}

func TestEvaluateRestoreChecksEnvironments(t *testing.T) {
	m := &Manifest{FormatVersion: FormatVersion, Scope: ScopeWorkspace, SourceInstance: Instance{Platform: "linux/amd64"},
		Contents: Contents{
			Workspace: &WorkspaceSummary{Slug: "acme"},
			Crews:     []CrewSummary{{Slug: "ops"}, {Slug: "researcher"}, {Slug: "plain"}},
			Environments: []EnvironmentSummary{
				{Crew: "ops", Platform: "linux/amd64", Blobs: []string{"sha256:a"}, Bytes: 100},
				{Crew: "researcher", Platform: "linux/arm64", Blobs: []string{"sha256:b"}, Bytes: 50},
			},
		}}
	got := EvaluateRestoreChecks(RestoreCheckInput{
		Manifest: m, Target: TargetReplace, FreeBytes: 1 << 30, HostPlatform: "linux/amd64", DockerVersion: "29.3.0",
		UnsafeChecked: true, HasBlob: func(string) bool { return true },
	})
	if len(got.Environments) != 2 || got.Environments[0].Action != EnvActionRestore || got.Environments[1].Action != EnvActionRebuild {
		t.Fatalf("environments = %+v", got.Environments)
	}
	if got.Runtime.Detail != "Docker 29.3.0, linux/amd64 for 2 environment(s)" {
		t.Errorf("runtime detail = %q", got.Runtime.Detail)
	}
	if len(got.Runtime.Warnings) != 1 || !strings.HasPrefix(got.Runtime.Warnings[0], "researcher: built for linux/arm64") {
		t.Errorf("warnings = %v", got.Runtime.Warnings)
	}
	if got.Space.NeedBytes < 150 {
		t.Errorf("layer bytes not counted in the space need: %d", got.Space.NeedBytes)
	}
	none := EvaluateRestoreChecks(RestoreCheckInput{Manifest: &Manifest{FormatVersion: FormatVersion, Scope: ScopeWorkspace}, Target: TargetNewWorkspace})
	if none.Environments == nil {
		t.Errorf("environments must be an array, not null")
	}
}

func TestScanUnsafeSettingsReadsEnvironmentRecords(t *testing.T) {
	env := &Environment{Crew: "ops", Unsafe: []UnsafeSetting{{Kind: UnsafeDockerSocket, Detail: "/var/run/docker.sock"}, {Kind: UnsafePrivileged}}}
	b, _ := jsonMarshal(env)
	path := writeTestBundle(t, map[string][]byte{"environments/ops.json": b})
	got, err := ScanUnsafeSettings(context.Background(), path, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "Docker socket mount on ops|privileged mode on ops" {
		t.Errorf("got %v", got)
	}
}

// A recovered server lands what recover staged: records and inline layers
// from the workspace's environments archive.
func TestLandStagedEnvironments(t *testing.T) {
	ctx := context.Background()
	ops := newFakeEnvOps()
	ops.snap = crewSnapshot()
	ops.snap.Mounts = nil
	store := &EnvironmentStore{Dir: t.TempDir()}
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, RecoveredEnvironmentsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "ws1.tar.zst"))
	if err != nil {
		t.Fatal(err)
	}
	tw, _ := NewTarZstWriter(f)
	env, err := CollectEnvironment(ctx, ops, CrewTarget{ID: "c1", Slug: "ops", ContainerID: "ctr-ops"}, store, EnvironmentOptions{Payload: tw, Inline: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = f.Close()

	// The capture's own store is gone: only the inline layers remain.
	rep, err := LandStagedEnvironments(ctx, ops, LandOptions{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Staged || len(rep.Environments) != 1 {
		t.Fatalf("report = %+v", rep)
	}
	got := rep.Environments[0]
	if got.Workspace != "ws1" || got.Result != EnvRestored || got.Image != env.ImageRef {
		t.Fatalf("landed = %+v", got)
	}
	if ops.tagged["crewship-cache:1234"] != env.ImageRef {
		t.Errorf("managed crew image not tagged back: %v", ops.tagged)
	}
	if _, err := os.Stat(filepath.Join(dir, "landed.json")); err != nil {
		t.Errorf("no landed.json: %v", err)
	}
}

var jsonMarshal = json.Marshal

// The off-site layer ships blobs straight out of the environment store.
var _ offsite.LocalBlobs = (*EnvironmentStore)(nil)

// writeTestBundle writes an unencrypted workspace bundle whose payload holds
// files.
func writeTestBundle(t *testing.T, files map[string][]byte) string {
	t.Helper()
	var payload bytes.Buffer
	tw, err := NewTarZstWriter(&payload)
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range files {
		if err := tw.WriteFile(name, 0o600, time.Now(), b); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	var sealed bytes.Buffer
	sha, size, err := SealPayload(&sealed, bytes.NewReader(payload.Bytes()), WriteBundleOptions{NoEncrypt: true})
	if err != nil {
		t.Fatal(err)
	}
	m := minValidManifest(ScopeWorkspace, "cafe")
	m.Checksums.PayloadSHA256 = sha
	m.Encryption = Encryption{Enabled: false}
	path := filepath.Join(t.TempDir(), "bundle.tar.zst")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteBundleStream(f, m, bytes.NewReader(sealed.Bytes()), size); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBundleEnvironmentBlobs(t *testing.T) {
	m := &Manifest{Contents: Contents{Environments: []EnvironmentSummary{
		{Crew: "a", Blobs: []string{"sha256:2", "sha256:1"}},
		{Crew: "b", Blobs: []string{"sha256:1", "sha256:3"}},
	}}}
	if got := strings.Join(BundleEnvironmentBlobs(m), ","); got != "sha256:1,sha256:2,sha256:3" {
		t.Errorf("got %s", got)
	}
}

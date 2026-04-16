package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// These tests pin down the public API surface of the provider package and
// document the expected zero-value semantics for the data types declared in
// container.go, state.go, and storage.go. They run with no Docker daemon
// and no filesystem side effects.

// --- Compile-time interface conformance via mocks --------------------------

// mockContainerProvider implements the full ContainerProvider +
// HostAddressProvider + VolumeManager + InteractiveExecProvider surface.
// CLAUDE.md: "When adding a method to an interface (ContainerProvider, etc.)
// — update ALL mock types in test files." This mock acts as a canary: if
// the interface gains a method, this file will not compile.
type mockContainerProvider struct {
	containerName string
	hostAddr      string
	ensureErr     error
	statusState   string
	statsRet      *provider.ContainerMetrics
	execInspectOK bool
}

func (m *mockContainerProvider) EnsureCrewRuntime(_ context.Context, t provider.CrewConfig) (string, error) {
	if m.ensureErr != nil {
		return "", m.ensureErr
	}
	return "id-" + t.Slug, nil
}
func (m *mockContainerProvider) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (m *mockContainerProvider) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (m *mockContainerProvider) ContainerStatus(_ context.Context, id string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{ID: id, State: m.statusState}, nil
}
func (m *mockContainerProvider) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return m.statsRet, nil
}
func (m *mockContainerProvider) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	return &provider.ExecResult{ExecID: "exec-" + cfg.ContainerID, Reader: io.NopCloser(strings.NewReader(""))}, nil
}
func (m *mockContainerProvider) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return m.execInspectOK, 0, nil
}
func (m *mockContainerProvider) CrewContainerName(slug string) string {
	if m.containerName != "" {
		return m.containerName
	}
	return "crewship-team-" + slug
}
func (m *mockContainerProvider) CopyToContainer(_ context.Context, _, _ string, _ io.Reader) error {
	return nil
}
func (m *mockContainerProvider) HostAddress() string { return m.hostAddr }
func (m *mockContainerProvider) RemoveCrewVolumes(_ context.Context, _ string) error {
	return nil
}
func (m *mockContainerProvider) ExecInteractive(_ context.Context, _ provider.InteractiveExecConfig) (*provider.InteractiveExecResult, error) {
	return &provider.InteractiveExecResult{ExecID: "i-1"}, nil
}
func (m *mockContainerProvider) ExecResize(_ context.Context, _ string, _, _ uint16) error {
	return nil
}

// Compile-time interface assertions — single source of truth for the
// surface every provider must keep implementing.
var (
	_ provider.ContainerProvider       = (*mockContainerProvider)(nil)
	_ provider.HostAddressProvider     = (*mockContainerProvider)(nil)
	_ provider.VolumeManager           = (*mockContainerProvider)(nil)
	_ provider.InteractiveExecProvider = (*mockContainerProvider)(nil)
)

func TestMockContainerProvider_BasicLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := &mockContainerProvider{statusState: "running", hostAddr: "host.docker.internal"}

	id, err := m.EnsureCrewRuntime(ctx, provider.CrewConfig{ID: "c1", Slug: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "id-alpha" {
		t.Errorf("got id %q", id)
	}

	st, err := m.ContainerStatus(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != "running" {
		t.Errorf("state = %q, want running", st.State)
	}
	if st.ID != id {
		t.Errorf("status ID mismatch")
	}
	if name := m.CrewContainerName("alpha"); name != "crewship-team-alpha" {
		t.Errorf("name = %q", name)
	}
	if got := m.HostAddress(); got != "host.docker.internal" {
		t.Errorf("hostAddress = %q", got)
	}
}

func TestMockContainerProvider_PropagatesEnsureError(t *testing.T) {
	t.Parallel()
	want := errors.New("boom")
	m := &mockContainerProvider{ensureErr: want}
	_, err := m.EnsureCrewRuntime(context.Background(), provider.CrewConfig{Slug: "x"})
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}

// --- StateProvider mock + interface conformance ----------------------------

type mockStateProvider struct {
	store  map[string]map[string][]byte
	closed bool
}

func newMockState() *mockStateProvider {
	return &mockStateProvider{store: map[string]map[string][]byte{}}
}

func (s *mockStateProvider) Get(_ context.Context, bucket, key string) ([]byte, error) {
	if b, ok := s.store[bucket]; ok {
		if v, ok := b[key]; ok {
			return append([]byte(nil), v...), nil
		}
	}
	return nil, nil
}

func (s *mockStateProvider) Set(_ context.Context, bucket, key string, value []byte) error {
	if _, ok := s.store[bucket]; !ok {
		s.store[bucket] = map[string][]byte{}
	}
	s.store[bucket][key] = append([]byte(nil), value...)
	return nil
}

func (s *mockStateProvider) Delete(_ context.Context, bucket, key string) error {
	if b, ok := s.store[bucket]; ok {
		delete(b, key)
	}
	return nil
}

func (s *mockStateProvider) List(_ context.Context, bucket string) (map[string][]byte, error) {
	out := map[string][]byte{}
	for k, v := range s.store[bucket] {
		out[k] = append([]byte(nil), v...)
	}
	return out, nil
}

func (s *mockStateProvider) ListByPrefix(_ context.Context, bucket, prefix string) (map[string][]byte, error) {
	out := map[string][]byte{}
	for k, v := range s.store[bucket] {
		if strings.HasPrefix(k, prefix) {
			out[k] = append([]byte(nil), v...)
		}
	}
	return out, nil
}

func (s *mockStateProvider) Close() error { s.closed = true; return nil }

var _ provider.StateProvider = (*mockStateProvider)(nil)

func TestMockStateProvider_Roundtrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newMockState()

	if err := s.Set(ctx, "runs", "a:1", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	v, err := s.Get(ctx, "runs", "a:1")
	if err != nil {
		t.Fatal(err)
	}
	if string(v) != "hello" {
		t.Errorf("got %q", v)
	}

	// Mutating returned slice must not mutate stored value (copy-on-read).
	v[0] = 'X'
	v2, _ := s.Get(ctx, "runs", "a:1")
	if string(v2) != "hello" {
		t.Errorf("Get returned aliased slice; storage now %q", v2)
	}

	// Prefix filter
	_ = s.Set(ctx, "runs", "a:2", []byte("world"))
	_ = s.Set(ctx, "runs", "b:1", []byte("other"))
	got, err := s.ListByPrefix(ctx, "runs", "a:")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("prefix list len=%d, want 2 (got %v)", len(got), got)
	}

	// Missing bucket returns nil, no error
	missing, err := s.Get(ctx, "ghost", "k")
	if err != nil {
		t.Fatal(err)
	}
	if missing != nil {
		t.Errorf("missing get = %v, want nil", missing)
	}
}

func TestMockStateProvider_Delete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newMockState()
	_ = s.Set(ctx, "b", "k", []byte("v"))
	if err := s.Delete(ctx, "b", "k"); err != nil {
		t.Fatal(err)
	}
	v, _ := s.Get(ctx, "b", "k")
	if v != nil {
		t.Errorf("expected nil after delete, got %v", v)
	}
	// Delete on missing bucket must be a no-op (matches bbolt semantics).
	if err := s.Delete(ctx, "ghost", "k"); err != nil {
		t.Errorf("delete on missing bucket: %v", err)
	}
}

func TestMockStateProvider_Close(t *testing.T) {
	t.Parallel()
	s := newMockState()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !s.closed {
		t.Error("Close() did not flip closed flag")
	}
}

// --- StorageProvider mock + interface conformance --------------------------

type mockStorageProvider struct {
	files map[string][]byte
	dirs  map[string]bool
}

func newMockStorage() *mockStorageProvider {
	return &mockStorageProvider{
		files: map[string][]byte{},
		dirs:  map[string]bool{},
	}
}

func (m *mockStorageProvider) Read(_ context.Context, path string) (io.ReadCloser, error) {
	v, ok := m.files[path]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(strings.NewReader(string(v))), nil
}

func (m *mockStorageProvider) Write(_ context.Context, path string, r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.files[path] = b
	return nil
}

func (m *mockStorageProvider) List(_ context.Context, dir string) ([]provider.FileInfo, error) {
	out := []provider.FileInfo{}
	for p := range m.files {
		if strings.HasPrefix(p, dir+"/") {
			out = append(out, provider.FileInfo{Path: p, Name: p, Size: int64(len(m.files[p]))})
		}
	}
	return out, nil
}

func (m *mockStorageProvider) ListRecursive(ctx context.Context, dir string) ([]provider.FileInfo, error) {
	return m.List(ctx, dir)
}

func (m *mockStorageProvider) Delete(_ context.Context, path string) error {
	delete(m.files, path)
	return nil
}

func (m *mockStorageProvider) Exists(_ context.Context, path string) (bool, error) {
	_, ok := m.files[path]
	return ok, nil
}

func (m *mockStorageProvider) EnsureDir(_ context.Context, path string) error {
	m.dirs[path] = true
	return nil
}

func (m *mockStorageProvider) Watch(_ context.Context, _ string, _ chan<- provider.FileEvent) error {
	return nil
}

var _ provider.StorageProvider = (*mockStorageProvider)(nil)

func TestMockStorageProvider_BasicOps(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newMockStorage()

	if err := s.Write(ctx, "a.txt", strings.NewReader("hi")); err != nil {
		t.Fatal(err)
	}
	r, err := s.Read(ctx, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, _ := io.ReadAll(r)
	if string(got) != "hi" {
		t.Errorf("got %q", got)
	}
	exists, _ := s.Exists(ctx, "a.txt")
	if !exists {
		t.Error("expected file to exist")
	}
	if err := s.Delete(ctx, "a.txt"); err != nil {
		t.Fatal(err)
	}
	exists, _ = s.Exists(ctx, "a.txt")
	if exists {
		t.Error("expected file to be deleted")
	}
}

// --- Struct value semantics ------------------------------------------------

func TestCrewConfig_ZeroValueIsSafe(t *testing.T) {
	t.Parallel()
	var c provider.CrewConfig
	// Documented behavior: zero CrewConfig is the "use provider defaults"
	// signal. Mutating the zero value must not panic.
	c.MemoryMB = 0
	c.CPUs = 0
	c.Slug = ""
	c.ContainerEnv = nil
	if c.MemoryMB != 0 || c.CPUs != 0 || len(c.AllowedDomains) != 0 || len(c.PostStartCommands) != 0 {
		t.Errorf("zero CrewConfig has unexpected non-zero fields: %+v", c)
	}
}

func TestCrewConfig_ContainerEnvIsMap(t *testing.T) {
	t.Parallel()
	c := provider.CrewConfig{ContainerEnv: map[string]string{"FOO": "bar"}}
	if got := c.ContainerEnv["FOO"]; got != "bar" {
		t.Errorf("ContainerEnv[FOO] = %q", got)
	}
}

func TestCrewMount_TypeDefaulting(t *testing.T) {
	t.Parallel()
	// CrewMount struct has no validation; documented contract: empty Type
	// means "bind". We only test that the field is settable.
	m := provider.CrewMount{Source: "/host", Target: "/inside"}
	if m.Type != "" {
		t.Errorf("zero Type should be empty (caller defaults to bind), got %q", m.Type)
	}
	m.Type = "volume"
	if m.Type != "volume" {
		t.Errorf("settable Type expected, got %q", m.Type)
	}
}

func TestExecConfig_ZeroValue(t *testing.T) {
	t.Parallel()
	cfg := provider.ExecConfig{ContainerID: "c1", Cmd: []string{"echo"}}
	if cfg.ContainerID != "c1" {
		t.Error("ContainerID should be set")
	}
	if cfg.User != "" {
		t.Error("zero User should be empty (provider defaults to 1001:1001)")
	}
}

func TestContainerStatus_StateLabels(t *testing.T) {
	t.Parallel()
	// Pin documented vocabulary so providers do not invent new state strings
	// without coordination. Frontend / orchestrator switch on these.
	allowed := map[string]bool{
		"creating": true,
		"running":  true,
		"idle":     true,
		"stopped":  true,
		"error":    true,
	}
	for state := range allowed {
		s := provider.ContainerStatus{ID: "x", State: state}
		if !allowed[s.State] {
			t.Errorf("rejected known state %q", state)
		}
	}
}

func TestContainerMetrics_JSONTags(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := provider.ContainerMetrics{
		CPUPercent: 12.5, MemoryUsed: 1024, MemoryLimit: 2048, MemoryPct: 50,
		NetRx: 100, NetTx: 200, PIDs: 3, Timestamp: now,
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	wantKeys := []string{
		`"cpu_percent"`, `"memory_used_bytes"`, `"memory_limit_bytes"`,
		`"memory_percent"`, `"net_rx_bytes"`, `"net_tx_bytes"`,
		`"pids"`, `"timestamp"`,
	}
	for _, k := range wantKeys {
		if !strings.Contains(s, k) {
			t.Errorf("expected %s in %s", k, s)
		}
	}

	// Roundtrip
	var back provider.ContainerMetrics
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.CPUPercent != m.CPUPercent || back.MemoryUsed != m.MemoryUsed || back.PIDs != m.PIDs {
		t.Errorf("roundtrip mismatch: %+v vs %+v", m, back)
	}
	if !back.Timestamp.Equal(m.Timestamp) {
		t.Errorf("timestamp roundtrip: %v vs %v", back.Timestamp, m.Timestamp)
	}
}

func TestFileInfo_JSONTags(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	fi := provider.FileInfo{
		Path: "agents/eva/notes.md", Name: "notes.md",
		Size: 42, IsDir: false, ModTime: now,
	}
	b, err := json.Marshal(fi)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, k := range []string{`"path"`, `"name"`, `"size"`, `"is_dir"`, `"mod_time"`} {
		if !strings.Contains(s, k) {
			t.Errorf("missing key %s in JSON %s", k, s)
		}
	}
	var back provider.FileInfo
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Path != fi.Path || back.Size != fi.Size || back.IsDir != fi.IsDir {
		t.Errorf("FileInfo roundtrip mismatch: %+v vs %+v", fi, back)
	}
}

func TestFileEvent_OpVocabulary(t *testing.T) {
	t.Parallel()
	// Pin the documented op strings — frontend filters on these.
	for _, op := range []string{"create", "write", "remove", "rename"} {
		ev := provider.FileEvent{Op: op, Path: "x", Agent: "eva", Size: 1, Timestamp: time.Now()}
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		var back provider.FileEvent
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatal(err)
		}
		if back.Op != op {
			t.Errorf("op roundtrip: got %q want %q", back.Op, op)
		}
	}
}

func TestInteractiveExecConfig_DimensionFields(t *testing.T) {
	t.Parallel()
	cfg := provider.InteractiveExecConfig{
		ContainerID: "c1",
		Cmd:         []string{"bash"},
		Rows:        24,
		Cols:        80,
	}
	if cfg.Rows != 24 || cfg.Cols != 80 {
		t.Errorf("dimensions not settable: %+v", cfg)
	}
}

// --- Sanity: interface variables remain assignable -------------------------

func TestInterfaceAssignability(t *testing.T) {
	t.Parallel()
	var (
		_ provider.ContainerProvider       = (*mockContainerProvider)(nil)
		_ provider.StateProvider           = (*mockStateProvider)(nil)
		_ provider.StorageProvider         = (*mockStorageProvider)(nil)
		_ provider.HostAddressProvider     = (*mockContainerProvider)(nil)
		_ provider.VolumeManager           = (*mockContainerProvider)(nil)
		_ provider.InteractiveExecProvider = (*mockContainerProvider)(nil)
	)
}

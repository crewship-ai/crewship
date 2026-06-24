package devcontainer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
)

// --- scripted exec docker client ---------------------------------------------

// covExecResult scripts the outcome of one ContainerExec* round trip.
type covExecResult struct {
	output     string
	exitCode   int
	createErr  error
	attachErr  error
	inspectErr error
}

// covExecClient implements DockerClient with per-call scripted results.
// respond (if set) decides the result for the n-th exec (0-based) given the
// exec options; the default is success with empty output.
type covExecClient struct {
	mu      sync.Mutex
	execs   []container.ExecOptions
	copies  []string
	copyErr error
	respond func(call int, cfg container.ExecOptions) covExecResult
	results map[string]covExecResult
}

func newCovExecClient(respond func(call int, cfg container.ExecOptions) covExecResult) *covExecClient {
	return &covExecClient{respond: respond, results: map[string]covExecResult{}}
}

func (c *covExecClient) ContainerExecCreate(_ context.Context, _ string, cfg container.ExecOptions) (container.ExecCreateResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.execs)
	c.execs = append(c.execs, cfg)
	res := covExecResult{}
	if c.respond != nil {
		res = c.respond(n, cfg)
	}
	if res.createErr != nil {
		return container.ExecCreateResponse{}, res.createErr
	}
	id := fmt.Sprintf("cov-exec-%d", n)
	c.results[id] = res
	return container.ExecCreateResponse{ID: id}, nil
}

func (c *covExecClient) ContainerExecAttach(_ context.Context, execID string, _ container.ExecStartOptions) (types.HijackedResponse, error) {
	c.mu.Lock()
	res := c.results[execID]
	c.mu.Unlock()
	if res.attachErr != nil {
		return types.HijackedResponse{}, res.attachErr
	}
	return types.NewHijackedResponse(newMockConn(res.output), "application/vnd.docker.raw-stream"), nil
}

func (c *covExecClient) ContainerExecInspect(_ context.Context, execID string) (container.ExecInspect, error) {
	c.mu.Lock()
	res := c.results[execID]
	c.mu.Unlock()
	if res.inspectErr != nil {
		return container.ExecInspect{}, res.inspectErr
	}
	return container.ExecInspect{ExitCode: res.exitCode}, nil
}

func (c *covExecClient) CopyToContainer(_ context.Context, _ string, dstPath string, content io.Reader, _ container.CopyToContainerOptions) error {
	if c.copyErr != nil {
		return c.copyErr
	}
	_, _ = io.Copy(io.Discard, content)
	c.mu.Lock()
	c.copies = append(c.copies, dstPath)
	c.mu.Unlock()
	return nil
}

// execCmd flattens the n-th recorded exec command for assertions.
func (c *covExecClient) execCmd(n int) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n >= len(c.execs) {
		return ""
	}
	return strings.Join(c.execs[n].Cmd, " ")
}

// --- commit client with controllable pulls -----------------------------------

// covCommitClient extends the package's mockCommitClient with pull
// counting/failure, which ensureImage tests need.
type covCommitClient struct {
	*mockCommitClient
	pullErr   error
	pullCalls int
}

func (c *covCommitClient) ImagePull(_ context.Context, _ string, _ image.PullOptions) (io.ReadCloser, error) {
	c.pullCalls++
	if c.pullErr != nil {
		return nil, c.pullErr
	}
	return io.NopCloser(strings.NewReader("pull-log")), nil
}

// --- harness -----------------------------------------------------------------

// newCovProvisioner wires a Provisioner with a real Installer/-Downloader over
// scripted fakes. cacheDir feeds the FeatureDownloader (pre-seed with
// covSeedFeature to avoid network).
func newCovProvisioner(commit CommitClient, exec *covExecClient, cacheDir string) *Provisioner {
	return NewProvisioner(commit, NewInstaller(exec, testLogger()),
		NewFeatureDownloader(cacheDir, testLogger()), testLogger())
}

// covSeedFeature places an extracted feature into the downloader cache so
// Download() resolves it without any network access.
func covSeedFeature(t *testing.T, cacheDir, ref, metaJSON string) {
	t.Helper()
	dir := filepath.Join(cacheDir, cacheKey(ref))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "install.sh"), []byte("#!/bin/sh\necho ok"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "devcontainer-feature.json"), []byte(metaJSON), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- aggregateFeatureRequirements ---------------------------------------------

func TestAggregateFeatureRequirements_NilEntriesInitAndPostStart(t *testing.T) {
	t.Parallel()

	p := NewProvisioner(&mockCommitClient{}, nil, nil, testLogger())
	features := []*ResolvedFeature{
		nil, // must be skipped, not panic
		{Metadata: FeatureMetadata{
			ID:               "f1",
			ContainerEnv:     map[string]string{"A": "from-f1", "B": "from-f1"},
			Init:             true,
			PostStartCommand: "f1-start",
		}},
		{Metadata: FeatureMetadata{
			ID:           "f2",
			ContainerEnv: map[string]string{"A": "from-f2"}, // first feature wins
		}},
	}
	req := p.aggregateFeatureRequirements(features, map[string]string{"B": "root"})

	if req.ContainerEnv["A"] != "from-f1" {
		t.Errorf("A = %q, want first feature to win", req.ContainerEnv["A"])
	}
	if req.ContainerEnv["B"] != "root" {
		t.Errorf("B = %q, want root-level override", req.ContainerEnv["B"])
	}
	if !req.Init {
		t.Error("Init flag not propagated")
	}
	if req.Privileged {
		t.Error("Privileged must stay false when no feature requests it")
	}
	if len(req.PostStartCommands) != 1 || req.PostStartCommands[0] != "f1-start" {
		t.Errorf("PostStartCommands = %#v", req.PostStartCommands)
	}
}

// --- ensureImage ---------------------------------------------------------------

// covUnresolvableRef cannot be parsed by go-containerregistry, so the digest
// resolver returns "" instantly without any network traffic.
const covUnresolvableRef = "not a parseable image ref"

func TestEnsureImage_LocalPresentRemoteUnknown_SkipsPull(t *testing.T) {
	t.Parallel()

	mock := &covCommitClient{mockCommitClient: &mockCommitClient{
		existingImages: []string{covUnresolvableRef},
	}}
	p := NewProvisioner(mock, nil, nil, testLogger())
	if err := p.ensureImage(context.Background(), covUnresolvableRef); err != nil {
		t.Fatalf("ensureImage: %v", err)
	}
	if mock.pullCalls != 0 {
		t.Errorf("expected no pull when local copy exists and remote digest unknown, got %d", mock.pullCalls)
	}
}

func TestEnsureImage_NotLocal_Pulls(t *testing.T) {
	t.Parallel()

	mock := &covCommitClient{mockCommitClient: &mockCommitClient{}}
	p := NewProvisioner(mock, nil, nil, testLogger())
	if err := p.ensureImage(context.Background(), covUnresolvableRef); err != nil {
		t.Fatalf("ensureImage: %v", err)
	}
	if mock.pullCalls != 1 {
		t.Errorf("expected exactly one pull, got %d", mock.pullCalls)
	}
}

func TestEnsureImage_PullFailsLocalPresent_ProceedsStale(t *testing.T) {
	t.Parallel()

	// Local copy exists but with a digest, and the remote digest is known and
	// different → stale → re-pull attempted → pull fails → proceed with local.
	host := covStartRegistry(t)
	ref, _ := covPushFeature(t, host, "base/img:1", map[string]string{
		"install.sh":                "x",
		"devcontainer-feature.json": `{"id":"x"}`,
	})

	mock := &covCommitClient{
		mockCommitClient: &mockCommitClient{
			existingImages: []string{ref},
			inspectDigests: map[string][]string{ref: {"base/img@sha256:" + strings.Repeat("0", 64)}},
		},
		pullErr: errors.New("registry quota exceeded"),
	}
	p := NewProvisioner(mock, nil, nil, testLogger())
	if err := p.ensureImage(context.Background(), ref); err != nil {
		t.Fatalf("ensureImage must proceed with stale local copy, got %v", err)
	}
	if mock.pullCalls != 1 {
		t.Errorf("expected a pull attempt, got %d", mock.pullCalls)
	}
}

func TestEnsureImage_PullFailsNotLocal_Errors(t *testing.T) {
	t.Parallel()

	mock := &covCommitClient{
		mockCommitClient: &mockCommitClient{},
		pullErr:          errors.New("no such image upstream"),
	}
	p := NewProvisioner(mock, nil, nil, testLogger())
	err := p.ensureImage(context.Background(), covUnresolvableRef)
	if err == nil || !strings.Contains(err.Error(), "pull image") {
		t.Errorf("expected pull error, got %v", err)
	}
}

func TestEnsureImage_RemoteDigestMatchesLocal_NoPull(t *testing.T) {
	t.Parallel()

	host := covStartRegistry(t)
	ref, digest := covPushFeature(t, host, "base/match:1", map[string]string{
		"install.sh":                "x",
		"devcontainer-feature.json": `{"id":"x"}`,
	})

	mock := &covCommitClient{mockCommitClient: &mockCommitClient{
		existingImages: []string{ref},
		inspectDigests: map[string][]string{ref: {"base/match@" + digest}},
	}}
	p := NewProvisioner(mock, nil, nil, testLogger())
	if err := p.ensureImage(context.Background(), ref); err != nil {
		t.Fatalf("ensureImage: %v", err)
	}
	if mock.pullCalls != 0 {
		t.Errorf("local digest matches remote — no pull expected, got %d", mock.pullCalls)
	}
}

func TestCreateTempContainer_EnsureImageFailure(t *testing.T) {
	t.Parallel()

	mock := &covCommitClient{
		mockCommitClient: &mockCommitClient{},
		pullErr:          errors.New("dead registry"),
	}
	p := NewProvisioner(mock, nil, nil, testLogger())
	_, err := p.createTempContainer(context.Background(), covUnresolvableRef)
	if err == nil || !strings.Contains(err.Error(), "pull base image") {
		t.Errorf("expected pull-base-image error, got %v", err)
	}
	if len(mock.createdContainers) != 0 {
		t.Error("container must not be created when the base image is unavailable")
	}
}

// --- installFeatures -----------------------------------------------------------

// covInstallFeatures composes the split resolve+install pipeline so these
// coverage tests keep exercising the full download → sort → install flow that
// the former installFeatures method provided in one call. Download errors come
// from resolveFeatures, install errors from installResolvedFeatures — matching
// the original error semantics.
func covInstallFeatures(p *Provisioner, ctx context.Context, cid string, cfg *Config, cb func(string)) ([]*ResolvedFeature, error) {
	sorted, opts, err := p.resolveFeatures(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := p.installResolvedFeatures(ctx, cid, sorted, opts, cb); err != nil {
		return nil, err
	}
	return sorted, nil
}

func TestInstallFeatures_NoFeaturesIsNoop(t *testing.T) {
	t.Parallel()

	exec := newCovExecClient(nil)
	p := newCovProvisioner(&mockCommitClient{}, exec, t.TempDir())
	got, err := covInstallFeatures(p, context.Background(), "cid", &Config{Image: "x"}, nil)
	if err != nil || got != nil {
		t.Errorf("expected nil/nil for empty features, got %v, %v", got, err)
	}
	if len(exec.execs) != 0 {
		t.Errorf("no execs expected, got %d", len(exec.execs))
	}
}

func TestInstallFeatures_DependencyOrderAndCallback(t *testing.T) {
	t.Parallel()

	cacheDir := t.TempDir()
	refA := "ghcr.io/t/features/aaa:1"
	refB := "ghcr.io/t/features/bbb:1"
	// aaa declares it installs after bbb, inverting the alphabetical order.
	covSeedFeature(t, cacheDir, refA, `{"id":"aaa","version":"1","installsAfter":["bbb"]}`)
	covSeedFeature(t, cacheDir, refB, `{"id":"bbb","version":"1"}`)

	exec := newCovExecClient(nil)
	p := newCovProvisioner(&mockCommitClient{}, exec, cacheDir)

	cfg := &Config{Image: "x", Features: map[string]map[string]any{
		refA: {"version": "2"},
		refB: nil,
	}}
	var callbackOrder []string
	sorted, err := covInstallFeatures(p, context.Background(), "cid", cfg, func(id string) {
		callbackOrder = append(callbackOrder, id)
	})
	if err != nil {
		t.Fatalf("installFeatures: %v", err)
	}
	if featureIDs(sorted) != "bbb,aaa" {
		t.Errorf("install order = %s, want bbb,aaa", featureIDs(sorted))
	}
	if strings.Join(callbackOrder, ",") != "bbb,aaa" {
		t.Errorf("beforeInstall order = %v, want [bbb aaa]", callbackOrder)
	}
	// Per feature: mkdir, install.sh, rm → 6 execs; 2 tar copies.
	if len(exec.execs) != 6 {
		t.Fatalf("expected 6 execs, got %d", len(exec.execs))
	}
	if len(exec.copies) != 2 {
		t.Errorf("expected 2 CopyToContainer calls, got %d", len(exec.copies))
	}
	if !strings.Contains(exec.execCmd(1), "/tmp/devcontainer-features/bbb/install.sh") {
		t.Errorf("first install exec = %q, want bbb install.sh", exec.execCmd(1))
	}
	if !strings.Contains(exec.execCmd(4), "/tmp/devcontainer-features/aaa/install.sh") {
		t.Errorf("second install exec = %q, want aaa install.sh", exec.execCmd(4))
	}
	// User options are passed as uppercased env vars to aaa's install.sh.
	foundVersion := false
	for _, e := range exec.execs[4].Env {
		if e == "VERSION=2" {
			foundVersion = true
		}
	}
	if !foundVersion {
		t.Errorf("aaa install env missing VERSION=2: %v", exec.execs[4].Env)
	}
}

func TestInstallFeatures_DownloadErrorPropagates(t *testing.T) {
	t.Parallel()

	cacheDir := t.TempDir()
	goodRef := "ghcr.io/t/features/good:1"
	covSeedFeature(t, cacheDir, goodRef, `{"id":"good","version":"1"}`)

	exec := newCovExecClient(nil)
	p := newCovProvisioner(&mockCommitClient{}, exec, cacheDir)
	cfg := &Config{Image: "x", Features: map[string]map[string]any{
		goodRef:               nil,
		"ghcr.io/t/bad ref:1": nil, // unparseable → download fails without network
	}}
	_, err := covInstallFeatures(p, context.Background(), "cid", cfg, nil)
	if err == nil || !strings.Contains(err.Error(), "downloading feature") {
		t.Errorf("expected download error, got %v", err)
	}
}

func TestInstallFeatures_InstallErrorPropagates(t *testing.T) {
	t.Parallel()

	cacheDir := t.TempDir()
	ref := "ghcr.io/t/features/broken:1"
	covSeedFeature(t, cacheDir, ref, `{"id":"broken","version":"1"}`)

	exec := newCovExecClient(func(_ int, cfg container.ExecOptions) covExecResult {
		if strings.Contains(strings.Join(cfg.Cmd, " "), "install.sh") {
			return covExecResult{output: "compile error", exitCode: 1}
		}
		return covExecResult{}
	})
	p := newCovProvisioner(&mockCommitClient{}, exec, cacheDir)
	cfg := &Config{Image: "x", Features: map[string]map[string]any{ref: nil}}
	_, err := covInstallFeatures(p, context.Background(), "cid", cfg, nil)
	if err == nil || !strings.Contains(err.Error(), "installing feature broken") {
		t.Errorf("expected install error for broken, got %v", err)
	}
}

// --- runFeatureLifecycleCommands ------------------------------------------------

func TestRunFeatureLifecycleCommands_RunsAsAgentUser(t *testing.T) {
	t.Parallel()

	exec := newCovExecClient(func(int, container.ExecOptions) covExecResult {
		return covExecResult{output: "hook done"}
	})
	p := newCovProvisioner(&mockCommitClient{}, exec, t.TempDir())
	features := []*ResolvedFeature{
		nil,                                      // skipped
		{Metadata: FeatureMetadata{ID: "quiet"}}, // no hook
		{Metadata: FeatureMetadata{ID: "hooked", PostCreateCommand: "echo hi"}}, // string form
	}
	if err := p.runFeatureLifecycleCommands(context.Background(), "cid", features); err != nil {
		t.Fatalf("runFeatureLifecycleCommands: %v", err)
	}
	if len(exec.execs) != 1 {
		t.Fatalf("expected exactly 1 exec, got %d", len(exec.execs))
	}
	e := exec.execs[0]
	if e.User != "1001:1001" {
		t.Errorf("user = %q, want 1001:1001", e.User)
	}
	if e.Cmd[len(e.Cmd)-1] != "set -e\necho hi" {
		t.Errorf("cmd = %q, want strict-mode wrapped hook", e.Cmd)
	}
	env := strings.Join(e.Env, " ")
	if !strings.Contains(env, "HOME=/home/agent") || !strings.Contains(env, "USER=agent") {
		t.Errorf("env = %v, want agent HOME/USER", e.Env)
	}
}

func TestRunFeatureLifecycleCommands_Failures(t *testing.T) {
	t.Parallel()

	feature := []*ResolvedFeature{{Metadata: FeatureMetadata{ID: "f", PostCreateCommand: "boom"}}}

	// Non-zero exit.
	exec := newCovExecClient(func(int, container.ExecOptions) covExecResult {
		return covExecResult{output: "stack trace", exitCode: 3}
	})
	p := newCovProvisioner(&mockCommitClient{}, exec, t.TempDir())
	err := p.runFeatureLifecycleCommands(context.Background(), "cid", feature)
	if err == nil || !strings.Contains(err.Error(), "postCreateCommand exit 3") {
		t.Errorf("expected exit-3 error, got %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "stack trace") {
		t.Errorf("error should surface the hook output, got %v", err)
	}

	// Transport error.
	exec = newCovExecClient(func(int, container.ExecOptions) covExecResult {
		return covExecResult{createErr: errors.New("daemon gone")}
	})
	p = newCovProvisioner(&mockCommitClient{}, exec, t.TempDir())
	err = p.runFeatureLifecycleCommands(context.Background(), "cid", feature)
	if err == nil || !strings.Contains(err.Error(), "feature f postCreateCommand") {
		t.Errorf("expected wrapped exec error, got %v", err)
	}
}

// --- installMise -----------------------------------------------------------------

func TestInstallMiseMethod_ParseAndValidateErrors(t *testing.T) {
	t.Parallel()

	exec := newCovExecClient(nil)
	p := newCovProvisioner(&mockCommitClient{}, exec, t.TempDir())

	if err := p.installMise(context.Background(), "cid", "[tools\nnope"); err == nil {
		t.Error("expected parse error for malformed TOML")
	}
	if err := p.installMise(context.Background(), "cid", "[tools]\n\"bad!name\" = \"1\""); err == nil {
		t.Error("expected validation error for invalid tool name")
	}
	if len(exec.execs) != 0 {
		t.Errorf("invalid configs must not exec anything, got %d execs", len(exec.execs))
	}
}

func TestInstallMiseMethod_EmptyToolsSkips(t *testing.T) {
	t.Parallel()

	exec := newCovExecClient(nil)
	p := newCovProvisioner(&mockCommitClient{}, exec, t.TempDir())
	if err := p.installMise(context.Background(), "cid", "[tools]\n"); err != nil {
		t.Fatalf("installMise: %v", err)
	}
	if len(exec.execs) != 0 {
		t.Errorf("empty mise config must skip installation, got %d execs", len(exec.execs))
	}
}

func TestInstallMiseMethod_FullPipeline(t *testing.T) {
	t.Parallel()

	exec := newCovExecClient(nil)
	p := newCovProvisioner(&mockCommitClient{}, exec, t.TempDir())
	if err := p.installMise(context.Background(), "cid", "[tools]\nnode = \"22\""); err != nil {
		t.Fatalf("installMise: %v", err)
	}
	// InstallMise (3 calls) + InstallMiseTools (4 calls).
	if len(exec.execs) != 7 {
		t.Fatalf("expected 7 execs, got %d", len(exec.execs))
	}
	if !strings.Contains(exec.execCmd(0), "mise.jdx.dev/install.sh") {
		t.Errorf("exec 0 = %q, want mise installer download", exec.execCmd(0))
	}
	if exec.execs[0].User != "0:0" {
		t.Errorf("mise binary install must run as root, got %q", exec.execs[0].User)
	}
	if !strings.Contains(exec.execCmd(3), `node = "22"`) {
		t.Errorf("exec 3 = %q, want config write containing the tool pin", exec.execCmd(3))
	}
	if exec.execCmd(5) != "mise install --yes" || exec.execs[5].User != "1001:1001" {
		t.Errorf("exec 5 = %q (user %q), want mise install --yes as agent", exec.execCmd(5), exec.execs[5].User)
	}
}

func TestInstallMiseMethod_BinaryInstallFailure(t *testing.T) {
	t.Parallel()

	exec := newCovExecClient(func(call int, _ container.ExecOptions) covExecResult {
		if call == 0 {
			return covExecResult{output: "curl: 404", exitCode: 22}
		}
		return covExecResult{}
	})
	p := newCovProvisioner(&mockCommitClient{}, exec, t.TempDir())
	err := p.installMise(context.Background(), "cid", "[tools]\nnode = \"22\"")
	if !errors.Is(err, ErrMiseInstallFailed) {
		t.Errorf("expected ErrMiseInstallFailed, got %v", err)
	}
}

func TestInstallMiseMethod_ToolsInstallFailure(t *testing.T) {
	t.Parallel()

	exec := newCovExecClient(func(call int, _ container.ExecOptions) covExecResult {
		if call == 3 { // first InstallMiseTools call: write config
			return covExecResult{output: "disk full", exitCode: 1}
		}
		return covExecResult{}
	})
	p := newCovProvisioner(&mockCommitClient{}, exec, t.TempDir())
	err := p.installMise(context.Background(), "cid", "[tools]\nnode = \"22\"")
	if err == nil || !strings.Contains(err.Error(), "write config") {
		t.Errorf("expected write-config error, got %v", err)
	}
}

// --- runPostCreateCommands ---------------------------------------------------------

func TestRunPostCreateCommands_Failures(t *testing.T) {
	t.Parallel()

	cfg := &Config{Image: "x", PostCreateCommand: []string{"make setup"}}

	exec := newCovExecClient(func(int, container.ExecOptions) covExecResult {
		return covExecResult{output: "make: error", exitCode: 2}
	})
	p := newCovProvisioner(&mockCommitClient{}, exec, t.TempDir())
	err := p.runPostCreateCommands(context.Background(), "cid", cfg)
	if err == nil || !strings.Contains(err.Error(), `"make setup" exited with code 2`) {
		t.Errorf("expected exit-code error, got %v", err)
	}

	exec = newCovExecClient(func(int, container.ExecOptions) covExecResult {
		return covExecResult{createErr: errors.New("daemon gone")}
	})
	p = newCovProvisioner(&mockCommitClient{}, exec, t.TempDir())
	err = p.runPostCreateCommands(context.Background(), "cid", cfg)
	if err == nil || !strings.Contains(err.Error(), `postCreateCommand "make setup"`) {
		t.Errorf("expected wrapped exec error, got %v", err)
	}
}

func TestRunPostCreateCommands_RunsAllInOrderWithOutput(t *testing.T) {
	t.Parallel()

	exec := newCovExecClient(func(int, container.ExecOptions) covExecResult {
		return covExecResult{output: "done"}
	})
	p := newCovProvisioner(&mockCommitClient{}, exec, t.TempDir())
	cfg := &Config{Image: "x", PostCreateCommand: []string{"first", "second"}}
	if err := p.runPostCreateCommands(context.Background(), "cid", cfg); err != nil {
		t.Fatalf("runPostCreateCommands: %v", err)
	}
	if len(exec.execs) != 2 {
		t.Fatalf("expected 2 execs, got %d", len(exec.execs))
	}
	if !strings.HasSuffix(exec.execCmd(0), "set -e\nfirst") || !strings.HasSuffix(exec.execCmd(1), "set -e\nsecond") {
		t.Errorf("commands = %q, %q", exec.execCmd(0), exec.execCmd(1))
	}
	if exec.execs[0].User != "1001:1001" {
		t.Errorf("user = %q, want 1001:1001", exec.execs[0].User)
	}
}

// --- writeAggregatedContainerEnv -----------------------------------------------------

func TestWriteAggregatedContainerEnv(t *testing.T) {
	t.Parallel()

	// Empty env: no exec at all.
	exec := newCovExecClient(nil)
	p := newCovProvisioner(&mockCommitClient{}, exec, t.TempDir())
	if err := p.writeAggregatedContainerEnv(context.Background(), "cid", nil); err != nil {
		t.Fatalf("empty env: %v", err)
	}
	if len(exec.execs) != 0 {
		t.Errorf("empty env must not exec, got %d", len(exec.execs))
	}

	// Deterministic, sorted key order in the written content.
	env := map[string]string{"ZED": "26", "ALPHA": "1"}
	if err := p.writeAggregatedContainerEnv(context.Background(), "cid", env); err != nil {
		t.Fatalf("writeAggregatedContainerEnv: %v", err)
	}
	cmd := exec.execCmd(0)
	if !strings.Contains(cmd, `ALPHA=1\nZED=26\n`) {
		t.Errorf("written content not sorted/terminated as expected: %q", cmd)
	}
	if !strings.Contains(cmd, ">> /etc/environment") {
		t.Errorf("must append to /etc/environment: %q", cmd)
	}
	if exec.execs[0].User != "0:0" {
		t.Errorf("env write must run as root, got %q", exec.execs[0].User)
	}
}

func TestWriteAggregatedContainerEnv_Failures(t *testing.T) {
	t.Parallel()

	env := map[string]string{"A": "1"}

	exec := newCovExecClient(func(int, container.ExecOptions) covExecResult {
		return covExecResult{exitCode: 1}
	})
	p := newCovProvisioner(&mockCommitClient{}, exec, t.TempDir())
	err := p.writeAggregatedContainerEnv(context.Background(), "cid", env)
	if err == nil || !strings.Contains(err.Error(), "exit code 1") {
		t.Errorf("expected exit-code error, got %v", err)
	}

	exec = newCovExecClient(func(int, container.ExecOptions) covExecResult {
		return covExecResult{createErr: errors.New("daemon gone")}
	})
	p = newCovProvisioner(&mockCommitClient{}, exec, t.TempDir())
	err = p.writeAggregatedContainerEnv(context.Background(), "cid", env)
	if err == nil || !strings.Contains(err.Error(), "writing containerEnv") {
		t.Errorf("expected wrapped exec error, got %v", err)
	}
}

// --- cleanupCaches ---------------------------------------------------------------------

func TestCleanupCaches(t *testing.T) {
	t.Parallel()

	exec := newCovExecClient(nil)
	p := newCovProvisioner(&mockCommitClient{}, exec, t.TempDir())
	if err := p.cleanupCaches(context.Background(), "cid"); err != nil {
		t.Fatalf("cleanupCaches: %v", err)
	}
	cmd := exec.execCmd(0)
	for _, path := range []string{"/var/cache/apt", "/home/agent/.npm", "chmod 1777 /tmp"} {
		if !strings.Contains(cmd, path) {
			t.Errorf("cleanup script missing %q:\n%s", path, cmd)
		}
	}
}

func TestCleanupCaches_Failures(t *testing.T) {
	t.Parallel()

	exec := newCovExecClient(func(int, container.ExecOptions) covExecResult {
		return covExecResult{exitCode: 9}
	})
	p := newCovProvisioner(&mockCommitClient{}, exec, t.TempDir())
	err := p.cleanupCaches(context.Background(), "cid")
	if err == nil || !strings.Contains(err.Error(), "exited with code 9") {
		t.Errorf("expected exit-code error, got %v", err)
	}

	exec = newCovExecClient(func(int, container.ExecOptions) covExecResult {
		return covExecResult{createErr: errors.New("daemon gone")}
	})
	p = newCovProvisioner(&mockCommitClient{}, exec, t.TempDir())
	if err := p.cleanupCaches(context.Background(), "cid"); err == nil {
		t.Error("expected exec error to propagate")
	}
}

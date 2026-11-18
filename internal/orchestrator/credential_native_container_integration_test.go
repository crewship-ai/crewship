//go:build integration && linux

package orchestrator_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/buildinfo"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	dockerprovider "github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/moby/moby/client"
)

// This test executes standard glab INSIDE a real production crew runtime.
// It qualifies direct token authentication and file bytes/permissions, not model
// CLI shell adapters, Keeper, SSH, certificates or the full lifecycle matrix.
func TestNativeVaultRealCrewContainerGlab(t *testing.T) { testNativeVaultCrewContainer(t, false) }

func TestNativeKeeperRealCrewContainer(t *testing.T) { testNativeVaultCrewContainer(t, true) }

func testNativeVaultCrewContainer(t *testing.T, keeperMode bool) {
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	daemon, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { daemon.Close() })
	imageRef := os.Getenv("CREWSHIP_CODEX_QUALIFICATION_IMAGE")
	if imageRef == "" {
		imageRef = "node:24-bookworm-slim@sha256:ba849c60be29959425b8734d57b8b4b7d56f98edd9504c9af091d5281095a71e"
	}
	image, err := daemon.ImageInspect(ctx, imageRef)
	if err != nil {
		t.Fatalf("requires local pinned Docker fixture; no image pull: %v", err)
	}
	if image.Os != "linux" || image.Architecture != runtime.GOARCH {
		t.Fatal("fixture and native glab must have the same Linux architecture")
	}
	glab, err := exec.LookPath("glab")
	if err != nil {
		t.Fatal("required standard glab missing", err)
	}
	glabFile, err := os.Open(glab)
	if err != nil {
		t.Fatal(err)
	}
	defer glabFile.Close()
	glabInfo, err := glabFile.Stat()
	if err != nil || !glabInfo.Mode().IsRegular() {
		t.Fatal("invalid native glab fixture")
	}
	commitBytes, err := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(string(commitBytes))
	install := t.TempDir()
	sdk := filepath.Join(install, "crewship-agent-cli")
	sidecar := filepath.Join(install, "crewship-sidecar")
	for _, spec := range []struct {
		path, source string
		cli          bool
	}{{sdk, "./cmd/crewship", true}, {sidecar, "./cmd/crewship-sidecar", false}} {
		args := []string{"build", "-trimpath", "-o", spec.path}
		if spec.cli {
			args = append(args, "-tags", "clionly", "-ldflags", "-X main.commit="+commit+" -X main.version=fixture-vault")
		}
		args = append(args, spec.source)
		build := exec.CommandContext(ctx, "go", args...)
		build.Dir = filepath.Join("..", "..")
		build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build native runtime companion: %v %s", err, out)
		}
	}
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("ab", 32))
	db := testutil.MigratedDB(t).DB
	const workspace = "cnativews00000000000001"
	const agent = "cnativeagent00000000001"
	const peer = "cnativepeer000000000001"
	humanToken := "crewship_cli_fixture_" + rand.Text()
	master := "fixture-internal-" + rand.Text()
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	sum := sha256.Sum256([]byte(humanToken))
	seed(`INSERT INTO workspaces(id,name,slug) VALUES(?,'Container fixture','container-fixture')`, workspace)
	seed(`INSERT INTO users(id,email,full_name) VALUES('native-owner','owner@native.invalid','Fixture')`)
	seed(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('native-member',?,'native-owner','OWNER')`, workspace)
	seed(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('native-token','native-owner','fixture',?,datetime('now'))`, hex.EncodeToString(sum[:]))
	seed(`INSERT INTO crews(id,workspace_id,name,slug,network_mode,container_memory_mb,container_cpus,created_at) VALUES('native-crew',?,'Native','native','free',512,1,datetime('now'))`, workspace)
	for _, id := range []string{agent, peer} {
		seed(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,status,cli_adapter,llm_provider,llm_model,tool_profile,timeout_seconds,memory_enabled,created_at) VALUES(?,?,'native-crew',?,?,'AGENT','IDLE','CLAUDE_CODE','ANTHROPIC','claude-sonnet-5-5','CODING',60,0,datetime('now'))`, id, workspace, id, id)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	keeperExecutor := &nativeKeeperRuntimeProxy{}
	options := []api.RouterOption{api.WithInternalToken(master)}
	if keeperMode {
		options = append(options, api.WithKeeperConfig(&config.KeeperConfig{Enabled: true}), api.WithKeeperGatekeeper(gatekeeper.New(nativeCrewKeeperJudge{}, "fixture", logger)), api.WithKeeperSecrets(nativeCrewKeeperEmptyCache{}), api.WithKeeperContainer(keeperExecutor))
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger, options...)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(router)
	if keeperMode {
		listening, err := net.Listen("tcp4", "0.0.0.0:0")
		if err != nil {
			t.Fatal(err)
		}
		server.Listener.Close()
		server.Listener = listening
	}
	server.Start()
	defer server.Close()
	defer router.Shutdown()
	config := filepath.Join(install, "cli.yaml")
	if err := os.WriteFile(config, []byte("server: "+server.URL+"\nworkspace: "+workspace+"\ntoken: "+humanToken+"\nformat: json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cli := func(input string, args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, sdk, args...)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "CREWSHIP_") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("native fixture CLI %q failed: %v", args, err)
		}
		if input != "" && bytes.Contains(out, []byte(input)) {
			t.Fatal("native CLI exposed fixture credential input")
		}
		return out
	}
	resolver := chatbridge.NewIPCResolver(server.URL, master, logger)
	resolve := func(id string) []orchestrator.Credential {
		t.Helper()
		info, err := resolver.ResolveAgent(ctx, id, workspace)
		if err != nil {
			t.Fatal(err)
		}
		return info.Credentials
	}
	toolToken := "fixture-glab-" + rand.Text()
	generic := "fixture-opaque\r\nfinal-LF\n"
	password := "fixture-password-$HOME\n"
	for _, spec := range []struct{ name, typ, slot, value string }{{"NATIVE_CONTAINER_TOKEN", "CLI_TOKEN", "GITLAB_TOKEN", toolToken}, {"NATIVE_CONTAINER_FILE", "GENERIC_SECRET", "NATIVE_CONTAINER_FILE", generic}, {"NATIVE_CONTAINER_LOGIN", "USERPASS", "NATIVE_CONTAINER_LOGIN", password}} {
		args := []string{"credential", "create", "--name", spec.name, "--type", spec.typ, "--provider", "NONE", "--env-var-name", spec.slot, "--security-level", "3", "--value-stdin"}
		if spec.typ == "USERPASS" {
			args = args[:len(args)-1]
			args = append(args, "--username", "fixture-user", "--password-stdin")
		}
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(cli(spec.value, args...), &created); err != nil || created.ID == "" {
			t.Fatal("native create returned no credential identity")
		}
		for _, candidate := range resolve(agent) {
			if candidate.ID == created.ID {
				t.Fatal("unassigned credential resolved")
			}
		}
		cli("", "credential", "assign", created.ID, agent, "--env-var-name", spec.slot, "--ttl", "30m")
	}
	creds := resolve(agent)
	if len(creds) != 3 || len(resolve(peer)) != 0 {
		t.Fatal("native grants did not enforce resolver agent boundary")
	}
	var authenticated atomic.Int32
	var acceptedToken atomic.Value
	acceptedToken.Store(toolToken)
	fixture := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/user" || (r.Header.Get("PRIVATE-TOKEN") != acceptedToken.Load().(string) && r.Header.Get("Authorization") != "Bearer "+acceptedToken.Load().(string)) {
			http.Error(w, "fixture authentication required", 401)
			return
		}
		authenticated.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":1,"username":"fixture-container-user"}`)
	}))
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture.Listener.Close()
	fixture.Listener = listener
	fixture.Start()
	defer fixture.Close()
	prefix := "crewship-vault-container-" + strings.ToLower(rand.Text())
	entrypoint, err := filepath.Abs("../../scripts/entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	requiredCLI := []string{"version", "commands"}
	if keeperMode {
		requiredCLI = append(requiredCLI, "keeper request", "keeper execute", "keeper request-status")
	}
	ctr, err := dockerprovider.New(ctx, dockerprovider.Config{InstanceID: prefix, ContainerPrefix: prefix, OutputBasePath: t.TempDir(), Network: "bridge", RuntimeImage: image.ID, SidecarBinaryPath: sidecar, EntrypointPath: entrypoint, AgentCLIBinaryPath: sdk, RequireAgentCLI: true, ExpectedAgentCLICommit: commit, RequiredAgentCLICommands: requiredCLI}, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctr.Close() })
	keeperExecutor.ContainerProvider = ctr
	crew := provider.CrewConfig{ID: "native-crew", Slug: "native", CachedImage: image.ID}
	if keeperMode {
		crew.ContainerEnv = map[string]string{"GITLAB_HOST": ctr.HostAddress() + fmt.Sprintf(":%d", listener.Addr().(*net.TCPAddr).Port), "GLAB_API_PROTOCOL": "http", "GLAB_CONFIG_DIR": "/home/agent/fixture-keeper-glab", "GLAB_CHECK_UPDATE": "false", "GLAB_SEND_TELEMETRY": "false", "GLAB_NO_PROMPT": "true", "NO_PROXY": "*", "HTTP_PROXY": "", "HTTPS_PROXY": "", "ALL_PROXY": ""}
	}
	// Reserve exact, initially absent fixture identities; never list or prune.
	ownedVolumes := []string{prefix + "-home-" + crew.Slug + "-" + crew.ID, prefix + "-tools-" + crew.Slug + "-" + crew.ID}
	for _, name := range ownedVolumes {
		if _, err := daemon.VolumeInspect(ctx, name, client.VolumeInspectOptions{}); !cerrdefs.IsNotFound(err) {
			t.Fatal("fixture volume identity was not provably absent")
		}
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		found, err := daemon.ContainerInspect(cleanup, ctr.CrewContainerName(crew.ID, crew.Slug), client.ContainerInspectOptions{})
		if err == nil {
			if found.Container.Config == nil || found.Container.Config.Labels[resourcelifecycle.InstanceLabel] != prefix {
				t.Error("refused unowned fixture cleanup")
				return
			}
			if _, err := daemon.ContainerRemove(cleanup, found.Container.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
				t.Error(err)
				return
			}
		} else if !cerrdefs.IsNotFound(err) {
			t.Error("fixture cleanup could not authenticate container absence", err)
			return
		}
		for _, name := range ownedVolumes {
			inspected, err := daemon.VolumeInspect(cleanup, name, client.VolumeInspectOptions{})
			if cerrdefs.IsNotFound(err) {
				continue
			}
			if err != nil || inspected.Volume.Name != name || inspected.Volume.Labels["managed-by"] != "crewship" {
				t.Error("refused unauthenticated fixture volume cleanup")
				continue
			}
			if _, err := daemon.VolumeRemove(cleanup, name, client.VolumeRemoveOptions{}); err != nil {
				t.Error(err)
				continue
			}
			if _, err := daemon.VolumeInspect(cleanup, name, client.VolumeInspectOptions{}); !cerrdefs.IsNotFound(err) {
				t.Error("fixture volume removal not confirmed")
			}
		}
	})
	use, err := ctr.AcquireCrewRuntimeUse(ctx, crew)
	if err != nil {
		t.Fatal(err)
	}
	defer use.Release()
	id := use.ContainerID()
	run := func(command []string, env []string, input io.Reader) []byte {
		t.Helper()
		result, err := ctr.Exec(ctx, provider.ExecConfig{ContainerID: id, User: "1001:1001", Cmd: command, Env: env, WorkingDir: "/", Stdin: input})
		if err != nil {
			t.Fatal(err)
		}
		out, err := readNativeContainerOutput(ctx, result.Reader)
		if err != nil {
			t.Fatal("container output did not complete safely", err)
		}
		code, err := provider.WaitExecExit(ctx, ctr, result.ExecID, time.Second)
		if err != nil || code != 0 {
			t.Fatalf("container command failed (output withheld): code=%d error=%v", code, err)
		}
		return out
	}
	var probe buildinfo.AgentRuntimeProbe
	if err := json.Unmarshal(run([]string{"/usr/local/bin/crewship", "agent-runtime-probe"}, nil, nil), &probe); err != nil {
		t.Fatal("runtime SDK probe did not decode")
	}
	artifact, err := os.Open(sdk)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.New()
	_, hashErr := io.Copy(digest, artifact)
	artifact.Close()
	if hashErr != nil || probe.Protocol != 1 || probe.Commit != commit || probe.OS != "linux" || probe.Arch != runtime.GOARCH || probe.SHA256 != hex.EncodeToString(digest.Sum(nil)) {
		t.Fatal("executing SDK did not match the fixture source artifact")
	}
	// Install only a standard test tool, never a credential helper or credential.
	var archive bytes.Buffer
	tarWriter := tar.NewWriter(&archive)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "fixture-glab", Size: glabInfo.Size(), Mode: 0755, Uid: 1001, Gid: 1001}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(tarWriter, glabFile); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ctr.CopyToContainer(ctx, id, "/opt/crew-tools", &archive); err != nil {
		t.Fatal(err)
	}
	const secrets = "/secrets/native-container-run"
	run([]string{"mkdir", "-p", secrets}, nil, nil)
	if err := orchestrator.NativeCredentialWriteForTest(ctx, ctr, id, "fixture", creds, secrets, "", true, logger); err != nil {
		t.Fatal(err)
	}
	req := orchestrator.AgentRunRequest{AgentSlug: "fixture", RunID: "container-run", CLIAdapter: "CLAUDE_CODE", Credentials: creds}
	env, err := orchestrator.NativeCredentialEnvForTest(orchestrator.BuildEnvVarsSidecar(req, true), creds, secrets, true)
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	// Replace rather than duplicate transport variables: libc and Go differ in
	// which duplicate an inherited environment lookup observes.
	var filtered []string
	for _, entry := range env {
		name := strings.SplitN(entry, "=", 2)[0]
		switch name {
		case "HOME", "GLAB_CONFIG_DIR", "GITLAB_HOST", "GLAB_API_PROTOCOL", "NO_PROXY", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY":
			continue
		}
		filtered = append(filtered, entry)
	}
	env = filtered
	env = append(env, "HOME=/home/agent", "GLAB_CONFIG_DIR=/home/agent/fixture-glab", "GITLAB_HOST="+ctr.HostAddress()+fmt.Sprintf(":%d", port), "GLAB_API_PROTOCOL=http", "GLAB_CHECK_UPDATE=false", "GLAB_SEND_TELEMETRY=false", "GLAB_NO_PROMPT=true", "XDG_CONFIG_DIRS=/home/agent/fixture-system-config", "NO_PROXY=*", "HTTP_PROXY=", "HTTPS_PROXY=", "ALL_PROXY=")
	run([]string{"sh", "-c", fmt.Sprintf(`set -eu; test "$(id -u)" = 1001; test "$(sha256sum "$NATIVE_CONTAINER_FILE" | cut -d ' ' -f1)" = %x; test "$(sha256sum "$NATIVE_CONTAINER_LOGIN_PASSWORD" | cut -d ' ' -f1)" = %x; test "$(cat "$NATIVE_CONTAINER_LOGIN_USERNAME")" = fixture-user; test "$(stat -c %%a "$NATIVE_CONTAINER_LOGIN_PASSWORD")" = 400; test "$(stat -c %%a "$NATIVE_CONTAINER_LOGIN_USERNAME")" = 400; test "$(stat -c %%a "$NATIVE_CONTAINER_FILE")" = 400; sh -c 'test -f "$NATIVE_CONTAINER_FILE" && test -f "$NATIVE_CONTAINER_LOGIN_PASSWORD"'`, sha256.Sum256([]byte(generic)), sha256.Sum256([]byte(password)))}, env, nil)
	out := run([]string{"/opt/crew-tools/fixture-glab", "api", "user"}, env, nil)
	if !bytes.Contains(out, []byte("fixture-container-user")) || authenticated.Load() != 1 {
		t.Fatal("standard glab did not authenticate from native runtime env")
	}
	if keeperMode {
		nativeKeeperCrewScenario(t, ctx, db, ctr, keeperExecutor, id, crew.ID, workspace, agent, peer, master, cli, server.Listener.Addr().(*net.TCPAddr).Port, &acceptedToken, &authenticated)
	}
	for _, credential := range creds {
		cli("", "credential", "unassign", credential.ID, agent)
	}
	if len(resolve(agent)) != 0 {
		t.Fatal("native fixture grant cleanup failed")
	}
	run([]string{"rm", "-rf", secrets}, nil, nil)
	t.Logf("native Vault->crew->glab PASS container=%s image=%s source=%s sdk_sha256=%s UID=1001; GENERIC_SECRET and USERPASS byte/permission checks PASS; no model CLI shell qualification", id, image.ID, commit, probe.SHA256)
}

// Close the attached stream on cancellation so the test can reach its scoped
// cleanup, instead of waiting for the outer go-test process timeout.
func readNativeContainerOutput(ctx context.Context, reader io.ReadCloser) ([]byte, error) {
	stop := context.AfterFunc(ctx, func() { reader.Close() })
	defer stop()
	defer reader.Close()
	const limit = 64 << 10
	out, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	if len(out) > limit {
		return nil, fmt.Errorf("fixture output exceeded bound")
	}
	return out, nil
}
func TestNativeContainerOutputCancellationAndOverflow(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		reader, writer := io.Pipe()
		defer writer.Close()
		if _, err := readNativeContainerOutput(ctx, reader); err == nil {
			t.Fatal("cancelled silent stream accepted")
		}
	})
	t.Run("overflow", func(t *testing.T) {
		if _, err := readNativeContainerOutput(t.Context(), io.NopCloser(strings.NewReader(strings.Repeat("x", (64<<10)+1)))); err == nil {
			t.Fatal("truncated fixture output accepted")
		}
	})
}

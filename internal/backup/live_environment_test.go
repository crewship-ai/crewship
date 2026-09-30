//go:build livedocker

// Live complete-environment round trip (Track E) against a real daemon.
//
//	go test -tags livedocker -run TestLive_Environment -v ./internal/backup/
//
// It creates its own throwaway container from a small local image
// (CREWSHIP_LIVE_ENV_IMAGE, default alpine:3.19 — pulled only if absent),
// named crewship-test-env-*, and removes every container, volume and image
// it created. It never touches another container.
package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

func liveEnvImage() string {
	if v := os.Getenv("CREWSHIP_LIVE_ENV_IMAGE"); v != "" {
		return v
	}
	return "alpine:3.19"
}

func TestLive_EnvironmentRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cli := newLiveClient(t)
	image := liveEnvImage()
	ensureImage(ctx, t, cli, image)
	ops := &MobyDockerOps{Client: cli}

	var rb [4]byte
	_, _ = rand.Read(rb[:])
	tag := hex.EncodeToString(rb[:])
	name := "crewship-test-env-" + tag
	restoredName := "crewship-test-env-restored-" + tag
	volName := "crewship-test-env-vol-" + tag
	volPrefix := "crewship-test-env-rvol-" + tag
	hostDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostDir, "h.txt"), []byte("from the host"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The container writes into the bind as root; let it.
	_ = os.Chmod(hostDir, 0o777)

	var envImage string
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for _, n := range []string{name, restoredName} {
			_, _ = cli.ContainerRemove(c, n, client.ContainerRemoveOptions{Force: true})
		}
		for _, v := range []string{volName, volPrefix + "-0", volPrefix + "-1", volPrefix + "-2", volPrefix + "-3"} {
			_, _ = cli.VolumeRemove(c, v, client.VolumeRemoveOptions{Force: true})
		}
		if envImage != "" {
			_, _ = cli.ImageRemove(c, envImage, client.ImageRemoveOptions{})
		}
	})

	res, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image: image, Cmd: []string{"sleep", "3600"},
			Env:    []string{"KEEP_ME=yes", "GITHUB_TOKEN=do-not-keep"},
			Labels: map[string]string{FlushHookLabel: "echo flushed > /data/flushed"},
		},
		HostConfig: &container.HostConfig{
			CapAdd: []string{"NET_ADMIN"},
			Tmpfs:  map[string]string{"/scratch": ""},
			Mounts: []mount.Mount{
				{Type: mount.TypeVolume, Source: volName, Target: "/data"},
				{Type: mount.TypeBind, Source: hostDir, Target: "/hostdata"},
			},
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := cli.ContainerStart(ctx, res.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start: %v", err)
	}
	mustExec := func(id string, cmd ...string) string {
		t.Helper()
		code, out, err := ops.Exec(ctx, id, cmd)
		if err != nil || code != 0 {
			t.Fatalf("exec %v in %s: code %d err %v out %s", cmd, id, code, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	mustExec(name, "sh", "-c", "echo local-change > /etc/crewship-marker && echo volume-data > /data/hello")

	// Capture into a payload file and a store.
	storeDir := t.TempDir()
	store := &EnvironmentStore{Dir: storeDir}
	var payload bytes.Buffer
	tw, err := NewTarZstWriter(&payload)
	if err != nil {
		t.Fatal(err)
	}
	env, err := CollectEnvironment(ctx, ops, CrewTarget{ID: "c1", Slug: "envtest", ContainerID: name}, store, EnvironmentOptions{Payload: tw, Covered: map[string]bool{}})
	if err != nil {
		t.Fatalf("CollectEnvironment: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	envImage = env.ImageRef
	t.Logf("captured %s: %d archive entries, %d blobs, %s, platform %s, docker %s",
		env.ID, len(env.Image.Entries), len(env.Blobs()), humanBytes(env.BlobBytes()), env.Platform, env.Runtime.DockerVersion)
	if !env.Flush.OK {
		t.Errorf("flush hook did not run cleanly: %+v", env.Flush)
	}
	if ok, _ := ops.ImageExists(ctx, env.ImageRef); ok {
		t.Errorf("temporary image %s left behind after capture", env.ImageRef)
	}
	kinds := map[string]bool{}
	for _, u := range env.Unsafe {
		kinds[u.Kind] = true
	}
	if !kinds[UnsafeCapAdd] || !kinds[UnsafeHostBind] {
		t.Errorf("unsafe list = %+v, want cap_add and host_bind", env.Unsafe)
	}
	for _, e := range env.Config.Env {
		if strings.HasPrefix(e, "GITHUB_TOKEN=") {
			t.Errorf("secret-looking env kept: %s", e)
		}
	}
	for _, d := range env.Blobs() {
		if !store.Has(d) {
			t.Errorf("store lacks blob %s", d)
		}
	}

	// A second capture of the same container shares every base layer.
	var payload2 bytes.Buffer
	tw2, _ := NewTarZstWriter(&payload2)
	env2, err := CollectEnvironment(ctx, ops, CrewTarget{ID: "c1", Slug: "envtest", ContainerID: name}, store, EnvironmentOptions{Payload: tw2, NoFlush: true})
	if err != nil {
		t.Fatalf("second capture: %v", err)
	}
	_ = tw2.Close()
	shared := 0
	first := map[string]bool{}
	for _, d := range env.Blobs() {
		first[d] = true
	}
	for _, d := range env2.Blobs() {
		if first[d] {
			shared++
		}
	}
	if shared == 0 {
		t.Errorf("second capture shares no blob with the first")
	}
	t.Logf("second capture shares %d of %d blobs", shared, len(env2.Blobs()))

	// The original goes away entirely.
	if _, err := cli.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.VolumeRemove(ctx, volName, client.VolumeRemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}

	ex, err := ExtractPayload(ctx, bytes.NewReader(payload.Bytes()))
	if err != nil {
		t.Fatalf("ExtractPayload: %v", err)
	}
	defer ex.Close()
	got := ex.EnvironmentBySlug["envtest"]
	if got == nil || got.ID != env.ID {
		t.Fatalf("payload environment = %+v", got)
	}
	open, has := BlobSources(store)
	out := RestoreEnvironment(ctx, ops, got, EnvironmentRestoreOptions{
		Open: open, Has: has, Recreate: true, Name: restoredName, VolumePrefix: volPrefix,
		MountData: func(ctx context.Context, m EnvironmentMount) (io.ReadCloser, bool, error) {
			return ex.OpenEnvironmentMount(ctx, "envtest", m)
		},
	})
	t.Logf("restore outcome: %+v", out)
	if out.Result != EnvRestored {
		t.Fatalf("restore result %s: %s", out.Result, out.Reason)
	}
	if v := mustExec(restoredName, "cat", "/etc/crewship-marker"); v != "local-change" {
		t.Errorf("rootfs change = %q", v)
	}
	if v := mustExec(restoredName, "cat", "/data/hello"); v != "volume-data" {
		t.Errorf("volume content = %q", v)
	}
	if v := mustExec(restoredName, "cat", "/data/flushed"); v != "flushed" {
		t.Errorf("flush hook output = %q", v)
	}
	if v := mustExec(restoredName, "cat", "/hostdata/h.txt"); v != "from the host" {
		t.Errorf("bind content = %q", v)
	}
	insp, err := cli.ContainerInspect(ctx, restoredName, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(insp.Container.HostConfig.CapAdd) != 0 {
		t.Errorf("restored container carries cap_add %v", insp.Container.HostConfig.CapAdd)
	}
	for _, m := range insp.Container.Mounts {
		if m.Type == "bind" {
			t.Errorf("restored container binds a host path: %+v", m)
		}
	}
	for _, e := range insp.Container.Config.Env {
		if strings.HasPrefix(e, "GITHUB_TOKEN=") && e != "GITHUB_TOKEN=" {
			t.Errorf("restored container has the secret env: %s", e)
		}
	}
	// Docker bakes the container env into the committed image config; no
	// blob in the store may carry the secret's value.
	blobs, _ := filepath.Glob(filepath.Join(storeDir, "blobs", "sha256", "*"))
	for _, b := range blobs {
		data, err := os.ReadFile(b)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("do-not-keep")) {
			t.Errorf("blob %s carries the dropped secret's value", filepath.Base(b))
		}
	}
	// The loaded image stays for the restored container; the cleanup
	// removes it once the container is gone.
	_ = env2
}

// Two workspace backups with env_mode=complete against a real container:
// the manifest lists the layers, the store holds them once, and rotating
// the older bundle away keeps every layer the newer one shares.
func TestLive_EnvironmentWorkspaceBackupAndRotation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cli := newLiveClient(t)
	image := liveEnvImage()
	ensureImage(ctx, t, cli, image)
	ops := &MobyDockerOps{Client: cli}

	var rb [4]byte
	_, _ = rand.Read(rb[:])
	name := "crewship-test-env-ws-" + hex.EncodeToString(rb[:])
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, _ = cli.ContainerRemove(c, name, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	})
	if _, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:   name,
		Config: &container.Config{Image: image, Cmd: []string{"sleep", "3600"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.ContainerStart(ctx, name, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}

	db := openMigratedDBCov(t)
	wsID, _ := seedCovWorkspace(t, db, "liveenv")
	dir := t.TempDir()
	create := func(marker string) *CreateResult {
		t.Helper()
		if code, out, err := ops.Exec(ctx, name, []string{"sh", "-c", "echo " + marker + " > /etc/crewship-marker"}); err != nil || code != 0 {
			t.Fatalf("exec: %d %v %s", code, err, out)
		}
		res, err := CreateBackup(ctx, db, CreateOptions{
			Scope: ScopeWorkspace, WorkspaceID: wsID, OutputDir: dir, Actor: covAdminActor(),
			NoEncrypt: true, DockerOps: ops, EnvMode: EnvModeComplete,
			CrewContainerName: func(_, _ string) string { return name },
		})
		if err != nil {
			t.Fatalf("CreateBackup: %v", err)
		}
		if err := UpsertCatalogEntry(ctx, db, CatalogEntryFromResult(res, res.Manifest)); err != nil {
			t.Fatal(err)
		}
		return res
	}
	older := create("one")
	time.Sleep(1100 * time.Millisecond)
	newer := create("two")
	envs := older.Manifest.Contents.Environments
	if len(envs) != 1 || len(envs[0].Blobs) == 0 {
		t.Fatalf("manifest environments = %+v (incomplete %+v)", envs, older.Manifest.Contents.Incomplete)
	}
	t.Logf("older bundle: %d blobs, %s; newer: %d blobs", len(envs[0].Blobs), humanBytes(envs[0].Bytes), len(BundleEnvironmentBlobs(newer.Manifest)))

	store := EnvironmentStoreFor(dir)
	newerSet := map[string]bool{}
	for _, d := range BundleEnvironmentBlobs(newer.Manifest) {
		newerSet[d] = true
	}
	var onlyOlder []string
	for _, d := range BundleEnvironmentBlobs(older.Manifest) {
		if !newerSet[d] {
			onlyOlder = append(onlyOlder, d)
		}
	}
	dropped, err := RotateWithPolicy(ctx, db, dir, wsID, RetentionPolicy{KeepMin: 1}, false)
	if err != nil || len(dropped) != 1 || dropped[0] != older.Path {
		t.Fatalf("rotate = %v, %v", dropped, err)
	}
	for d := range newerSet {
		if !store.Has(d) {
			t.Errorf("shared layer %s deleted with the older bundle", d)
		}
	}
	for _, d := range onlyOlder {
		if store.Has(d) {
			t.Errorf("layer %s only the rotated bundle needed survived", d)
		}
	}
	t.Logf("rotation removed %d layer file(s) only the older bundle needed, kept %d", len(onlyOlder), len(newerSet))
	for _, ref := range []string{envs[0].ID, newer.Manifest.Contents.Environments[0].ID} {
		if ok, _ := ops.ImageExists(ctx, environmentImageRef("crew-liveenv", ref)); ok {
			t.Errorf("temporary environment image for %s left behind", ref)
		}
	}
}

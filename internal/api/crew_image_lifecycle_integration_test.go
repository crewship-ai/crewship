//go:build integration && linux

package api

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	dockerprovider "github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

// Run on a disposable local engine preloaded only with gc-fixture:base,
// containing /bin/busybox. The runner disposes of the entire engine.
func TestIsolatedImageLifecycle(t *testing.T) {
	host := os.Getenv("CREWSHIP_TEST_ISOLATED_ENGINE")
	if host == "" {
		t.Skip("requires a dedicated engine")
	}
	if !strings.HasPrefix(host, "unix:///") || host != os.Getenv("DOCKER_HOST") {
		t.Fatal("explicit isolated socket must match DOCKER_HOST")
	}
	ctx := t.Context()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	command := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "docker", append([]string{"--host", host}, args...)...)
		out, err := cmd.Output()
		if err != nil {
			var failure *exec.ExitError
			if errors.As(err, &failure) {
				t.Fatalf("%v: %s: %v", args, failure.Stderr, err)
			}
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	if command("ps", "-aq") != "" || command("volume", "ls", "-q") != "" || len(strings.Fields(command("image", "ls", "-aq"))) != 1 {
		t.Fatal("fixture requires a fresh engine")
	}
	base := command("image", "inspect", "--format", "{{.Id}}", "gc-fixture:base")
	scratch := command("create", "--network", "none", base)
	image := func(key, owner string) string {
		t.Helper()
		tag := "crewship-cache:" + owner + "-" + strings.Repeat(key, 12)
		command("commit", "--change", "LABEL "+devcontainer.ImageInstanceLabel+"="+owner, "--change", "LABEL "+devcontainer.ImageKindLabel+"=crew", "--change", "LABEL "+devcontainer.ImageAcquisitionKeyLabel+"="+strings.Repeat(key, 64), scratch, tag)
		return command("image", "inspect", "--format", "{{.Id}}", tag)
	}
	old := image("1", collectionOwner)
	graceOnly := image("6", collectionOwner)
	current := image("2", collectionOwner)
	previous := image("3", collectionOwner)
	stoppedImage := image("4", collectionOwner)
	foreign := image("5", strings.Repeat("b", 64))
	command("rm", scratch)
	makeCrew := func(name, crew, img string) string {
		return command("create", "--name", name, "--network", "none", "--label", resourcelifecycle.InstanceLabel+"="+collectionOwner, "--label", "crewship.kind=crew", "--label", "crewship.crew-id="+crew, img)
	}
	target := makeCrew("deleted-crew", "deleted", old)
	live := makeCrew("surviving-crew", "live", current)
	unknown := makeCrew("crew-without-row", "unknown", base)
	stopped := command("create", "--network", "none", stoppedImage)
	volume := command("volume", "create", "customer")
	app := command("run", "-d", "--network", "none", "--label", "com.docker.compose.project=customer", "-v", volume+":/customer", base, "/bin/busybox", "sleep", "600")
	command("exec", app, "/bin/busybox", "sh", "-c", "printf 'customer bytes' >/customer/marker")
	temp := command("create", "--name", devcontainer.TempContainerNamePrefix+"scratch", "--label", "crewship.temp=provision", "--network", "none", base)

	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(&tar.Header{Name: "marker", Size: 3, Mode: 0600}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup.tar")
	if err := os.WriteFile(backup, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	seedCrewRow(t, db, "deleted", ws, "Deleted", "deleted")
	seedCrewRow(t, db, "live", ws, "Live", "live")
	seedChatForDelete(t, &AgentHandler{db: db}, ws, "chat-agent", "chat", user)
	mustExec(t, db, "UPDATE crews SET cached_image=?,previous_image=? WHERE id='deleted'", old, graceOnly)
	mustExec(t, db, "UPDATE crews SET cached_image=?,previous_image=? WHERE id='live'", current, previous)
	h := &ProvisioningHandler{db: db, logger: slog.Default(), instanceID: collectionOwner, gcClient: cli, imageReferences: dockerprovider.NewImageReferences(cli, collectionOwner)}
	clock := time.Now().UTC()
	h.gcClock = func() time.Time { return clock }
	t.Setenv(imageGCEnv, "true")
	h.reconcileImageReferences(ctx)
	// Restore a database-only snapshot on the same daemon. Each installation
	// has its own gate and derives the daemon-visible pins from its own DB.
	copyPath := filepath.Join(t.TempDir(), "restored.db")
	mustExec(t, db, "VACUUM INTO ?", copyPath)
	borrowDB, err := sql.Open("sqlite", copyPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = borrowDB.Close() })
	mustExec(t, borrowDB, "UPDATE crews SET previous_image=NULL WHERE id='deleted'")
	borrower := &ProvisioningHandler{db: borrowDB, logger: slog.Default(), instanceID: strings.Repeat("b", 64), imageReferences: dockerprovider.NewImageReferences(cli, strings.Repeat("b", 64))}
	borrower.reconcileImageReferences(ctx)
	counts := func(stage string) {
		t.Logf("%s images=%d containers=%d volumes=%d", stage, len(strings.Fields(command("image", "ls", "-aq"))), len(strings.Fields(command("ps", "-aq"))), len(strings.Fields(command("volume", "ls", "-q"))))
	}
	counts("before DELETE")
	controller := &resourcelifecycle.Controller{DB: db, InstanceID: collectionOwner, Connect: dockerprovider.NewCleanupRuntime}
	handler := NewCrewHandler(db, newTestLogger())
	handler.containerCleanup = controller
	reply := httptest.NewRecorder()
	handler.Delete(reply, crewDeleteReq(user, ws, "deleted"))
	if reply.Code != 200 {
		t.Fatalf("DELETE: %d %s", reply.Code, reply.Body.String())
	}
	controller.Tick(ctx)
	containerPresent := func(id string, want bool) {
		t.Helper()
		_, err := cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if (err != nil && !errdefs.IsNotFound(err)) || (err == nil) != want {
			t.Errorf("container %s want present=%t: %v", id, want, err)
		}
	}
	imagePresent := func(id string, want bool) {
		t.Helper()
		_, err := cli.ImageInspect(ctx, id)
		if (err != nil && !errdefs.IsNotFound(err)) || (err == nil) != want {
			t.Errorf("image %s want present=%t: %v", id, want, err)
		}
	}
	containerPresent(target, false)
	start := clock
	pass := func() {
		h.reconcileImageReferences(ctx)
		h.sweepOrphanTempContainers(ctx)
		h.sweepOrphanCacheImages(ctx)
	}
	clock = start.Add(30 * time.Minute)
	pass()
	containerPresent(temp, true)
	clock = start.Add(90 * time.Minute)
	pass()
	containerPresent(temp, false)
	clock = start.Add(7*24*time.Hour - time.Minute)
	pass()
	imagePresent(old, true)
	imagePresent(graceOnly, true)
	clock = start.Add(7*24*time.Hour + time.Minute)
	pass()
	for range 47 {
		clock = clock.Add(orphanGCInterval)
		h.sweepOrphanCacheImages(ctx)
	}
	imagePresent(graceOnly, true)
	clock = clock.Add(orphanGCInterval)
	pass()
	imagePresent(graceOnly, false)
	imagePresent(old, true)
	// Two successive selections retire both the current and rollback reference.
	for _, next := range []string{previous, current} {
		mustExec(t, borrowDB, "UPDATE crews SET cached_image=? WHERE id='deleted'", next)
	}
	borrower.reconcileImageReferences(ctx)
	pass()
	for i := 0; i < 47; i++ {
		clock = clock.Add(orphanGCInterval)
		h.sweepOrphanCacheImages(ctx)
	}
	imagePresent(old, true)
	clock = clock.Add(orphanGCInterval)
	pass()
	imagePresent(old, false)
	for _, id := range []string{base, current, previous, foreign, stoppedImage} {
		imagePresent(id, true)
	}
	for _, id := range []string{app, stopped, live, unknown} {
		containerPresent(id, true)
	}
	if command("exec", app, "/bin/busybox", "cat", "/customer/marker") != "customer bytes" {
		t.Error("volume contents changed")
	}
	content, err := os.ReadFile(backup)
	if err != nil || !bytes.Equal(content, archive.Bytes()) {
		t.Fatal("archive changed", err)
	}
	var message string
	if err := db.QueryRow("SELECT content FROM conversation_messages WHERE id='chat-m1' AND role='user'").Scan(&message); err != nil || message != "hi" {
		t.Fatal("chat changed", err)
	}
	counts("after retention and unused window")
}

// Exercise ordinary replacement of a feature tag while an installation
// retains the prior ID. Report ImageInspect independently of reconciliation:
// retagging alone need not discard the record on every engine.
func TestIsolatedFeatureTagReplacement(t *testing.T) {
	host := os.Getenv("CREWSHIP_TEST_ISOLATED_ENGINE")
	if host == "" {
		t.Skip("requires a dedicated engine")
	}
	if !strings.HasPrefix(host, "unix:///") || host != os.Getenv("DOCKER_HOST") {
		t.Fatal("isolated socket mismatch")
	}
	ctx := t.Context()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	cmd := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", append([]string{"--host", host}, args...)...).Output()
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	owner := strings.Repeat("c", 64)
	key := strings.Repeat("a", 64)
	labels := map[string]string{devcontainer.ImageInstanceLabel: owner, devcontainer.ImageKindLabel: devcontainer.ImageKindFeature, devcontainer.ImageAcquisitionKeyLabel: key}
	tag := devcontainer.ImageAcquisitionRef(labels)
	dir := t.TempDir()
	builder := devcontainer.NewDockerBuildKitBuilderFor(slog.Default(), cli)
	export := func(gen string) string {
		t.Helper()
		var recipe strings.Builder
		recipe.WriteString("FROM scratch\nCOPY generation /generation\n")
		for _, key := range []string{devcontainer.ImageInstanceLabel, devcontainer.ImageKindLabel, devcontainer.ImageAcquisitionKeyLabel} {
			recipe.WriteString("LABEL " + key + "=" + labels[key] + "\n")
		}
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(recipe.String()), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "generation"), []byte(gen), 0600); err != nil {
			t.Fatal(err)
		}
		// Podman's Docker compatibility service has no BuildKit session
		// endpoint. Exercise its native exporter on the same API socket.
		if os.Getenv("CREWSHIP_TEST_PODMAN_BUILDER") == "1" {
			out, err := exec.CommandContext(ctx, "podman", "--remote", "--url", host, "build", "--network=none", "--pull=never", "-t", tag, dir).CombinedOutput()
			if err != nil {
				t.Fatalf("podman build: %v: %s", err, out)
			}
		} else if err := builder.Build(ctx, dir, tag, func(line string) { t.Log(line) }); err != nil {
			t.Fatal(err)
		}
		return cmd("image", "inspect", "--format", "{{.Id}}", tag)
	}
	old := export("first")
	pins := dockerprovider.NewImageReferences(cli, owner)
	if _, err := pins.Preserve(ctx, []resourcelifecycle.ImageReference{{Ref: old, Required: true}}); err != nil {
		t.Fatal(err)
	}
	newID := export("second")
	if old == newID {
		t.Fatal("fixture did not move tag")
	}
	_, inspectErr := cli.ImageInspect(ctx, old)
	if inspectErr != nil && !errdefs.IsNotFound(inspectErr) {
		t.Fatal(inspectErr)
	}
	t.Logf("tag moved, prior config ID still inspectable=%t", inspectErr == nil)
	if err := pins.Reconcile(ctx, &resourcelifecycle.ImageUse{}, func(context.Context) ([]resourcelifecycle.ImageReference, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	left := cmd("ps", "-aq", "--filter", "label=crewship.image.reference.instance-id="+owner)
	if left != "" {
		t.Fatalf("unselected fixture reference remains: %s", left)
	}
}

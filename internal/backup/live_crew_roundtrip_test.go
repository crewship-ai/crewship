//go:build livedocker

// Live backup/restore round-trip against a real container runtime.
//
// # Why this file exists
//
// Every one of #1713 / #1714 / #1715 shipped with a green unit suite. The
// unit tests assert the SHAPE of the bundle ("a memory section exists")
// against a fake DockerOps that faithfully reproduces whatever the
// collector asks it for — so a collector asking for the wrong path gets a
// tar back and looks healthy. Nothing in the package had ever put a byte
// on a real filesystem, read it back through a real kernel, and looked at
// who owned it.
//
// This harness does exactly that, and only that. It builds a container
// with the crew's real mount shape (three bind-backed local volumes at
// /workspace, /output, /crew; two named volumes at /home/agent and
// /opt/crew-tools; uid 1001; CapDrop ALL), seeds real memory files at the
// real addresses, runs CollectCrew -> ExtractPayload -> RestoreCrew, and
// then reads content, uid, gid and mode back from INSIDE the container.
//
// # Running it
//
//	go test -tags livedocker -run TestLive -v ./internal/backup/
//
// Against a specific runtime, point DOCKER_HOST at it:
//
//	DOCKER_HOST=unix://$HOME/.colima/default/docker.sock \
//	  go test -tags livedocker -run TestLive -v ./internal/backup/
//
// Knobs: CREWSHIP_LIVE_IMAGE (default debian:bookworm-slim — needs tar,
// which the restore's exec path shells out to) and CREWSHIP_LIVE_KEEP=1 to
// leave containers and volumes up for a post-mortem.
package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// agentUID / agentGID are the crew container's runtime identity, and
// sidecarGID is the group the .memory subtrees are handed to so the
// memory sidecar (uid 1002) can write the same tree the agent does. All
// three are hard-coded across the provider (buildChownInitCmd,
// prepMemoryDirs) — repeated here rather than imported because
// internal/backup must not depend on internal/provider/docker.
// sidecarGID is defined in restorer.go — the production constant is the
// one worth asserting against, since a test carrying its own copy would
// keep passing if the runtime's changed.
const (
	agentUID = 1001
	agentGID = 1001
)

// liveCrew is a real container wearing the crew mount shape, plus the
// host paths and volume names behind it.
type liveCrew struct {
	cli         *client.Client
	id          string
	name        string
	homeVolume  string
	toolsVolume string
	image       string
}

func liveImage() string {
	if v := os.Getenv("CREWSHIP_LIVE_IMAGE"); v != "" {
		return v
	}
	return "debian:bookworm-slim"
}

// newLiveClient dials whatever runtime DOCKER_HOST (or the default
// socket) points at. Fails rather than skips: this file is behind a
// build tag, so reaching it means somebody asked for a live run, and
// reporting ok because no daemon answered is the silent non-coverage
// that let these four bugs ship.
func newLiveClient(t *testing.T) *client.Client {
	t.Helper()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatalf("no container runtime reachable: %v — start one, or point DOCKER_HOST at it", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	if _, err := cli.Ping(context.Background(), client.PingOptions{}); err != nil {
		t.Fatalf("runtime did not answer ping: %v", err)
	}
	return cli
}

func ensureImage(ctx context.Context, t *testing.T, cli *client.Client, image string) {
	t.Helper()
	if _, err := cli.ImageInspect(ctx, image); err == nil {
		return
	}
	rc, err := cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		t.Fatalf("pull %s: %v", image, err)
	}
	defer func() { _ = rc.Close() }()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		t.Fatalf("drain pull %s: %v", image, err)
	}
}

// bindVolume reproduces the provider's noexecBindMount: a TypeVolume
// with the local driver bind-mounting a host path. Using the same shape
// matters — Docker's archive API treats a bind-backed local volume
// differently from a plain TypeBind, and the restore path this test
// exercises is chosen by exactly that distinction.
func bindVolume(hostPath, target string) mount.Mount {
	return mount.Mount{
		Type:   mount.TypeVolume,
		Target: target,
		VolumeOptions: &mount.VolumeOptions{
			DriverConfig: &mount.Driver{
				Name: "local",
				Options: map[string]string{
					"type":   "none",
					"device": hostPath,
					"o":      "bind,noexec,nosuid",
				},
			},
		},
	}
}

// createVolumeBackedRoot makes a Docker volume and returns its
// daemon-side mountpoint, which callers use as the `device=` of the
// bind-backed local volumes. See the comment in startLiveCrew for why
// the roots cannot be macOS host directories.
func createVolumeBackedRoot(ctx context.Context, t *testing.T, cli *client.Client, name string) string {
	t.Helper()
	_, _ = cli.VolumeRemove(ctx, name, client.VolumeRemoveOptions{Force: true})
	vol, err := cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name})
	if err != nil {
		t.Fatalf("create root volume %s: %v", name, err)
	}
	if os.Getenv("CREWSHIP_LIVE_KEEP") == "" {
		t.Cleanup(func() {
			_, _ = cli.VolumeRemove(context.Background(), name, client.VolumeRemoveOptions{Force: true})
		})
	}
	if vol.Volume.Mountpoint == "" {
		t.Fatalf("runtime reported no mountpoint for volume %s", name)
	}
	return vol.Volume.Mountpoint
}

// startLiveCrew builds and starts the container. chownVolumes mirrors the
// #1715 fix: when false the named volumes come up in Docker's default
// state (root-owned 0755), which is what a freshly provisioned crew has
// whenever the image has no /opt/crew-tools to seed the volume from.
func startLiveCrew(ctx context.Context, t *testing.T, cli *client.Client, id string, chownVolumes bool) *liveCrew {
	t.Helper()
	image := liveImage()
	ensureImage(ctx, t, cli, image)

	// The three bind roots have to live on a Linux filesystem, not on the
	// developer's macOS host. A macOS host directory reaches the container
	// through the runtime's file-sharing layer (VirtioFS on OrbStack),
	// which does not carry POSIX ownership: every file inside reads back
	// uid 0 gid 0 no matter who wrote it, and a chown is silently dropped.
	// That would make every ownership assertion in this file vacuously
	// pass — the exact failure mode #1714 is about. Backing the binds with
	// a Docker volume puts them on the daemon's own Linux filesystem,
	// where ownership is real, while keeping the production mount shape
	// (a local-driver volume bind-mounting a host path).
	//
	// In production OutputBasePath is a Linux path on the server, so this
	// is the faithful arrangement, not a workaround around one.
	base := createVolumeBackedRoot(ctx, t, cli, "crewship-live-root-"+id)
	outputPath := path.Join(base, "output", id)
	workspacePath := path.Join(base, "output", "workspaces", id)
	crewPath := path.Join(base, "output", "crews", id)
	sharedPath := path.Join(crewPath, "shared")
	agentsPath := path.Join(crewPath, "agents")
	allDirs := []string{outputPath, workspacePath, crewPath, sharedPath, agentsPath}

	name := "crewship-live-" + id
	homeVol := "crewship-live-home-" + id
	toolsVol := "crewship-live-tools-" + id
	// A previous aborted run leaves these behind; the create would then
	// fail on the name rather than on anything this test is about.
	_, _ = cli.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	_, _ = cli.VolumeRemove(ctx, homeVol, client.VolumeRemoveOptions{Force: true})
	_, _ = cli.VolumeRemove(ctx, toolsVol, client.VolumeRemoveOptions{Force: true})

	lc := &liveCrew{cli: cli, name: name, homeVolume: homeVol, toolsVolume: toolsVol, image: image}

	// The root init container: chowns the host binds to the agent and
	// re-flips .memory subtrees to the sidecar group, exactly as
	// buildChownInitCmd does. Optionally chowns the two named volume
	// roots — that is the #1715 fix, staged here as a flag so the same
	// harness can reproduce the defect and prove the fix.
	initCmd := "mkdir -p"
	for _, d := range allDirs {
		initCmd += " '" + path.Join("/mnt/root", strings.TrimPrefix(d, base)) + "'"
	}
	initCmd += " && chown -R 1001:1001"
	for _, d := range allDirs {
		initCmd += " '" + path.Join("/mnt/root", strings.TrimPrefix(d, base)) + "'"
	}
	initMounts := []mount.Mount{
		{Type: mount.TypeVolume, Source: "crewship-live-root-" + id, Target: "/mnt/root"},
		{Type: mount.TypeVolume, Source: homeVol, Target: "/mnt/home"},
		{Type: mount.TypeVolume, Source: toolsVol, Target: "/mnt/tools"},
	}
	if chownVolumes {
		initCmd += " && chown 1001:1001 /mnt/home /mnt/tools && chmod 755 /mnt/home /mnt/tools"
	}
	runThrowaway(ctx, t, cli, image, "0:0", []string{"sh", "-c", initCmd}, initMounts)

	mounts := []mount.Mount{
		bindVolume(workspacePath, "/workspace"),
		bindVolume(outputPath, "/output"),
		bindVolume(crewPath, "/crew"),
		{Type: mount.TypeVolume, Source: homeVol, Target: "/home/agent"},
		{Type: mount.TypeVolume, Source: toolsVol, Target: "/opt/crew-tools"},
	}

	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: image,
			User:  "1001:1001",
			Cmd:   []string{"sleep", "infinity"},
		},
		HostConfig: &container.HostConfig{
			Mounts:  mounts,
			CapDrop: []string{"ALL"},
		},
		NetworkingConfig: &dockernetwork.NetworkingConfig{},
		Name:             name,
	})
	if err != nil {
		t.Fatalf("create live crew container: %v", err)
	}
	lc.id = created.ID
	if os.Getenv("CREWSHIP_LIVE_KEEP") == "" {
		t.Cleanup(func() {
			rmCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			_, _ = cli.ContainerRemove(rmCtx, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
			_, _ = cli.VolumeRemove(rmCtx, homeVol, client.VolumeRemoveOptions{Force: true})
			_, _ = cli.VolumeRemove(rmCtx, toolsVol, client.VolumeRemoveOptions{Force: true})
		})
	}
	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start live crew container: %v", err)
	}
	return lc
}

// runThrowaway runs a one-shot container to completion and fails the test
// on a non-zero exit, printing its output — an init container that
// silently failed is how #1715 stayed invisible.
func runThrowaway(ctx context.Context, t *testing.T, cli *client.Client, image, user string, cmd []string, mounts []mount.Mount) {
	t.Helper()
	resp, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: image, User: user, Entrypoint: cmd},
		HostConfig: &container.HostConfig{Mounts: mounts},
	})
	if err != nil {
		t.Fatalf("create throwaway container: %v", err)
	}
	defer func() {
		_, _ = cli.ContainerRemove(context.Background(), resp.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if _, err := cli.ContainerStart(ctx, resp.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start throwaway container: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	wait := cli.ContainerWait(waitCtx, resp.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case res := <-wait.Result:
		if res.StatusCode != 0 {
			logs, _ := cli.ContainerLogs(context.Background(), resp.ID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
			var b bytes.Buffer
			if logs != nil {
				_, _ = io.Copy(&b, logs)
				_ = logs.Close()
			}
			t.Fatalf("throwaway container %v exited %d: %s", cmd, res.StatusCode, b.String())
		}
	case err := <-wait.Error:
		t.Fatalf("wait throwaway container: %v", err)
	case <-waitCtx.Done():
		t.Fatalf("throwaway container timed out")
	}
}

// sh runs a shell command inside the crew container as the given user and
// returns its combined output and exit code.
func (lc *liveCrew) sh(ctx context.Context, t *testing.T, user, script string) (string, int) {
	t.Helper()
	exec, err := lc.cli.ExecCreate(ctx, lc.id, client.ExecCreateOptions{
		Cmd:          []string{"sh", "-c", script},
		User:         user,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		t.Fatalf("exec create: %v", err)
	}
	resp, err := lc.cli.ExecAttach(ctx, exec.ID, client.ExecAttachOptions{})
	if err != nil {
		t.Fatalf("exec attach: %v", err)
	}
	defer resp.Close()
	var out bytes.Buffer
	_, _ = stdcopy.StdCopy(&out, &out, resp.Reader)
	insp, err := lc.cli.ExecInspect(ctx, exec.ID, client.ExecInspectOptions{})
	if err != nil {
		t.Fatalf("exec inspect: %v", err)
	}
	return out.String(), insp.ExitCode
}

// mustSh runs a script as the agent and fails on non-zero exit.
func (lc *liveCrew) mustSh(ctx context.Context, t *testing.T, user, script string) string {
	t.Helper()
	out, code := lc.sh(ctx, t, user, script)
	if code != 0 {
		t.Fatalf("script exited %d: %s\n--- script ---\n%s", code, out, script)
	}
	return out
}

// fileFacts is what the kernel says about one restored path, read from
// inside the container. Asserting these — rather than "the tar had an
// entry" — is the entire point of this file.
type fileFacts struct {
	content string
	uid     string
	gid     string
	mode    string
	// mtime matters because internal/memory's watcher snapshots the
	// mtime of every .md under the memory root to decide what to
	// reindex, and because a daily note's timestamp is part of what it
	// says. A restore that stamps every file with "now" reports a tree
	// that was all written the instant it was recovered.
	mtime  string
	exists bool
}

func (lc *liveCrew) stat(ctx context.Context, t *testing.T, p string) fileFacts {
	t.Helper()
	// `cat` on a directory exits non-zero; without the trailing `true`
	// the whole probe would report every directory as missing, which is
	// a way to pass an ownership assertion by never making it.
	out, code := lc.sh(ctx, t, "0:0", fmt.Sprintf(
		`if [ -e %q ]; then stat -c '%%u %%g %%a %%Y' %q; echo '--'; [ -f %q ] && cat %q; else echo MISSING; fi; true`, p, p, p, p))
	if code != 0 {
		return fileFacts{}
	}
	if strings.HasPrefix(strings.TrimSpace(out), "MISSING") {
		return fileFacts{}
	}
	parts := strings.SplitN(out, "--\n", 2)
	fields := strings.Fields(parts[0])
	f := fileFacts{exists: true}
	if len(fields) >= 4 {
		f.uid, f.gid, f.mode, f.mtime = fields[0], fields[1], fields[2], fields[3]
	}
	if len(parts) == 2 {
		f.content = parts[1]
	}
	return f
}

func (f fileFacts) String() string {
	if !f.exists {
		return "MISSING"
	}
	return fmt.Sprintf("uid=%s gid=%s mode=%s mtime=%s content=%q", f.uid, f.gid, f.mode, f.mtime, f.content)
}

// seedMemory writes the memory tree at the addresses the runtime really
// uses, with the ownership the runtime really applies: the agent owns the
// files, the .memory dirs are group 1002 setgid 2775, and the files in
// them are group-writable. Reproduces prepMemoryDirs + buildChownInitCmd.
func (lc *liveCrew) seedMemory(ctx context.Context, t *testing.T, agentSlugs []string) map[string]string {
	t.Helper()
	want := map[string]string{}
	var b strings.Builder
	// Single-quoted, one line + trailing newline: the content has to
	// survive a trip through `sh -c`, and %q would hand printf a literal
	// backslash-n that lands in the file as two characters.
	add := func(p, line string) {
		want[p] = line + "\n"
		fmt.Fprintf(&b, "mkdir -p '%s' && printf '%%s\\n' '%s' > '%s'\n", filepath.Dir(p), line, p)
	}
	// A memory topic directory whose name contains a space. Topic
	// directories are agent-written, so this is reachable content, and it
	// has to survive both the tar round trip and the recursive chmod the
	// restore runs afterwards.
	//
	// It does NOT exercise the `find -exec … +` form in
	// reapplyMemoryPerms: that walk only ever names `.memory` directories
	// themselves, whose parents are validated slug components. Mutating
	// that form back to `for p in $(find …)` was tried and survived —
	// the shape is defence-in-depth against a future caller, not a fix
	// for a reachable bug, and is reported as such.
	add("/crew/shared/.memory/topics/quarterly review/pins.md", "pinned: topic directories are agent-written")
	add("/crew/shared/.memory/CREW.md", "crew charter: ship the backup fix")
	add("/crew/shared/.memory/learned.md", "learned: never trust a manifest that asserts")
	add("/crew/shared/.memory/topics/eng/pins.md", "pinned: /crew is the memory tree")
	for _, slug := range agentSlugs {
		add("/crew/agents/"+slug+"/.memory/AGENT.md", "agent "+slug+" identity")
		add("/crew/agents/"+slug+"/.memory/PERSONA.md", "persona for "+slug)
		add("/crew/agents/"+slug+"/.memory/pins.md", "pins for "+slug)
		add("/crew/agents/"+slug+"/.memory/daily/2026-08-03.md", "daily note for "+slug)
	}
	// /crew/init.sh is the one file under /crew whose MODE is the whole
	// point of it: the entrypoint runs it as `[ -x /crew/init.sh ] &&
	// /crew/init.sh`, so a restore that drops the executable bit disables
	// the crew's init hook and reports nothing. It is also the reason the
	// crew section restores with --same-permissions rather than letting
	// the destination umask decide.
	add("/crew/init.sh", "#!/bin/sh")
	// /workspace and /output are the two sections that already round-trip,
	// kept in the fixture so a regression there is caught by the same run.
	add("/workspace/probe.txt", "workspace probe")
	add("/output/probe.txt", "output probe")
	b.WriteString("chmod 755 /crew/init.sh\n")
	lc.mustSh(ctx, t, "1001:1001", b.String())

	// The runtime's memory-dir prep: group 1002, setgid, group-writable.
	// Runs as 1001:1002 exactly as prepMemoryDirs does, because a
	// cap-dropped root cannot chown.
	lc.mustSh(ctx, t, "1001:1002",
		`for p in /crew/shared/.memory /crew/agents/*/.memory; do `+
			`chgrp -R 1002 -- "$p"; chmod -R u+rwX,g+rwXs -- "$p"; done`)
	return want
}

// assertBundleRecordsOwnership walks the raw payload tar and checks that
// REGULAR-FILE entries carry the ownership their source had.
//
// This is the half of #1714 the round-trip assertions cannot see. The
// restore extracts with --no-same-owner, because a CapDrop: ALL
// container has no CAP_CHOWN, so the ownership files end up with comes
// from the exec identity and not from the archive — a bundle recording
// uid 0 for every file would still restore to 1001:1001 and look
// perfect. What it would NOT survive is any other consumer: a
// host-side extraction, an operator unpacking the tar to inspect it, or
// a future non-docker restore target. The bundle has to describe the
// tree it actually saw.
//
// Directory and symlink headers already carried Uid/Gid before the fix,
// which is what made the bundle self-contradictory: agent-owned
// directories full of root-owned files, a state no container was in.
func assertBundleRecordsOwnership(t *testing.T, payload []byte, wantPrefix string, wantUID, wantGID int) {
	t.Helper()
	tr, err := NewTarZstReader(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("open payload: %v", err)
	}
	defer func() { _ = tr.Close() }()
	checked := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("walk payload: %v", err)
		}
		if hdr.Typeflag != tar.TypeReg || !strings.HasPrefix(hdr.Name, wantPrefix) {
			continue
		}
		checked++
		if hdr.Uid != wantUID || hdr.Gid != wantGID {
			t.Errorf("bundle entry %s records uid %d gid %d, source had %d:%d — the bundle describes a tree that never existed",
				hdr.Name, hdr.Uid, hdr.Gid, wantUID, wantGID)
		}
	}
	if checked == 0 {
		t.Fatalf("no regular-file entries under %q in the payload — nothing was asserted", wantPrefix)
	}
	t.Logf("bundle ownership verified on %d entries under %q", checked, wantPrefix)
}

// collectToPayload runs the real collector into a real bundle payload and
// hands back the extracted sections (what RestoreCrew consumes) plus the
// raw payload bytes (what the bundle actually records).
func collectToPayload(ctx context.Context, t *testing.T, ops DockerOps, crew CrewTarget, level ScopeLevel) (*ExtractedPayload, []byte) {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewTarZstWriter(&buf)
	if err != nil {
		t.Fatalf("tar writer: %v", err)
	}
	capture, err := CollectCrew(ctx, ops, w, crew, level)
	if err != nil {
		t.Fatalf("CollectCrew: %v", err)
	}
	t.Logf("captured: workspace=%d crew=%d crew-memory=%d output=%d home=%d tools=%d",
		capture.WorkspaceFiles, capture.CrewFiles, capture.CrewMemoryFiles,
		capture.OutputFiles, capture.HomeFiles, capture.ToolsFiles)
	if capture.CrewFiles == 0 {
		t.Errorf("collector captured NOTHING from %s — the crew memory tree is not in the bundle (#1713)", ContainerCrewPath)
	}
	// CrewFiles counts init.sh too, so it is not evidence of memory.
	// Against a fixture that seeds a real memory tree, the memory count
	// is what proves the section carries what the manifest will claim.
	if capture.CrewMemoryFiles == 0 {
		t.Errorf("collector counted no files inside a .memory directory, so memory_included would be false for a crew whose memory tree was just seeded")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close payload: %v", err)
	}
	payload, err := ExtractPayload(ctx, bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("ExtractPayload: %v", err)
	}
	t.Cleanup(func() { _ = payload.Close() })
	return payload, buf.Bytes()
}

// TestLive_CrewMemoryRoundTrip is the #1713 + #1714 proof: seed the real
// memory tree, back the crew up, destroy the tree, restore, and read
// every file back from inside the container — content, uid, gid, mode.
func TestLive_CrewMemoryRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cli := newLiveClient(t)
	lc := startLiveCrew(ctx, t, cli, "memroundtrip", true)
	ops := &MobyDockerOps{Client: cli}

	want := lc.seedMemory(ctx, t, []string{"alex", "robin"})

	// Record the pre-backup facts so the assertions compare against what
	// the runtime actually had, not against a hard-coded guess.
	before := map[string]fileFacts{}
	for p := range want {
		before[p] = lc.stat(ctx, t, p)
		if !before[p].exists {
			t.Fatalf("fixture did not land: %s", p)
		}
	}

	crew := CrewTarget{ID: "live-crew", Slug: "engineering", ContainerID: lc.id}
	payload, raw := collectToPayload(ctx, t, ops, crew, ScopeLevelStandard)
	// The .memory subtrees are agent-owned with the SIDECAR group; the
	// rest of /crew is agent-owned with the agent's own group. The bundle
	// has to record both correctly, independently of what the restore
	// then does with them — asserting one gid across the whole section
	// would be asserting something untrue.
	assertBundleRecordsOwnership(t, raw, "crew/"+crew.Slug+"/shared/.memory/", agentUID, sidecarGID)
	assertBundleRecordsOwnership(t, raw, "crew/"+crew.Slug+"/agents/alex/.memory/", agentUID, sidecarGID)
	assertBundleRecordsOwnership(t, raw, "crew/"+crew.Slug+"/init.sh", agentUID, agentGID)
	assertBundleRecordsOwnership(t, raw, "workspace/"+crew.Slug+"/", agentUID, agentGID)

	// Destroy the tree the way a real loss does: remove it entirely.
	lc.mustSh(ctx, t, "1001:1001", `rm -rf /crew/shared/.memory /crew/agents /crew/init.sh /workspace/probe.txt /output/probe.txt`)
	for p := range want {
		if lc.stat(ctx, t, p).exists {
			t.Fatalf("destroy step left %s behind", p)
		}
	}

	if err := RestoreCrew(ctx, ops, lc.id, crew.Slug, payload); err != nil {
		t.Fatalf("RestoreCrew: %v", err)
	}

	var failures []string
	for p, content := range want {
		got := lc.stat(ctx, t, p)
		t.Logf("%-46s before=%s  after=%s", p, before[p], got)
		if !got.exists {
			failures = append(failures, fmt.Sprintf("%s: MISSING (never restored)", p))
			continue
		}
		if got.content != content {
			failures = append(failures, fmt.Sprintf("%s: content %q, want %q", p, got.content, content))
		}
		if got.uid != before[p].uid || got.gid != before[p].gid {
			failures = append(failures, fmt.Sprintf("%s: ownership %s:%s, want %s:%s (agent cannot write its own restored data)",
				p, got.uid, got.gid, before[p].uid, before[p].gid))
		}
		if got.mode != before[p].mode {
			failures = append(failures, fmt.Sprintf("%s: mode %s, want %s", p, got.mode, before[p].mode))
		}
		if got.mtime != before[p].mtime {
			failures = append(failures, fmt.Sprintf("%s: mtime %s, want %s (restored files claim to have been written at recovery time)",
				p, got.mtime, before[p].mtime))
		}
	}

	// The .memory directories carry the crew-shared contract: group 1002
	// and setgid, so anything the agent creates later stays readable by
	// the memory sidecar. A restore that flattens this looks fine until
	// crew-shared memory silently stops working.
	for _, dir := range []string{
		"/crew/shared/.memory",
		"/crew/agents/alex/.memory",
		"/crew/agents/robin/.memory",
	} {
		got := lc.stat(ctx, t, dir)
		if !got.exists {
			failures = append(failures, fmt.Sprintf("%s: MISSING", dir))
			continue
		}
		if got.gid != fmt.Sprint(sidecarGID) {
			failures = append(failures, fmt.Sprintf("%s: gid %s, want %d (sidecar loses access to crew-shared memory)", dir, got.gid, sidecarGID))
		}
		if got.mode != "2775" {
			failures = append(failures, fmt.Sprintf("%s: mode %s, want 2775 (setgid lost: new files stop inheriting the sidecar group)", dir, got.mode))
		}
	}

	// Writability is the claim that actually matters, so assert it
	// directly rather than inferring it from the uid.
	if out, code := lc.sh(ctx, t, "1001:1001", `echo post-restore >> /crew/shared/.memory/CREW.md`); code != 0 {
		failures = append(failures, fmt.Sprintf("agent cannot append to its own restored memory: %s", strings.TrimSpace(out)))
	}

	if len(failures) > 0 {
		t.Fatalf("round trip lost data:\n  %s", strings.Join(failures, "\n  "))
	}
}

// TestLive_RestoreRefusesUnpreparedVolumes is the containment half of
// #1715. A target whose named volumes are in Docker's default state —
// root-owned 0755, which is what every crew got before the provider
// started chowning them — cannot receive the home/tools sections at all,
// because the crew container runs CapDrop: ALL and the agent cannot
// create an entry there.
//
// Pre-fix that produced an HTTP 500 whose message arrived AFTER the
// workspace and memory sections were already on disk. The requirement is
// not that the restore succeed (it cannot; nothing inside a cap-dropped
// container can chown a root-owned volume). It is that it refuse before
// touching anything, and say why.
func TestLive_RestoreRefusesUnpreparedVolumes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cli := newLiveClient(t)
	ops := &MobyDockerOps{Client: cli}

	src := startLiveCrew(ctx, t, cli, "unprepsrc", true)
	src.seedMemory(ctx, t, []string{"alex"})
	src.mustSh(ctx, t, "1001:1001", `printf 'tools probe\n' > /opt/crew-tools/probe.txt`)
	srcCrew := CrewTarget{ID: "live-unprep", Slug: "engineering", ContainerID: src.id}
	payload, _ := collectToPayload(ctx, t, ops, srcCrew, ScopeLevelStandard)

	dst := startLiveCrew(ctx, t, cli, "unprepdst", false)
	t.Logf("target volume ownership: %s",
		strings.TrimSpace(dst.mustSh(ctx, t, "0:0", `stat -c '%u:%g %a' /home/agent /opt/crew-tools`)))

	err := RestoreCrew(ctx, ops, dst.id, srcCrew.Slug, payload)
	if err == nil {
		t.Fatalf("restore into root-owned volumes reported success")
	}
	if !errors.Is(err, ErrRestorePreflight) {
		t.Fatalf("restore failed, but not as a preflight refusal — so it may have written first: %v", err)
	}
	t.Logf("refused as expected: %v", err)

	var landed []string
	for _, p := range []string{
		"/workspace/probe.txt",
		"/output/probe.txt",
		"/crew/shared/.memory/CREW.md",
		"/crew/agents/alex/.memory/AGENT.md",
	} {
		if dst.stat(ctx, t, p).exists {
			landed = append(landed, p)
		}
	}
	if len(landed) > 0 {
		t.Fatalf("restore refused but left a half-written crew behind: %s", strings.Join(landed, ", "))
	}
}

// TestLive_RestoreIntoFreshVolumes is the disaster-recovery case the
// whole feature exists for, and the other half of #1715: a DIFFERENT
// crew, a different container, prepared exactly the way the fixed
// provider prepares one (bind roots chowned to the agent; named volume
// roots chowned to the agent — the step fixBindMountOwnership now
// performs, and which nothing in the tree performed before).
//
// Everything the bundle carries has to arrive, owned by the agent.
func TestLive_RestoreIntoFreshVolumes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cli := newLiveClient(t)
	ops := &MobyDockerOps{Client: cli}

	src := startLiveCrew(ctx, t, cli, "drsource", true)
	src.seedMemory(ctx, t, []string{"alex"})
	src.mustSh(ctx, t, "1001:1001", `mkdir -p /home/agent/.config && printf 'home probe\n' > /home/agent/.config/probe.txt`)
	src.mustSh(ctx, t, "1001:1001", `printf 'tools probe\n' > /opt/crew-tools/probe.txt`)

	srcCrew := CrewTarget{ID: "live-src", Slug: "engineering", ContainerID: src.id}
	payload, _ := collectToPayload(ctx, t, ops, srcCrew, ScopeLevelStandard)

	// The DR target: a DIFFERENT crew, a different container, prepared
	// the way the fixed provider prepares one.
	dst := startLiveCrew(ctx, t, cli, "drtarget", true)
	facts := dst.mustSh(ctx, t, "0:0", `stat -c '%u:%g %a' /home/agent /opt/crew-tools`)
	t.Logf("target volume ownership before restore:\n%s", facts)

	err := RestoreCrew(ctx, ops, dst.id, srcCrew.Slug, payload)
	if err != nil {
		t.Fatalf("RestoreCrew into fresh volumes: %v", err)
	}

	var failures []string
	check := func(p, wantContent string) {
		got := dst.stat(ctx, t, p)
		if !got.exists {
			failures = append(failures, fmt.Sprintf("%s: MISSING", p))
			return
		}
		if got.content != wantContent {
			failures = append(failures, fmt.Sprintf("%s: content %q, want %q", p, got.content, wantContent))
		}
		if got.uid != fmt.Sprint(agentUID) {
			failures = append(failures, fmt.Sprintf("%s: uid %s, want %d", p, got.uid, agentUID))
		}
	}
	check("/crew/shared/.memory/CREW.md", "crew charter: ship the backup fix\n")
	check("/crew/agents/alex/.memory/AGENT.md", "agent alex identity\n")
	check("/workspace/probe.txt", "workspace probe\n")
	check("/home/agent/.config/probe.txt", "home probe\n")
	check("/opt/crew-tools/probe.txt", "tools probe\n")
	if len(failures) > 0 {
		t.Fatalf("DR restore into a fresh crew lost data:\n  %s", strings.Join(failures, "\n  "))
	}
}

// TestLive_RestoreIsAtomicAcrossSections is the other half of #1715: the
// complaint is not the 500, it is that a failed restore leaves a
// half-written crew. Point the restore at a target whose tools volume it
// cannot write, and assert that NOTHING landed — not the workspace, not
// the memory, which under the old section loop were both already on disk
// by the time the tools section failed.
// TestLive_RestoreSurvivesRootOwnedEntriesInsideAgentVolume is #1715's
// real shape, which the root-only preflight could not see.
//
// A named volume is agent-owned at its ROOT and contains root-owned
// entries left by a root postCreate step. `touch` in the root succeeds,
// so the preflight passed every section, and the apply loop wrote
// workspace, then crew-memory, then output — and only failed on home,
// fourth, where tar tried to open a root-owned file for writing as uid
// 1001. The operator was left holding a half-restored crew: exactly the
// state the atomicity comment says can no longer happen.
//
// The fix is not to make the write succeed. A cap-dropped container has
// no CAP_CHOWN and tar cannot replace an entry its identity cannot open
// — --unlink-first, the obvious candidate, is rejected outright when
// combined with --overwrite and on its own dies on the first non-empty
// directory it tries to unlink. Both were observed here rather than
// reasoned about.
//
// What has to hold is the guarantee: the restore is REFUSED, before
// anything is written, naming the path to fix. Both shapes — a
// root-owned file the section replaces, and a root-owned directory the
// section writes into — must behave that way.
func TestLive_RestoreSurvivesRootOwnedEntriesInsideAgentVolume(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cli := newLiveClient(t)
	ops := &MobyDockerOps{Client: cli}

	src := startLiveCrew(ctx, t, cli, "rootentsrc", true)
	src.seedMemory(ctx, t, []string{"alex"})
	src.mustSh(ctx, t, "1001:1001", `mkdir -p /home/agent/.config && printf 'home probe\n' > /home/agent/.config/probe.txt`)
	srcCrew := CrewTarget{ID: "live-rootent", Slug: "engineering", ContainerID: src.id}
	payload, _ := collectToPayload(ctx, t, ops, srcCrew, ScopeLevelStandard)

	dst := startLiveCrew(ctx, t, cli, "rootentdst", true)

	assertRefusedWithNothingWritten := func(t *testing.T, target *liveCrew, wantNamed string) {
		t.Helper()
		err := RestoreCrew(ctx, ops, target.id, srcCrew.Slug, payload)
		if err == nil {
			t.Fatalf("restore reported success over a path the agent cannot write")
		}
		if !errors.Is(err, ErrRestorePreflight) {
			t.Fatalf("failed, but not as a preflight refusal — so it may have written first: %v", err)
		}
		if !strings.Contains(err.Error(), wantNamed) {
			t.Errorf("refusal must name the offending path %q so it can be fixed; got %v", wantNamed, err)
		}
		t.Logf("refused as expected: %v", err)
		var landed []string
		for _, p := range []string{
			"/workspace/probe.txt",
			"/crew/shared/.memory/CREW.md",
			"/crew/agents/alex/.memory/AGENT.md",
			"/output/probe.txt",
		} {
			if target.stat(ctx, t, p).exists {
				landed = append(landed, p)
			}
		}
		if len(landed) > 0 {
			t.Fatalf("sections that precede the failing one landed anyway — the preflight still does not see this case: %s",
				strings.Join(landed, ", "))
		}
	}

	t.Run("root-owned FILE inside an agent-owned volume", func(t *testing.T) {
		// Exactly what a root postCreate leaves behind: the volume root
		// and its directories belong to the agent, one file inside does
		// not. The init container has CAP_CHOWN; the crew container does
		// not, which is why this state is not self-healing.
		runThrowaway(ctx, t, cli, dst.image, "0:0",
			[]string{"sh", "-c", `mkdir -p /mnt/home/.config &&` +
				// The DIRECTORY belongs to the agent — only the file
				// inside does not. That is what a root postCreate
				// leaves, and it is the case --unlink-first makes
				// survivable: unlinking needs write on the parent.
				` chown 1001:1001 /mnt/home/.config && chmod 755 /mnt/home/.config &&` +
				` printf 'stale\n' > /mnt/home/.config/probe.txt &&` +
				` chown 0:0 /mnt/home/.config/probe.txt && chmod 644 /mnt/home/.config/probe.txt`},
			[]mount.Mount{{Type: mount.TypeVolume, Source: dst.homeVolume, Target: "/mnt/home"}})
		before := dst.stat(ctx, t, "/home/agent/.config/probe.txt")
		if before.uid != "0" {
			t.Fatalf("fixture did not land a root-owned file: %s", before)
		}
		// The bundle carries /home/agent/.config/probe.txt, so this is a
		// file the section WILL open for writing.
		assertRefusedWithNothingWritten(t, dst, "/home/agent/.config/probe.txt")
	})

	t.Run("root-owned DIRECTORY is refused before anything is written", func(t *testing.T) {
		dst2 := startLiveCrew(ctx, t, cli, "rootdirdst", true)
		runThrowaway(ctx, t, cli, dst2.image, "0:0",
			[]string{"sh", "-c", `mkdir -p /mnt/home/.config && chown 0:0 /mnt/home/.config && chmod 755 /mnt/home/.config`},
			[]mount.Mount{{Type: mount.TypeVolume, Source: dst2.homeVolume, Target: "/mnt/home"}})

		assertRefusedWithNothingWritten(t, dst2, "/home/agent/.config")
	})

	// Writability is not the only thing tar needs. The crew section
	// restores with PreserveModes and PreserveTimes, and utime()/chmod()
	// are OWNER rights — so a .memory directory the agent can write but
	// does not own passes a writability probe and then dies mid-apply
	// with "Cannot utime: Operation not permitted", after the workspace
	// section has already landed. Found live, exactly that way.
	t.Run("a memory dir the agent can write but does not own is refused", func(t *testing.T) {
		const dstID = "notownerdst"
		dst4 := startLiveCrew(ctx, t, cli, dstID, true)
		crewRoot := "/mnt/root/output/crews/" + dstID
		// alex IS in the bundle, so this is a directory the crew section
		// writes into. Owned by the sidecar, group-writable by the agent.
		runThrowaway(ctx, t, cli, dst4.image, "0:0",
			[]string{"sh", "-c", "mkdir -p " + crewRoot + "/agents/alex/.memory" +
				" && chown -R 1001:1001 " + crewRoot + "/agents" +
				" && chown 1002:1001 " + crewRoot + "/agents/alex/.memory" +
				" && chmod 0775 " + crewRoot + "/agents/alex/.memory"},
			[]mount.Mount{{Type: mount.TypeVolume, Source: "crewship-live-root-" + dstID, Target: "/mnt/root"}})

		err := RestoreCrew(ctx, ops, dst4.id, srcCrew.Slug, payload)
		if err == nil {
			t.Fatalf("restore into a memory dir the agent does not own reported success")
		}
		if !errors.Is(err, ErrRestorePreflight) {
			t.Fatalf("must be refused in the preflight — otherwise tar fails on utime after earlier sections have landed: %v", err)
		}
		if dst4.stat(ctx, t, "/workspace/probe.txt").exists {
			t.Fatalf("the workspace section landed before the crew section failed — the mid-apply failure this exists to prevent")
		}
		t.Logf("refused as expected: %v", err)
	})

	// The other half of "exact, not broad": an unwritable path the
	// bundle never touches must NOT block the restore. A real
	// /home/agent is full of root-owned paths from feature installs, and
	// refusing over one of those would trade a half-written restore for
	// a restore that never runs.
	t.Run("an unrelated root-owned path does not block the restore", func(t *testing.T) {
		dst3 := startLiveCrew(ctx, t, cli, "unrelateddst", true)
		runThrowaway(ctx, t, cli, dst3.image, "0:0",
			[]string{"sh", "-c", `mkdir -p /mnt/home/.cache/somefeature && chown -R 0:0 /mnt/home/.cache && chmod -R 755 /mnt/home/.cache && printf 'x\n' > /mnt/home/.cache/somefeature/blob && chmod 644 /mnt/home/.cache/somefeature/blob`},
			[]mount.Mount{{Type: mount.TypeVolume, Source: dst3.homeVolume, Target: "/mnt/home"}})

		if err := RestoreCrew(ctx, ops, dst3.id, srcCrew.Slug, payload); err != nil {
			t.Fatalf("a root-owned path the bundle never writes blocked the restore: %v", err)
		}
		got := dst3.stat(ctx, t, "/home/agent/.config/probe.txt")
		if got.content != "home probe\n" {
			t.Errorf("the restore did not land: %s", got)
		}
	})
}

// TestLive_SidecarOwnedMemoryDirDoesNotFailACompletedRestore is #5.
//
// reapplyMemoryPerms runs AFTER every section has been written. A
// `.memory` directory that already exists in the target can be owned by
// the memory sidecar (uid 1002) rather than by the agent — tar does not
// chown a directory it merely extracts into — and chgrp by uid 1001 on a
// directory it does not own is EPERM. With the passes under `set -e` and
// no `|| true`, GNU find's non-zero exit aborted the script and
// RestoreCrew reported failure on a crew that had been fully and
// correctly overwritten. Re-running did not help: the same entry EPERMs
// again.
//
// The contract now: the data landing is what decides success. A
// permission shortfall is reported through ErrMemoryPermsDegraded —
// loud, but not fatal. The operator is told what to fix, not told their
// restore failed when it did not.
func TestLive_SidecarOwnedMemoryDirDoesNotFailACompletedRestore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cli := newLiveClient(t)
	ops := &MobyDockerOps{Client: cli}

	src := startLiveCrew(ctx, t, cli, "sidecarsrc", true)
	src.seedMemory(ctx, t, []string{"alex"})
	srcCrew := CrewTarget{ID: "live-sidecar", Slug: "engineering", ContainerID: src.id}
	payload, _ := collectToPayload(ctx, t, ops, srcCrew, ScopeLevelQuick)

	const dstID = "sidecardst"
	dst := startLiveCrew(ctx, t, cli, dstID, true)
	// A .memory directory the sidecar owns outright. Only a privileged
	// helper can create this state — which is exactly why the restore,
	// running inside a CapDrop: ALL container, cannot chgrp its way out
	// of it either. Reached through the root volume's known layout
	// (output/crews/<id>) rather than a host path, because the bind
	// sources are daemon-side.
	//
	// The precise shape matters, and it is deliberately a directory the
	// BUNDLE DOES NOT TOUCH. The source crew seeded only `alex`, so
	// `robin`'s memory tree exists on the target and appears in no
	// section. That distinction is the whole split:
	//
	//   - a .memory directory the section WRITES INTO and the agent does
	//     not own is caught by the preflight, with nothing written
	//     (covered by TestLive_RestoreSurvivesRootOwnedEntriesInsideAgentVolume);
	//   - one the section never touches cannot be caught there, because
	//     refusing over it would block a restore that is going to
	//     succeed — but the perms pass still walks all of /crew and
	//     still cannot chgrp it.
	//
	// Owned by the sidecar (1002) with the agent's group, which is what
	// a sidecar-created directory inherits under an agent-owned setgid
	// parent: chgrp and chmod are owner rights, so both EPERM.
	crewRoot := "/mnt/root/output/crews/" + dstID
	runThrowaway(ctx, t, cli, dst.image, "0:0",
		[]string{"sh", "-c", "mkdir -p " + crewRoot + "/agents/robin/.memory" +
			" && chown -R 1001:1001 " + crewRoot + "/agents" +
			" && chown 1002:1001 " + crewRoot + "/agents/robin/.memory" +
			" && chmod 0775 " + crewRoot + "/agents/robin/.memory"},
		[]mount.Mount{{Type: mount.TypeVolume, Source: "crewship-live-root-" + dstID, Target: "/mnt/root"}})

	err := RestoreCrew(ctx, ops, dst.id, srcCrew.Slug, payload)

	// Whatever the permission outcome, the DATA has to be there. That is
	// the assertion separating "degraded" from "failed".
	got := dst.stat(ctx, t, "/crew/agents/alex/.memory/AGENT.md")
	if got.content != "agent alex identity\n" {
		t.Fatalf("the crew memory did not land: %s (err=%v)", got, err)
	}
	if err == nil {
		t.Logf("permissions were fully re-applied even over a sidecar-owned directory")
		return
	}
	if !errors.Is(err, ErrMemoryPermsDegraded) {
		t.Fatalf("a completed restore reported a HARD failure over permissions, so the caller will roll back data that is on disk: %v", err)
	}
	if !strings.Contains(err.Error(), "robin") {
		t.Errorf("the degraded report must name the directory to fix; got %v", err)
	}
	t.Logf("reported as degraded, not failed: %v", err)
}

func TestLive_RestoreIsAtomicAcrossSections(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cli := newLiveClient(t)
	ops := &MobyDockerOps{Client: cli}

	src := startLiveCrew(ctx, t, cli, "atomicsrc", true)
	src.seedMemory(ctx, t, []string{"alex"})
	src.mustSh(ctx, t, "1001:1001", `printf 'tools probe\n' > /opt/crew-tools/probe.txt`)
	srcCrew := CrewTarget{ID: "live-src2", Slug: "engineering", ContainerID: src.id}
	payload, _ := collectToPayload(ctx, t, ops, srcCrew, ScopeLevelStandard)

	dst := startLiveCrew(ctx, t, cli, "atomicdst", true)
	// Wedge exactly one section, and deliberately not the last one:
	// /output is restored THIRD, after /workspace and /crew. Under the
	// old section loop those two were already on disk by the time a later
	// section failed, and the operator got a 500 describing a crew that
	// was half overwritten. chmod is an owner right, so the agent can do
	// this to its own bind root without any capability — which matters,
	// because a cap-dropped container cannot chown anything.
	dst.mustSh(ctx, t, "1001:1001", `chmod 555 /output`)
	t.Cleanup(func() { dst.mustSh(context.Background(), t, "1001:1001", `chmod 755 /output`) })

	err := RestoreCrew(ctx, ops, dst.id, srcCrew.Slug, payload)
	if err == nil {
		t.Fatalf("restore into an unwritable tools volume reported success")
	}
	t.Logf("restore refused as expected: %v", err)

	// The two sections that come BEFORE the wedged one. If either is
	// present, the restore wrote before it knew it could finish.
	var landed []string
	for _, p := range []string{
		"/workspace/probe.txt",
		"/crew/shared/.memory/CREW.md",
		"/crew/agents/alex/.memory/AGENT.md",
	} {
		if dst.stat(ctx, t, p).exists {
			landed = append(landed, p)
		}
	}
	if len(landed) > 0 {
		t.Fatalf("a section that is restored BEFORE the failing one landed anyway — restore is not preflight-atomic: %s",
			strings.Join(landed, ", "))
	}
}

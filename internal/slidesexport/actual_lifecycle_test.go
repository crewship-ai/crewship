//go:build slides_export_runtime

package slidesexport

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const maliciousEntry = `import {readFile} from 'node:fs/promises';
let b=await readFile('/opt/slides-export/fixture-frame');
switch(process.env.FIXTURE_MODE){
case 'truncated':b=b.subarray(0,b.length-1);break;
case 'extra':b=Buffer.concat([b,Buffer.from('extra')]);break;
case 'header_oversize':b.writeUInt32BE(0xffffffff,8);break;
case 'binary_oversize':b.writeBigUInt64BE(0xffffffffffffffffn,12);break;
case 'profile_mismatch':case 'hash_mismatch': {
const h=JSON.parse(b.subarray(20).toString());
if(process.env.FIXTURE_MODE==='profile_mismatch')h.profile_sha256='b'.repeat(64);else h.sha256='b'.repeat(64);
const body=Buffer.from(JSON.stringify(h));b=Buffer.concat([b.subarray(0,20),body]);b.writeUInt32BE(body.length,8);break;}
case 'hold':await new Promise(resolve=>setTimeout(resolve,200000));break;
}
process.stdout.write(b);
`

// These are deliberately untrusted fixture workers derived from the exact built
// image. Their framing failures qualify host admission, not renderer fidelity.
func TestActualExportImageLifecycle(t *testing.T) {
	if os.Getenv("CREWSHIP_JOB_ID") != "" {
		t.Fatal("actual lifecycle requires authorized isolated CI, never bypass the local gateway")
	}
	image := os.Getenv("CREWSHIP_TEST_SLIDES_EXPORT_IMAGE")
	if !imagePattern.MatchString(image) {
		t.Fatal("actual immutable image required")
	}
	got := actualDocker(t, "image", "inspect", "--format", "{{.Id}}", "crewship-slides-export:ci")
	if strings.TrimSpace(got) != image {
		t.Fatal("fixture base tag differs from exact tested image")
	}
	raw, err := os.ReadFile("../../tools/slides-export/testdata/supported.json")
	if err != nil {
		t.Fatal(err)
	}
	in, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	b := Binding{Mode: "report", PageID: in.Deck.DeckID, Revision: in.Revision, ContentHash: in.ContentHash}
	results := map[string]any{"production_image": image, "controlled_late_create_window_ms": 250, "arbitrarily_delayed_daemon_atomic_cleanup": false}
	// A test-owned foreign container must survive every runner cleanup.
	foreign := newIntent("foreign_ci")
	foreign.ContainerID = strings.TrimSpace(actualDocker(t, createArgs(foreign.Name, foreign.Token, foreign.OwnerID, image, "report", "")...))
	foreign.Phase = "created"
	if !digestPattern.MatchString(foreign.ContainerID) {
		t.Fatal("foreign fixture ID")
	}
	t.Cleanup(func() {
		if c, e := inspectOwned(context.Background(), foreign); e == nil && c != nil {
			_ = removeOwned(context.Background(), c)
		} else if e != nil {
			t.Error("foreign fixture cleanup ownership unresolved")
		}
	})
	for _, mode := range []string{"truncated", "extra", "header_oversize", "binary_oversize", "profile_mismatch", "hash_mismatch", "hold"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
			defer cancel()
			for path, data := range map[string][]byte{"entry.mjs": []byte(maliciousEntry), "frame": fixtureFrame(t, b, nil), "Dockerfile": []byte("FROM crewship-slides-export:ci\nCOPY --chown=1001:1001 entry.mjs /opt/slides-export/entry.mjs\nCOPY --chown=1001:1001 frame /opt/slides-export/fixture-frame\nARG MODE\nENV FIXTURE_MODE=$MODE\n")} {
				if e := os.WriteFile(filepath.Join(dir, path), data, 0600); e != nil {
					t.Fatal(e)
				}
			}
			iid := filepath.Join(dir, "iid")
			out, logs := &capture{limit: 1 << 20, cancel: cancel}, &capture{limit: 1 << 20, cancel: cancel}
			cmd := exec.CommandContext(ctx, "docker", "build", "--progress=plain", "--build-arg", "MODE="+mode, "--iidfile", iid, dir)
			cmd.Stdout = out
			cmd.Stderr = logs
			cmd.WaitDelay = time.Second
			if e := cmd.Run(); e != nil {
				t.Fatal("trusted fixture image build failed", e)
			}
			id, e := os.ReadFile(iid)
			if e != nil || !imagePattern.MatchString(strings.TrimSpace(string(id))) {
				t.Fatal("fixture immutable image ID", e)
			}
			fixtureImage := strings.TrimSpace(string(id))
			t.Cleanup(func() { actualDocker(t, "image", "rm", fixtureImage) })
			r := &Runner{Image: fixtureImage, OwnerID: "actual_ci", JournalDir: filepath.Join(t.TempDir(), "journal")}
			defer r.Close()
			runCtx, stop := context.WithCancel(t.Context())
			defer stop()
			done := make(chan error, 1)
			go func() { _, e := r.Run(runCtx, raw, b); done <- e }()
			if mode == "hold" {
				intent := waitActualIntent(t, r, true)
				profile := verifyActualIsolation(t, intent.ContainerID)
				results["actual_isolation"] = profile
				stop()
			}
			select {
			case e := <-done:
				if mode == "hold" {
					if !errors.Is(e, context.Canceled) || errors.Is(e, ErrCleanup) {
						t.Fatal("actual running cancellation", e)
					}
				} else if !errors.Is(e, ErrProtocol) {
					t.Fatal("actual malformed frame not rejected", e)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("actual lifecycle exceeded bounded test deadline")
			}
			if intents, e := r.journal.list(); e != nil || len(intents) != 0 {
				t.Fatal("owned settled containers/intents remain", e)
			}
			results[mode] = map[string]any{"result": "PASS", "fixture_image": fixtureImage}
		})
	}
	// Exercise actual Docker creation after parent cancellation, plus a controlled
	// unacknowledged late create. This test-owned CLI wrapper never changes daemon
	// configuration or DOCKER_HOST; the production independent deadline still runs.
	realDocker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"cancel_create", "late_unacknowledged_create"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			script := `#!/bin/sh
if [ "$1" = create ]; then
 : > "$CREWSHIP_TEST_ACTUAL_CREATE_DIR/entered"
 if [ "$CREWSHIP_TEST_ACTUAL_CREATE_MODE" = late_unacknowledged_create ]; then
  (sleep 0.25; "$CREWSHIP_TEST_ACTUAL_DOCKER" "$@" > "$CREWSHIP_TEST_ACTUAL_CREATE_DIR/created" 2> "$CREWSHIP_TEST_ACTUAL_CREATE_DIR/diagnostic" && : > "$CREWSHIP_TEST_ACTUAL_CREATE_DIR/finished") </dev/null >/dev/null 2>&1 &
  exit 1
 fi
 sleep 0.25
fi
exec "$CREWSHIP_TEST_ACTUAL_DOCKER" "$@"
`
			if e := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); e != nil {
				t.Fatal(e)
			}
			t.Setenv("CREWSHIP_TEST_ACTUAL_DOCKER", realDocker)
			t.Setenv("CREWSHIP_TEST_ACTUAL_CREATE_DIR", dir)
			t.Setenv("CREWSHIP_TEST_ACTUAL_CREATE_MODE", mode)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			r := &Runner{Image: image, OwnerID: "actual_ci", JournalDir: filepath.Join(t.TempDir(), "journal")}
			defer r.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, e := r.Run(ctx, raw, b); done <- e }()
			waitActualFile(t, filepath.Join(dir, "entered"))
			if mode == "cancel_create" {
				cancel()
			}
			select {
			case e := <-done:
				if mode == "cancel_create" {
					if !errors.Is(e, context.Canceled) || errors.Is(e, ErrCleanup) {
						t.Fatal("actual late acknowledged create", e)
					}
				} else if !errors.Is(e, ErrCleanup) {
					t.Fatal("uncertain actual create falsely settled", e)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("actual create/cancel cleanup exceeded bounded window")
			}
			intents, e := r.journal.list()
			if e != nil {
				t.Fatal(e)
			}
			if mode == "cancel_create" {
				if len(intents) != 0 {
					t.Fatal("acknowledged create retained")
				}
			} else {
				if len(intents) != 1 || intents[0].Phase != "uncertain" {
					t.Fatal("uncertainty not retained")
				}
				v := intents[0]
				t.Cleanup(func() { uncertainJobs.Delete(v.Name) })
				waitActualFile(t, filepath.Join(dir, "finished"))
				created, readErr := os.ReadFile(filepath.Join(dir, "created"))
				if readErr != nil || !digestPattern.MatchString(strings.TrimSpace(string(created))) {
					t.Fatal("controlled late producer did not complete a valid create", readErr)
				}
				if c, e := inspectOwned(t.Context(), v); e != nil || c != nil {
					t.Fatal("controlled late create did not eventually clean", e)
				}
				if _, e = r.Acquire(t.Context()); !errors.Is(e, ErrCleanup) {
					t.Fatal("uncertain intent admitted work", e)
				}
				// The test owns the completed child and knows this controlled producer is
				// quiescent. This does not infer arbitrary real-daemon quiescence.
				if e = r.AcknowledgeCleanup(t.Context(), v.Name, "ci_controlled_producer_completed", true); e != nil {
					t.Fatal(e)
				}
			}
			results[mode] = "PASS"
		})
	}
	if c, e := inspectOwned(t.Context(), foreign); e != nil || c == nil {
		t.Fatal("unrelated owned foreign fixture did not survive", e)
	}
	results["foreign_container_survived"] = true
	evidence := os.Getenv("CREWSHIP_TEST_SLIDES_EXPORT_EVIDENCE")
	if evidence == "" {
		t.Fatal("owned evidence directory required")
	}
	if e := os.MkdirAll(evidence, 0700); e != nil {
		t.Fatal(e)
	}
	encoded, _ := json.MarshalIndent(results, "", "  ")
	if e := os.WriteFile(filepath.Join(evidence, "image-lifecycle.json"), append(encoded, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
}
func actualDocker(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, logs := &capture{limit: 1 << 20, cancel: cancel}, &capture{limit: 1 << 20, cancel: cancel}
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = out
	cmd.Stderr = logs
	cmd.WaitDelay = time.Second
	if e := cmd.Run(); e != nil {
		t.Fatalf("actual Docker helper %s failed: %v", args[0], e)
	}
	return out.buffer.String()
}
func waitActualFile(t *testing.T, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		if _, e := os.Stat(path); e == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("controlled actual lifecycle marker timeout")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func waitActualIntent(t *testing.T, r *Runner, running bool) cleanupIntent {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	for {
		r.mu.Lock()
		j := r.journal
		r.mu.Unlock()
		if j != nil {
			if intents, e := j.list(); e == nil && len(intents) == 1 && intents[0].Phase == "created" {
				v := intents[0]
				if !running {
					return v
				}
				if strings.TrimSpace(actualDocker(t, "inspect", "--format", "{{.State.Running}}", v.ContainerID)) == "true" {
					return v
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("actual container never entered running state")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func verifyActualIsolation(t *testing.T, id string) map[string]any {
	t.Helper()
	var rows []struct {
		Config struct {
			User string
			Cmd  []string
		}
		HostConfig struct {
			NetworkMode                             string
			ReadonlyRootfs                          bool
			CapDrop                                 []string
			SecurityOpt                             []string
			Memory, MemorySwap, NanoCpus, PidsLimit int64
			Tmpfs                                   map[string]string
			LogConfig                               struct{ Type string }
		}
	}
	if json.Unmarshal([]byte(actualDocker(t, "inspect", id)), &rows) != nil || len(rows) != 1 {
		t.Fatal("actual isolation inspection")
	}
	x := rows[0]
	if x.Config.User != "1001:1001" || x.HostConfig.NetworkMode != "none" || !x.HostConfig.ReadonlyRootfs || x.HostConfig.Memory != 1<<30 || x.HostConfig.MemorySwap != 1<<30 || x.HostConfig.NanoCpus != 1e9 || x.HostConfig.PidsLimit != 64 || x.HostConfig.LogConfig.Type != "none" || !strings.Contains(strings.Join(x.HostConfig.CapDrop, ","), "ALL") || !strings.Contains(strings.Join(x.HostConfig.SecurityOpt, ","), "no-new-privileges") || !strings.Contains(x.HostConfig.Tmpfs["/tmp"], "noexec") || !strings.Contains(strings.Join(x.Config.Cmd, " "), "--max-old-space-size=512") {
		t.Fatal("actual container profile differs from approved limits")
	}
	return map[string]any{"user": x.Config.User, "network": x.HostConfig.NetworkMode, "readonly": x.HostConfig.ReadonlyRootfs, "memory": x.HostConfig.Memory, "memory_swap": x.HostConfig.MemorySwap, "nano_cpus": x.HostConfig.NanoCpus, "pids": x.HostConfig.PidsLimit, "cap_drop": x.HostConfig.CapDrop, "security_opt": x.HostConfig.SecurityOpt, "tmpfs": x.HostConfig.Tmpfs, "logs": x.HostConfig.LogConfig.Type}
}

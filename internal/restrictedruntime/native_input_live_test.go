//go:build linux && restrictedruntime_live

package restrictedruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This acceptance test uses only journaled, uniquely named synthetic objects.
// The image must include the actual native worker verifier and hold runner.
func TestLiveNativeInputFreezeReadonlyAndRestartCleanup(t *testing.T) {
	if os.Getenv("CREWSHIP_RESTRICTED_LIVE") != "1" {
		t.Fatal("explicit disposable Docker acceptance gate required")
	}
	image := os.Getenv("CREWSHIP_RESTRICTED_IMAGE")
	if image == "" {
		t.Fatal("owned acceptance image required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	d := Docker{Image: image}
	root := filepath.Join(t.TempDir(), "catalog")
	data := []byte("H1 binary project canary\x00\xff")
	sum := sha256.Sum256(data)
	manifest, err := NewNativeInputManifest([]NativeInputFile{{VersionID: "version1", Name: "nested/input.bin", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}})
	if err != nil {
		t.Fatal(err)
	}
	p := Plan{Workspace: "synthetic-workspace", Principal: "synthetic-human", Agent: "synthetic-agent", Scope: "synthetic-scope", Attempt: "synthetic-attempt", Revision: "1", NativeSandbox: NativeSandboxFingerprint(), NativeInputs: manifest}
	p.Mounts = []Mount{{Resource: NativeInputResource(p), Target: NativeInputTarget, ReadOnly: true}}
	source := func(context.Context, Plan) ([]NativeInputData, error) {
		return []NativeInputData{{VersionID: "version1", Content: data}}, nil
	}
	c, err := NewFrozenNativeCatalog(root, d, source)
	if err != nil {
		t.Fatal(err)
	}
	consumer := ""
	t.Cleanup(func() {
		clean, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if consumer != "" {
			_ = d.remove(clean, consumer)
		}
		if e := c.ReleaseNativeInputs(clean, p); e != nil {
			t.Errorf("owned fixture cleanup: %v", e)
		}
	})
	volume, err := c.Volume(ctx, p, p.Mounts[0])
	if err != nil {
		t.Fatalf("stage real Docker snapshot: %v", err)
	}
	r, err := c.load(p.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	imageID, err := d.image(ctx)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"create", "--pull=never", "--name", r.Populator + "-consumer", "--user", "1002:1002", "--read-only", "--network", "none", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--restart", "no", "--memory", "134217728", "--memory-swap", "134217728", "--pids-limit", "16", "--entrypoint", "/opt/crewship-runner", "--mount", "type=volume,source=" + volume + ",target=" + NativeInputTarget + ",readonly,volume-nocopy"}
	args = append(args, "--label", labelPrefix+"attempt="+p.Attempt, "--label", labelPrefix+"plan="+p.fingerprint(), "--label", labelPrefix+"input-owner="+c.owner, imageID, "hold")
	raw, err := d.call(ctx, nil, args...)
	if err != nil {
		t.Fatal(err)
	}
	consumer = strings.TrimSpace(string(raw))
	if _, err = d.call(ctx, nil, "start", consumer); err != nil {
		t.Fatal(err)
	}
	if err = c.FreezeNativeInputs(ctx, p, consumer); err != nil {
		t.Fatalf("freeze actual snapshot: %v", err)
	}
	record, err := c.load(p.Attempt)
	if err != nil || record.State != "frozen" {
		t.Fatalf("durable freeze: %v %+v", err, record)
	}
	raw, err = d.call(ctx, nil, "ps", "-aq", "--filter", "name=^/"+r.Populator+"$")
	if err != nil || len(strings.Fields(string(raw))) != 0 {
		t.Fatal("writer survived freeze")
	}
	if _, err = d.call(ctx, nil, "exec", "--user", "1001:1001", consumer, "sh", "-c", "printf changed > /data/project-inputs/version1/nested/input.bin"); err == nil {
		t.Fatal("readonly source accepted mutation")
	}
	encoded, _ := json.Marshal(manifest)
	if _, err = d.call(ctx, encoded, "exec", "-i", "--user", "1001:1001", consumer, "/opt/crewship-native-runner", "verify-project-inputs"); err != nil {
		t.Fatal("binary source changed or verifier failed")
	}
	if err = c.ReleaseNativeInputs(ctx, p); err == nil {
		t.Fatal("release accepted active consumer")
	}
	if err = d.remove(ctx, consumer); err != nil {
		t.Fatal(err)
	}
	consumer = ""
	restarted, err := NewFrozenNativeCatalog(root, d, source)
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.ReconcileNativeInputs(ctx); err != nil {
		t.Fatalf("restart orphan reconciliation: %v", err)
	}
	raw, err = d.call(ctx, nil, "volume", "ls", "-q", "--filter", "name=^"+volume+"$")
	if err != nil || len(strings.Fields(string(raw))) != 0 {
		t.Fatal("orphan snapshot survived reconciliation")
	}
	if err = restarted.ReconcileNativeInputs(ctx); err != nil {
		t.Fatalf("idempotent reconciliation: %v", err)
	}
}

//go:build linux && restrictedruntime_live

package restrictedruntime

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

type liveFixture struct {
	t                   *testing.T
	d                   Docker
	a                   *fixtureAuthority
	cat                 catalogMap
	m                   *Manager
	ctx                 context.Context
	prefix              string
	volumes, containers []string
}

func live(t *testing.T) *liveFixture {
	t.Helper()
	if os.Getenv("CREWSHIP_RESTRICTED_LIVE") != "1" {
		t.Fatal("live gate requires owned disposable Docker fixtures: scripts/restricted-runtime-probe/run.sh")
	}
	image := os.Getenv("CREWSHIP_RESTRICTED_IMAGE")
	if image == "" {
		t.Fatal("acceptance image required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	var r [5]byte
	_, _ = rand.Read(r[:])
	f := &liveFixture{t: t, d: Docker{Image: image}, ctx: ctx, prefix: hex.EncodeToString(r[:]), cat: catalogMap{}, a: &fixtureAuthority{plans: map[string]Plan{}, secrets: map[string]map[string]string{}, denied: map[string]bool{}, ttl: 15 * time.Second}}
	dir := filepath.Join(t.TempDir(), "state")
	var e error
	f.m, e = New(dir, f.d, f.a, f.cat, Limits{128 << 20, 500000000, 48})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_ = f.m.Close()
		clean, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		for _, id := range f.containers {
			_ = f.d.remove(clean, id)
		}
		for _, v := range f.volumes {
			_, _ = f.d.call(clean, nil, "volume", "rm", v)
		}
		cancel()
	})
	return f
}
func (f *liveFixture) must(input []byte, args ...string) []byte {
	f.t.Helper()
	b, e := f.d.call(f.ctx, input, args...)
	if e != nil {
		f.t.Fatalf("Docker %s: %v", args[0], e)
	}
	return b
}
func (f *liveFixture) seed(p Plan, r string, files map[string]string) string {
	f.t.Helper()
	v := "crewship-rtest-" + f.prefix + "-" + r
	args := []string{"volume", "create"}
	for k, val := range resourceLabels(p, r) {
		args = append(args, "--label", k+"="+val)
	}
	args = append(args, v)
	f.must(nil, args...)
	f.volumes = append(f.volumes, v)
	f.cat[r] = v
	base := []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--memory", "32m", "--pids-limit", "16", "--mount", "type=volume,src=" + v + ",dst=/data,volume-nocopy", "--entrypoint", "sh"}
	f.must(nil, append(append([]string{}, base[:2]...), append([]string{"--user", "0:0", "--cap-add", "CHOWN"}, append(base[2:], f.d.Image, "-c", "chown 1001:1001 /data")...)...)...)
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for name, value := range files {
		if e := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(value))}); e != nil {
			f.t.Fatal(e)
		}
		_, _ = tw.Write([]byte(value))
	}
	_ = tw.Close()
	args = append([]string{"run", "--rm", "-i", "--user", "1001:1001"}, base[2:]...)
	args = append(args, f.d.Image, "-c", "tar -xf - -C /data")
	f.must(b.Bytes(), args...)
	return v
}
func (f *liveFixture) start(h string, p Plan, secret string) *Session {
	f.t.Helper()
	f.a.mu.Lock()
	f.a.plans[h] = p
	f.a.secrets[h] = map[string]string{"direct": secret}
	f.a.mu.Unlock()
	start := time.Now()
	s, e := f.m.Start(f.ctx, h)
	if e != nil {
		f.t.Fatal(e)
	}
	f.containers = append(f.containers, s.ID())
	for time.Since(start) < 5*time.Second {
		out, e := s.Output(f.ctx)
		if e == nil && strings.Contains(out, "RESTRICTED_READY") {
			f.t.Logf("startup_ms=%.3f", float64(time.Since(start).Microseconds())/1000)
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatal("no actual UID-1001 ready marker")
	return nil
}
func (f *liveFixture) shell(s *Session, script string) string {
	f.t.Helper()
	return string(f.must(nil, "exec", "--user", "1001:1001", s.ID(), "sh", "-c", script))
}

func TestLiveIsolationAndRecovery(t *testing.T) {
	f := live(t)
	a := testPlan()
	a.Attempt = "a1"
	a.Mounts = []Mount{{"a", "/data/private", false}, {"share", "/data/shared", true}}
	b := a
	b.Principal = "h2"
	b.Scope = "h2-chat1"
	b.Attempt = "b1"
	b.OriginID = "chat2"
	b.Mounts = []Mount{{"b", "/data/private", false}, {"share-b", "/data/shared", true}}
	// Separate approved immutable snapshots: no writable alias to either share.
	f.seed(a, "a", map[string]string{"own": "A_DATA", "memory": "A_MEMORY", "checkpoint": "A_CHECKPOINT"})
	f.seed(a, "share", map[string]string{"read": "SHARED_DATA"})
	f.seed(b, "b", map[string]string{"own": "B_DATA", "memory": "B_MEMORY", "checkpoint": "B_CHECKPOINT"})
	f.seed(b, "share-b", map[string]string{"read": "SHARED_DATA"})
	a.Command = []string{"sh", "-c", `set -eu; test "$(id -u)" = 1001; test "$DIRECT_TOKEN" = "$(cat /secrets/direct)"; echo "$DIRECT_TOKEN"; printf '%s' "$DIRECT_TOKEN" > /data/private/private.log; printf '%s' "$DIRECT_TOKEN" > /data/private/secret-checkpoint; exec sleep 3600`}
	b.Command = a.Command
	sa := f.start("handle-a", a, "synthetic-A-direct-8c062dd0")
	sb := f.start("handle-b", b, "synthetic-B-direct-67c3a00b")
	for _, s := range []*Session{sa, sb} {
		if got := f.shell(s, `set -eu; deny() { if "$@"; then exit 91; fi; }; cat /data/shared/read; test ! -e /crew; test ! -e /var/run/docker.sock; test ! -e /run/docker.sock; test ! -e /data/foreign; deny sh -c 'printf bad >> /data/shared/read'; ln -s /data/shared/read /home/agent/shared-link; deny sh -c 'printf bad >> /home/agent/shared-link'; deny ln /data/shared/read /data/private/alias; deny mount -o remount,rw /data/shared; test "$(cat /data/shared/read)" = SHARED_DATA; echo boundaries-ok`); !strings.Contains(got, "boundaries-ok") {
			t.Fatal("no positive control")
		}
		raw := f.must(nil, "inspect", s.ID())
		if bytes.Contains(raw, []byte("synthetic-")) {
			t.Fatal("secret in Docker config")
		}
		f.checkProfile(s)
	}
	// B observes its complete mounted state, process environment, argv and fd/root
	// paths. Report only a boolean so even a regression does not print secrets.
	attack := `set -eu; deny() { if "$@"; then exit 91; fi; }; test "$(cat /data/private/own)" = B_DATA; test "$(cat /data/private/memory)" = B_MEMORY; test "$(cat /data/private/checkpoint)" = B_CHECKPOINT; needle=$(printf 'synthetic-%s-direct' A); if grep -R -a -q "$needle\|A_MEMORY\|A_CHECKPOINT\|A_DATA" /data /home/agent /secrets /tmp 2>/dev/null; then exit 92; fi; for p in /proc/[0-9]*; do for x in environ cmdline root/data/private/private.log; do if cat "$p/$x" 2>/dev/null | grep -a -q "$needle"; then exit 93; fi; done; for fd in "$p"/fd/*; do if test -f "$fd"; then if timeout 1 cat "$fd" 2>/dev/null | grep -a -q "$needle"; then exit 94; fi; fi; done; done; deny ls /proc/1/root/var/run/docker.sock; deny cat /data/private/../../data/foreign/own; echo foreign-denied`
	if !strings.Contains(f.shell(sb, attack), "foreign-denied") {
		t.Fatal("foreign canary")
	}
	// Prove the detector is live: inject only a synthetic foreign canary into
	// B's own tmpfs, require rejection, then remove it and pass again.
	f.must([]byte("synthetic-A-direct-positive-control"), "exec", "-i", sb.ID(), "sh", "-c", "cat > /tmp/foreign-control")
	if _, e := f.d.call(f.ctx, nil, "exec", "--user", "1001:1001", sb.ID(), "sh", "-c", attack); e == nil {
		t.Fatal("negative detector missed injected foreign canary")
	}
	f.must(nil, "exec", sb.ID(), "rm", "/tmp/foreign-control")
	if !strings.Contains(f.shell(sb, attack), "foreign-denied") {
		t.Fatal("detector did not recover")
	}

	out, e := sa.Output(f.ctx)
	if e != nil || strings.Contains(out, "synthetic-A") || !strings.Contains(out, "[REDACTED]") {
		t.Fatal("direct credential log redaction failed")
	}
	f.shell(sa, `set -eu; printf restored > /data/private/persisted; ln -s /data/foreign/own /data/private/escape; test ! -e /data/private/escape`)
	sa.Stop("recovery-test")
	if sa.Record().Status != "terminated" {
		t.Fatal(sa.Record())
	}
	f.must(nil, "rm", sa.ID())
	a.Attempt = "a2"
	a.Generation = 2
	sa2 := f.start("handle-a2", a, "synthetic-A-fresh-b063971a")
	if got := f.shell(sa2, `set -eu; cat /data/private/persisted; test "$(cat /secrets/direct)" = synthetic-A-fresh-b063971a; test ! -e /data/foreign; echo recovery-ok`); !strings.Contains(got, "restoredrecovery-ok") {
		t.Fatal("restore lost own state")
	}
	// A changed provenance revision cannot silently reuse old memory/checkpoints.
	changed := a
	changed.Attempt = "a3"
	changed.Revision = "r2"
	f.a.mu.Lock()
	f.a.plans["changed"] = changed
	f.a.secrets["changed"] = map[string]string{"direct": "synthetic-fresh"}
	f.a.mu.Unlock()
	if _, e := f.m.Start(f.ctx, "changed"); e == nil {
		t.Fatal("broader provenance restored")
	}
	// Foreign volume with otherwise plausible target is rejected by labels.
	if e := f.d.checkVolume(f.ctx, a, a.Mounts[0], f.cat["b"]); e == nil {
		t.Fatal("foreign resource labels accepted")
	}
	removed := a
	removed.Credentials = nil
	if e := f.d.checkVolume(f.ctx, removed, removed.Mounts[0], f.cat["a"]); e == nil {
		t.Fatal("credential removal restored tainted checkpoint without revision bump")
	}
	f.cat["a"] = f.cat["b"]
	a.Attempt = "a4"
	f.a.mu.Lock()
	f.a.plans["foreign-volume"] = a
	f.a.secrets["foreign-volume"] = map[string]string{"direct": "synthetic-fresh"}
	f.a.mu.Unlock()
	if _, e := f.m.Start(f.ctx, "foreign-volume"); e == nil {
		t.Fatal("foreign volume attached")
	}
	f.measure(sa2)
}

func (f *liveFixture) checkProfile(s *Session) {
	f.t.Helper()
	raw := f.must(nil, "inspect", s.ID())
	var rows []struct {
		Config struct {
			User string
			Env  []string
		}
		HostConfig struct {
			NetworkMode, PidMode, IpcMode           string
			Privileged, ReadonlyRootfs              bool
			CapAdd, CapDrop, GroupAdd, ExtraHosts   []string
			Memory, MemorySwap, NanoCpus, PidsLimit int64
			SecurityOpt                             []string
			Devices                                 []any
		}
		Mounts []struct {
			Type, Name, Destination string
			RW                      bool
		}
	}
	if e := json.Unmarshal(raw, &rows); e != nil {
		f.t.Fatal(e)
	}
	r := rows[0]
	h := r.HostConfig
	if r.Config.User != "1001:1001" || h.NetworkMode != "none" || h.PidMode != "" || h.IpcMode != "private" || h.Privileged || !h.ReadonlyRootfs || len(h.CapAdd) > 0 || len(h.GroupAdd) > 0 || len(h.ExtraHosts) > 0 || len(h.Devices) > 0 || h.Memory != 128<<20 || h.MemorySwap != h.Memory || h.NanoCpus != 500000000 || h.PidsLimit != 48 || strings.Join(h.CapDrop, ",") != "ALL" || !strings.Contains(strings.Join(h.SecurityOpt, ","), "no-new-privileges") {
		f.t.Fatal("unexpected runtime profile")
	}
	expected := map[string]Mount{}
	for _, m := range s.plan.Mounts {
		expected[m.Target] = m
	}
	for _, m := range r.Mounts {
		want, ok := expected[m.Destination]
		if !ok || m.Type != "volume" || m.RW == want.ReadOnly {
			f.t.Fatalf("unexpected mount %s", m.Destination)
		}
		delete(expected, m.Destination)
	}
	if len(expected) != 0 {
		f.t.Fatal("missing mounts")
	}
	info := f.shell(s, `cat /proc/self/mountinfo`)
	if strings.Contains(info, "docker.sock") || strings.Contains(info, " /crew ") {
		f.t.Fatal("forbidden mount alias")
	}
	f.t.Logf("mountinfo_entries=%d", strings.Count(info, "\n"))
}
func (f *liveFixture) measure(s *Session) {
	f.t.Helper()
	b := f.must(nil, "stats", "--no-stream", "--format", "{{json .}}", s.ID())
	f.t.Logf("docker_stats=%s", bytes.TrimSpace(b))
	f.t.Logf("cgroup_memory=%s", strings.TrimSpace(f.shell(s, `cat /sys/fs/cgroup/memory.current; cat /sys/fs/cgroup/memory.peak`)))
}

func TestLiveRevocationAndAdmission(t *testing.T) {
	f := live(t)
	p := testPlan()
	p.Mounts = nil
	p.Command = []string{"sh", "-c", `exec 3</secrets/direct; (while :; do cat <&3 >/dev/null; sleep 0.05; done) & echo FD_READY; wait`}
	s := f.start("h", p, "synthetic-open-fd")
	for i := 0; i < 100; i++ {
		out, e := s.Output(f.ctx)
		if e == nil && strings.Contains(out, "FD_READY") {
			break
		}
		if i == 99 {
			t.Fatal("FD fixture never opened")
		}
		time.Sleep(10 * time.Millisecond)
	}
	at := time.Now()
	f.a.revoke("h")
	s.Stop("grant_revoked")
	<-s.Done()
	if s.Record().Status != "terminated" || time.Since(at) > 2*time.Second {
		t.Fatalf("termination status=%s elapsed=%s", s.Record().Status, time.Since(at))
	}
	if _, e := s.Output(f.ctx); e == nil {
		t.Fatal("revoked output allowed")
	}
	if _, e := f.m.Start(f.ctx, "h"); e == nil {
		t.Fatal("revoked queued run started")
	}
	t.Logf("revoke_to_confirmed_stop_ms=%.3f", float64(time.Since(at).Microseconds())/1000)
	p.Attempt = "expires"
	f.a.ttl = 1500 * time.Millisecond
	s2 := f.start("short", p, "synthetic-expiry")
	start := time.Now()
	select {
	case <-s2.Done():
	case <-time.After(4 * time.Second):
		t.Fatal("watchdog missed expiry")
	}
	if s2.Record().Status != "terminated" {
		t.Fatal(s2.Record())
	}
	t.Logf("lease_expiry_stop_ms=%.3f", float64(time.Since(start).Microseconds())/1000)
	// Grant revoked while secret resolution runs: final admission denies release.
	f.a.ttl = 15 * time.Second
	p.Attempt = "last-check"
	f.a.plans["last"] = p
	f.a.secrets["last"] = map[string]string{"direct": "synthetic-last"}
	f.a.onSecrets = func() { f.a.denied["last"] = true }
	if _, e := f.m.Start(f.ctx, "last"); e == nil {
		t.Fatal("last-moment revocation lost")
	}
	f.a.onSecrets = nil
}

// Host reachability uses a real owned TCP listener as a positive control. The
// offline profile exposes only loopback inside each runtime, including sidecar.
func TestLiveNetworkAndSidecar(t *testing.T) {
	f := live(t)
	p := testPlan()
	p.Mounts = nil
	s := f.start("network", p, "synthetic-agent-direct")
	listener, e := net.Listen("tcp4", "0.0.0.0:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "owned-host-canary") }), ReadHeaderTimeout: time.Second}
	go server.Serve(listener)
	defer server.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	response, e := http.Get(fmt.Sprintf("http://127.0.0.1:%d", port))
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if !strings.Contains(f.shell(s, fmt.Sprintf(`set -eu; deny() { if "$@"; then exit 91; fi; }; test "$(ls /sys/class/net)" = lo; deny nc -z -w 1 172.17.0.1 %d; deny nc -z -w 1 192.0.2.1 443; deny nc -z -w 1 ::1 %d; deny nslookup forbidden.invalid; echo network-denied`, port, port)), "network-denied") {
		t.Fatal("network leak")
	}
	// Two actual sidecars, two people and the same agent. Each namespace owns
	// its own upstream and key; even a real token from B must fail against A.
	own := f.sidecar(s, p, "A")
	p2 := p
	p2.Principal = "h2"
	p2.Scope = "h2-chat"
	p2.Attempt = "network-b"
	p2.OriginID = "chat2"
	other := f.start("network-b", p2, "synthetic-B-agent-direct")
	foreign := f.sidecar(other, p2, "B")
	for _, pair := range []struct {
		s              *Session
		token, account string
	}{{s, own, "A"}, {other, foreign, "B"}} {
		result := f.shell(pair.s, "wget -q -O - --header='Authorization: Bearer "+pair.token+"' http://127.0.0.1:9119/llm/openai-compat/v1/models")
		if !strings.Contains(result, `"ok":true`) || !strings.Contains(result, `"account":"`+pair.account+`"`) {
			t.Fatal("authorized sidecar wrong account")
		}
	}
	before := f.shell(s, `wget -q -O - http://127.0.0.1:9120/count`)
	for _, token := range []string{foreign, "synthetic-forged", internaltoken.DeriveLLMRunRouteToken("synthetic-route-key-"+f.prefix+"-A-"+p.Attempt, p.Agent, p.Attempt) + internaltoken.RouteFingerprintDelimiter + "stale-config"} {
		f.shell(s, "if wget -q -O /dev/null --header='Authorization: Bearer "+token+"' http://127.0.0.1:9119/llm/openai-compat/v1/models; then exit 91; fi")
	}
	f.shell(s, `set -eu; deny() { if "$@"; then exit 91; fi; }; deny wget -q -O /dev/null http://127.0.0.1:9119/llm/openai-compat/v1/models; deny wget -q -O /dev/null http://127.0.0.1:9119/memory/read; deny wget -q -O /dev/null http://127.0.0.1:9119/assignments; deny wget -q -O /dev/null http://127.0.0.1:9119/credentials; deny cat /broker/private; needle=$(printf 'synthetic-sidecar-%s' A); for p in /proc/[0-9]*; do if cat "$p/environ" "$p/cmdline" 2>/dev/null | grep -q "$needle"; then exit 92; fi; done`)
	if after := f.shell(s, `wget -q -O - http://127.0.0.1:9120/count`); after != before {
		t.Fatal("denied request reached credential upstream")
	}
	// B cannot see A's broker or private synthetic key through the same port,
	// filesystem or PID paths. Its successful account=B control is above.
	f.shell(other, `set -eu; needle=$(printf 'synthetic-sidecar-%s' A); if grep -R -a -q "$needle" /secrets /home/agent /tmp 2>/dev/null; then exit 93; fi; for p in /proc/[0-9]*; do if cat "$p/environ" "$p/cmdline" 2>/dev/null | grep -q "$needle"; then exit 94; fi; done`)

	oldToken := own
	s.Stop("new-generation")
	p.Attempt = "network-restored"
	p.Generation = 2
	restored := f.start("network-restored", p, "synthetic-restored-direct")
	newToken := f.sidecar(restored, p, "A")
	f.shell(restored, "if wget -q -O /dev/null --header='Authorization: Bearer "+oldToken+"' http://127.0.0.1:9119/llm/openai-compat/v1/models; then exit 95; fi")
	if got := f.shell(restored, "wget -q -O - --header='Authorization: Bearer "+newToken+"' http://127.0.0.1:9119/llm/openai-compat/v1/models"); !strings.Contains(got, `"account":"A"`) {
		t.Fatal("fresh restored sidecar failed")
	}
	s = restored

	f.measure(s)
}
func (f *liveFixture) sidecar(s *Session, p Plan, account string) string {
	f.t.Helper()
	started := time.Now()
	secret := "synthetic-sidecar-" + account
	f.must([]byte(secret), "exec", "-i", "--user", "1002:1002", s.ID(), "sh", "-c", "umask 077; cat > /broker/private")
	f.background(s, "/opt/crewship-runner", []string{"mock"}, map[string]string{"Token": secret, "Account": account}, "1002:1002")
	routeKey := "synthetic-route-key-" + f.prefix + "-" + account + "-" + p.Attempt
	cfg := map[string]any{"credentials": []map[string]any{{"id": "mock", "provider": "OPENAI_COMPAT", "token": secret, "base_url": "http://127.0.0.1:9120", "agent_ids": []string{p.Agent}}}, "ipc": map[string]string{"base_url": "http://127.0.0.1:1", "token": "synthetic-unusable-host-token", "agent_id": p.Agent, "agent_slug": p.Agent, "workspace_id": p.Workspace, "crew_id": "isolated", "agent_token": "synthetic-ipc-" + account, "run_id": p.Attempt, "run_chat_id": p.OriginID, "container_id": s.ID()}, "route_auth": map[string]string{"key": routeKey}, "config_fingerprint": "prototype-config", "network_policy": map[string]any{"mode": "restricted", "allow_private_endpoints": true, "allowed_domains": []string{"127.0.0.1"}}}
	f.background(s, "/opt/crewship-sidecar", nil, cfg, "1002:1002")
	ready := false
	for i := 0; i < 40; i++ {
		_, e := f.d.call(f.ctx, nil, "exec", s.ID(), "wget", "-q", "-O", "/dev/null", "http://127.0.0.1:9119/health")
		if e == nil {
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		f.t.Fatal("sidecar not ready")
	}
	f.t.Logf("sidecar_ready_ms=%.3f", float64(time.Since(started).Microseconds())/1000)
	return internaltoken.DeriveLLMRunRouteToken(routeKey, p.Agent, p.Attempt) + internaltoken.RouteFingerprintDelimiter + "prototype-config"
}

func (f *liveFixture) background(s *Session, bin string, args []string, payload any, user string) {
	f.t.Helper()
	b, e := json.Marshal(payload)
	if e != nil {
		f.t.Fatal(e)
	}
	argv := []string{"exec", "-i", "--env", "CREWSHIP_SIDECAR_STATE_DIR=/broker/state", "--user", user, s.ID(), bin}
	argv = append(argv, args...)
	cmd := f.d.command(f.ctx, argv...)
	cmd.Stdin = bytes.NewReader(b)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if e = cmd.Start(); e != nil {
		f.t.Fatal(e)
	}
	go func() { _ = cmd.Wait() }()
}

func TestLiveStartupSamples(t *testing.T) {
	f := live(t)
	p := testPlan()
	p.Mounts = nil
	var samples []float64
	for i := 0; i < 5; i++ {
		p.Attempt = fmt.Sprintf("sample%d", i)
		start := time.Now()
		s := f.start(p.Attempt, p, "synthetic-benchmark")
		samples = append(samples, float64(time.Since(start).Microseconds())/1000)
		if i == 4 {
			f.measure(s)
		}
		s.Stop("sample-complete")
	}
	sort.Float64s(samples)
	t.Logf("startup_samples=%d p50_ms=%.3f p95_nearest_rank_ms=%.3f image_cache=warm samples_ms=%v", len(samples), samples[2], samples[4], samples)
}

func TestLiveRecoveryAndIndependentService(t *testing.T) {
	f := live(t)
	p := testPlan()
	p.Mounts = nil
	s := f.start("before-recovery", p, "synthetic-recovery")
	// This owned fixture represents an already managed service, outside Manager.
	service := strings.TrimSpace(string(f.must(nil, "run", "-d", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--user", "1001:1001", "--memory", "32m", "--cpus", "0.25", "--pids-limit", "16", "--entrypoint", "/opt/crewship-runner", f.d.Image, "hold")))
	f.containers = append(f.containers, service)
	dir := f.m.dir
	if _, e := New(dir, f.d, f.a, f.cat, f.m.Limits); e == nil {
		t.Fatal("second controller acquired same state")
	}
	if e := f.m.Close(); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(f.must(nil, "inspect", "--format", "{{.State.Running}}", service)), "true") {
		t.Fatal("agent stop stopped service")
	}
	var e error
	f.m, e = New(dir, f.d, f.a, f.cat, Limits{128 << 20, 500000000, 48})
	if e != nil {
		t.Fatal(e)
	}
	p.Attempt = "after-recovery"
	p.Generation = 2
	f.a.mu.Lock()
	f.a.plans["after-recovery"] = p
	f.a.secrets["after-recovery"] = map[string]string{"direct": "synthetic-new-generation"}
	f.a.mu.Unlock()
	if _, e = f.m.Start(f.ctx, "after-recovery"); e == nil {
		t.Fatal("admitted before reconciling old runtime")
	}
	// Forged journal identity must not authorize deletion/adoption of a neighbor.
	rec := s.Record()
	rec.Container = service
	encoded, _ := json.Marshal(rec)
	if e = os.WriteFile(filepath.Join(dir, rec.Attempt+".json"), encoded, 0600); e != nil {
		t.Fatal(e)
	}
	if e = f.m.Reconcile(f.ctx); e == nil {
		t.Fatal("adopted foreign service")
	}
	if !strings.Contains(string(f.must(nil, "inspect", "--format", "{{.State.Running}}", service)), "true") {
		t.Fatal("recovery touched foreign service")
	}
	rec.Container = s.ID()
	encoded, _ = json.Marshal(rec)
	if e = os.WriteFile(filepath.Join(dir, rec.Attempt+".json"), encoded, 0600); e != nil {
		t.Fatal(e)
	}
	if e = f.m.Reconcile(f.ctx); e != nil {
		t.Fatal(e)
	}
	next := f.start("after-recovery", p, "synthetic-new-generation")
	next.Stop("test-finished")
	f.a.revoke("after-recovery")
	if _, e = f.m.Start(f.ctx, "after-recovery"); e == nil {
		t.Fatal("revoked recovery allowed")
	}
}

func TestLiveWatchdogOutageAndUnconfirmedTermination(t *testing.T) {
	f := live(t)
	p := testPlan()
	p.Mounts = nil
	s := f.start("outage", p, "synthetic-outage")
	// An authority failure is denial, and the watchdog needs no agent cooperation.
	f.a.mu.Lock()
	f.a.unavailable = true
	f.a.mu.Unlock()
	start := time.Now()
	select {
	case <-s.Done():
	case <-time.After(8 * time.Second):
		t.Fatal("authority outage left process running")
	}
	if s.Record().Status != "terminated" {
		t.Fatal(s.Record())
	}
	t.Logf("authority_loss_stop_ms=%.3f", float64(time.Since(start).Microseconds())/1000)
	f.a.mu.Lock()
	f.a.unavailable = false
	f.a.mu.Unlock()
	if e := f.m.Close(); e != nil {
		t.Fatal(e)
	}
	// Simulate only this manager's Docker transport failing, not a real daemon
	// outage. No shared service or daemon is stopped or reconfigured.
	dir := t.TempDir()
	flag := filepath.Join(dir, "unavailable")
	wrapper := filepath.Join(dir, "docker")
	script := "#!/bin/sh\nif test -f '" + flag + "'; then exit 1; fi\nexec docker \"$@\"\n"
	if e := os.WriteFile(wrapper, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	d := f.d
	d.Binary = wrapper
	var e error
	f.m, e = New(filepath.Join(dir, "state"), d, f.a, f.cat, Limits{128 << 20, 500000000, 48})
	if e != nil {
		t.Fatal(e)
	}
	p.Attempt = "unconfirmed"
	s = f.start("unconfirmed", p, "synthetic-unconfirmed")
	if e = os.WriteFile(flag, nil, 0600); e != nil {
		t.Fatal(e)
	}
	s.Stop("revoke-while-offline")
	if s.Record().Status != "termination_unconfirmed" {
		t.Fatal("false successful termination")
	}
	data, e := os.ReadFile(filepath.Join(dir, "state", p.Attempt+".json"))
	if e != nil || !bytes.Contains(data, []byte("termination_unconfirmed")) {
		t.Fatal("failure was not durable")
	}
	p.Attempt = "replacement"
	f.a.mu.Lock()
	f.a.plans["replacement"] = p
	f.a.mu.Unlock()
	if _, e = f.m.Start(f.ctx, "replacement"); e == nil {
		t.Fatal("replacement admitted with unconfirmed process")
	}
	if e = os.Remove(flag); e != nil {
		t.Fatal(e)
	}
	if e = f.m.Reconcile(f.ctx); e != nil {
		t.Fatal(e)
	}
	next := f.start("replacement", p, "synthetic-replacement")
	next.Stop("recovered")
}

func TestLiveSharedUIDControl(t *testing.T) {
	f := live(t)
	p := testPlan()
	p.Mounts = nil
	s := f.start("shared-uid-control", p, "synthetic-own")
	// Two HOME values inside one namespace intentionally fail to isolate the
	// same UID, even with private directory/file modes. This is a synthetic
	// baseline control, not access to an existing crew or credential.
	got := f.shell(s, `set -eu; mkdir -m 700 /home/agent/client-a /home/agent/client-b; printf synthetic-shared-uid > /home/agent/client-a/private; chmod 400 /home/agent/client-a/private; HOME=/home/agent/client-b sh -c 'test "$(cat /home/agent/client-a/private)" = synthetic-shared-uid'; echo shared-uid-readable`)
	if !strings.Contains(got, "shared-uid-readable") {
		t.Fatal("shared UID control did not reproduce")
	}
}

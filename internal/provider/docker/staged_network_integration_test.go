//go:build integration && linux

package docker

import (
	"context"
	"fmt"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/stagedstart"
	"github.com/crewship-ai/crewship/internal/stagedstart/testfixture"
	"github.com/moby/moby/client"
	"os"
	"strings"
	"testing"
	"time"
)

func assertStagedNetwork(t *testing.T, ctx context.Context, p *Provider, cid string, f *testfixture.Fixture, probe func(*testing.T, string, []string) string) {
	t.Helper()
	cli, ip := f.Client, f.OriginIP
	arrivals := func() int { return f.Arrivals(t) }
	positive := func(user string, cmd []string, label string) {
		before := arrivals()
		if out := probe(t, user, cmd); out != "ok" {
			t.Fatalf("%s response: %q", label, out)
		}
		deadline := time.Now().Add(time.Second)
		for arrivals() <= before {
			if time.Now().After(deadline) {
				t.Fatalf("%s returned 200 without an observable origin arrival", label)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	// Independent calibration: trusted UID1002 reaches the origin directly.
	positive("1002:1002", []string{"wget", "-qO-", "http://" + ip + ":8080/"}, "origin calibration")
	payload := fmt.Sprintf(`{"network_policy":{"mode":"restricted","allowed_domains":[%q],"allow_private_endpoints":true}}`, ip)
	sidecar, e := p.Exec(ctx, provider.ExecConfig{ContainerID: cid, User: "1002:1002", Cmd: []string{stagedstart.Binary}, Stdin: strings.NewReader(payload)})
	if e != nil {
		t.Fatal(e)
	}
	defer sidecar.Reader.Close()
	for i := 0; i < 50; i++ {
		_, e := p.stagedRawExec(ctx, cid, "1002:1002", []string{stagedstart.Binary, "--health-check"}, nil)
		if e == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
		if i == 49 {
			t.Fatal("proxy unavailable")
		}
	}
	positive("1001:1001", []string{"/bin/sh", "-c", "http_proxy=http://127.0.0.1:9119 wget -qO- http://" + ip + ":8080/"}, "allowed proxy")
	// Matching no-payload TCP attempts prove the listener records accepts,
	// including connections that never send an HTTP request.
	socket := []string{"/bin/sh", "-c", "timeout 1 nc -w 1 " + ip + " 8080 </dev/null >/dev/null 2>&1 || true"}
	before := arrivals()
	probe(t, "1002:1002", socket)
	if arrivals() != before+1 {
		t.Fatal("bare TCP positive control did not record exactly one accept")
	}
	before = arrivals()
	probe(t, "1001:1001", socket)
	if arrivals() != before {
		t.Fatal("forbidden bare TCP socket reached origin")
	}
	probe(t, "1001:1001", []string{"/bin/sh", "-c", "if wget -T 1 -qO- http://" + ip + ":8080/; then exit 1; fi"})
	if arrivals() != before {
		t.Fatal("forbidden raw HTTP socket reached origin")
	}
	positive("1002:1002", []string{"wget", "-qO-", "http://" + ip + ":8080/"}, "origin post-negative calibration")
	if arrivals() != before+1 {
		t.Fatal("late forbidden connection or duplicate post-negative control")
	}
	got, e := cli.ContainerInspect(ctx, cid, client.ContainerInspectOptions{})
	if e != nil {
		t.Fatal(e)
	}
	state, e := p.stagedControl(ctx, cid, "status", "")
	if e != nil || state.Phase != "ready" {
		t.Fatalf("network probe has no live ready keeper: %+v %v", state, e)
	}
	kernel, e := os.ReadFile("/proc/sys/kernel/osrelease")
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("network controls image=%s container=%s started_at=%s nonce=%s keeper_hash=%s kernel=%s arrivals=%d S01_legacy_uid1002_exception=true", f.ImageID, cid, got.Container.State.StartedAt, state.Nonce, got.Container.Config.Labels["crewship.keeper-sha256"], strings.TrimSpace(string(kernel)), arrivals())
}

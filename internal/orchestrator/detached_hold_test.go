package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Independent audit: eventual runtime death must also release the credential HOME registry.
func TestDetachedHold_HoldCleansRunHome(t *testing.T) {
	c := covNewRunContainer(covRunOpts{stream: "{}\n", agentRunning: true, tmuxAliveOut: "PRESENT", tmuxStopOut: "PRESENT"})
	o := New(c, newMemState(), covQuietLogger())
	o.ipcToken = "audit-test-only-ipc-key"
	o.SetDetachedExecMonitoring(time.Millisecond, time.Millisecond)
	req := covRunReq()
	req.RunID = "audit-detached-cleanup"
	req.Credentials = []Credential{{Type: "SECRET", EnvVarName: "AUDIT_TEST_TOKEN", PlainValue: "audit-test-secret"}}
	defer releaseRunHome(req.ContainerID, req.AgentSlug, req.RunID)
	if err := o.RunAgent(context.Background(), req, nil); !errors.Is(err, ErrDetachedStillRunning) {
		t.Fatal(err)
	}
	if o.secretsHoldCount(req.ContainerID, req.AgentSlug, req.RunID) != 1 {
		t.Fatal("live runtime lost its secret hold")
	}
	c.setTmuxAlive("ABSENT")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, held := o.detachedHoldRunID(req.AgentID); !held {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, held := o.detachedHoldRunID(req.AgentID); held {
		t.Fatal("hold did not settle")
	}
	if _, _, found := runHomeLocation(req.RunID); found {
		t.Fatal("ended detached run remains in credential-refresh HOME registry")
	}
	if o.secretsHoldCount(req.ContainerID, req.AgentSlug, req.RunID) != 0 {
		t.Fatal("ended runtime retained secrets")
	}
	c.mu.Lock()
	scripts := strings.Join(c.stdins, "\n")
	c.mu.Unlock()
	if !strings.Contains(scripts, buildRunHomeCleanupScript(req.AgentSlug, req.RunID)) {
		t.Fatal("no HOME cleanup exec")
	}
	if !strings.Contains(scripts, "/agent/run/end") {
		t.Fatal("no sidecar run-end notification")
	}
}

// After a successful periodic stop the watcher must return, not keep probing.
func TestDetachedHold_RestopEndsWatcher(t *testing.T) {
	c := covNewRunContainer(covRunOpts{tmuxAliveOut: "PRESENT", tmuxStopOut: "ABSENT"})
	o := New(c, newMemState(), covQuietLogger())
	req := covRunReq()
	req.RunID = "audit-restop"
	released := make(chan struct{})
	done := make(chan struct{})
	h := &detachedHold{runID: req.RunID, containerID: req.ContainerID, agentSlug: req.AgentSlug, execID: "audit", req: req, release: func() { close(released) }, startedAt: time.Now(), alertAfter: 100 * time.Millisecond}
	o.detached[h.runID] = h
	go func() { o.watchDetachedHold(h, time.Millisecond); close(done) }()
	defer func() { c.setTmuxAlive("ABSENT"); <-done }()
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("stop never released hold")
	}
	select {
	case <-done:
	case <-time.After(30 * time.Millisecond):
		t.Fatal("watcher kept running after confirmed stop and release")
	}
}

func TestDetachedHold_CapMustNotReopenAdmission(t *testing.T) {
	c := covNewRunContainer(covRunOpts{tmuxAliveOut: "PRESENT", tmuxStopOut: "PRESENT"})
	o := New(c, newMemState(), covQuietLogger())
	req := covRunReq()
	req.RunID = "audit-cap"
	released := make(chan struct{})
	h := &detachedHold{runID: req.RunID, containerID: req.ContainerID, agentSlug: req.AgentSlug, execID: "audit", req: req, release: func() { close(released) }, startedAt: time.Now(), alertAfter: 100 * time.Millisecond}
	if err := o.registerDetachedHold(h, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	select {
	case <-released:
		t.Fatal("watch cap released capacity while runtime still PRESENT")
	case <-time.After(300 * time.Millisecond):
		o.closeDetachedHold(h, true)
		<-released
	}
}

func TestDetachedHold_AdmissionRechecksHoldAfterWaiting(t *testing.T) {
	c := covNewRunContainer(covRunOpts{stream: "{}\n"})
	o := New(c, newMemState(), covQuietLogger())
	o.runSem = make(chan struct{}, 1)
	release, err := o.acquireRunSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	req := covRunReq()
	req.RunID = "audit-waiting-admission"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- o.RunAgent(ctx, req, nil) }()
	// Wait for the admission to actually block in acquireRunSlot, not a timing guess.
	blocked := false
	deadline := time.Now().Add(time.Second)
	buf := make([]byte, 65536)
	for time.Now().Before(deadline) {
		n := runtime.Stack(buf, true)
		// Other tests can leave enough live goroutines to truncate a fixed
		// buffer before the newly started admission goroutine appears.
		for n == len(buf) {
			buf = make([]byte, 2*len(buf))
			n = runtime.Stack(buf, true)
		}
		if strings.Contains(string(buf[:n]), ".(*Orchestrator).acquireRunSlot(") {
			blocked = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !blocked {
		t.Fatal("second request never waited for capacity")
	}
	// Another already-running request publishes a hold while this request waits.
	h := &detachedHold{runID: "prior-live-run", req: req, release: func() {}}
	o.detachedMu.Lock()
	o.detached[h.runID] = h
	o.detachedMu.Unlock()
	release()
	if err := <-result; !errors.Is(err, ErrAgentDetachedBusy) {
		t.Fatalf("waiting admission ignored newly installed hold; err=%v", err)
	}
}

// Two runs admitted before the first detached hold appeared must both retain
// capacity. Settling one must not unblock the agent while the other is alive.
func TestDetachedHold_ConcurrentRunsRetainSeparateCapacity(t *testing.T) {
	c := covNewRunContainer(covRunOpts{tmuxAliveOut: "PRESENT", tmuxStopOut: "PRESENT"})
	o := New(c, newMemState(), covQuietLogger())
	o.runSem = make(chan struct{}, 2)
	req := covRunReq()
	holds := make([]*detachedHold, 0, 2)
	for _, id := range []string{"held-a", "held-b"} {
		release, err := o.acquireRunSlot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		h := &detachedHold{runID: id, req: req, release: release, startedAt: time.Now()}
		if err := o.registerDetachedHold(h, time.Hour); err != nil {
			t.Fatal(err)
		}
		holds = append(holds, h)
		t.Cleanup(func() { o.closeDetachedHold(h, true) })
	}
	if len(o.runSem) != 2 {
		t.Fatal("lost occupied capacity")
	}
	o.closeDetachedHold(holds[0], true)
	if len(o.runSem) != 1 {
		t.Fatal("wrong release count")
	}
	if _, held := o.detachedHoldRunID(req.AgentID); !held {
		t.Fatal("other live run lost admission fence")
	}
	o.closeDetachedHold(holds[1], true)
	o.closeDetachedHold(holds[1], true)
	if len(o.runSem) != 0 {
		t.Fatal("capacity leaked")
	}
	if _, held := o.detachedHoldRunID(req.AgentID); held {
		t.Fatal("admission did not reopen")
	}
}

func TestDetachedHold_ConfirmedStopCleansImmediately(t *testing.T) {
	c := covNewRunContainer(covRunOpts{stream: "{}\n", agentRunning: true, tmuxAliveOut: "PRESENT", tmuxStopOut: "ABSENT"})
	o := New(c, newMemState(), covQuietLogger())
	o.SetDetachedExecMonitoring(time.Millisecond, time.Millisecond)
	req := covRunReq()
	req.RunID = "confirmed-stop-cleanup"
	req.Credentials = []Credential{{Type: "SECRET", EnvVarName: "AUDIT_TOKEN", PlainValue: "test-value"}}
	if err := o.RunAgent(context.Background(), req, nil); !errors.Is(err, ErrDetachedExecStopped) {
		t.Fatal(err)
	}
	if _, _, found := runHomeLocation(req.RunID); found {
		t.Fatal("confirmed stop retained HOME")
	}
	if o.secretsHoldCount(req.ContainerID, req.AgentSlug, req.RunID) != 0 {
		t.Fatal("confirmed stop retained secrets")
	}
	if len(o.runSem) != 0 {
		t.Fatal("confirmed stop retained capacity")
	}
	data, err := o.state.Get(context.Background(), "agent_runs", req.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var run RunState
	if err := json.Unmarshal(data, &run); err != nil {
		t.Fatal(err)
	}
	if run.Status != "error" {
		t.Fatalf("confirmed stop left run status %q, want error", run.Status)
	}
}

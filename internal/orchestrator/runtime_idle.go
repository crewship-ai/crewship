package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/provider"
)

// VerifyRuntimeIdle is called only while the provider holds exclusive runtime
// admission. A missing observation never grants permission to stop a runtime.
// Custom images with a different resident process tree require explicit stop.
func (o *Orchestrator) VerifyRuntimeIdle(ctx context.Context, crewID, containerID string) error {
	if crewID == "" || containerID == "" || o.state == nil || o.container == nil {
		return errors.New("runtime occupancy is unavailable")
	}
	o.mu.RLock()
	cs := o.crews[crewID]
	held := cs != nil && (cs.holds > 0 || (cs.containerID != "" && cs.containerID != containerID))
	probe := o.containerBusy
	o.mu.RUnlock()
	if held {
		return errors.New("runtime has an active hold or changed identity")
	}
	busy := false
	o.agentRuns.Range(func(_, v any) bool {
		c := v.(*agentRunControl)
		c.mu.Lock()
		busy = c.location.ContainerID == containerID || c.location.ContainerID == ""
		c.mu.Unlock()
		return !busy
	})
	if busy {
		return errors.New("runtime has a local invocation")
	}
	states, err := o.state.List(ctx, "agent_runs")
	if err != nil {
		return errors.New("cannot read recovered run occupancy")
	}
	for _, data := range states {
		var run RunState
		if json.Unmarshal(data, &run) != nil {
			return errors.New("unreadable recovered run occupancy")
		}
		switch run.Status {
		case "completed", "error", "failed", "cancelled", "stopped":
			continue
		}
		if run.ContainerID == "" || run.ContainerID == containerID {
			return errors.New("runtime has a recovered or unresolved invocation")
		}
	}
	if probe != nil && probe(ctx, crewID, containerID) {
		return errors.New("runtime has an external occupant")
	}
	ctx, cancel := context.WithTimeout(ctx, tmuxProbeTimeout)
	defer cancel()
	// Read kernel process metadata using shell builtins, without requiring
	// procps or collecting process arguments/environment. A disappearing or
	// unreadable process makes the observation inconclusive.
	result, err := o.container.Exec(ctx, provider.ExecConfig{ContainerID: containerID, User: "1001:1001", Cmd: []string{"/bin/sh", "-c", `printf '%s\n' "$$"
for p in /proc/[0-9]*; do
    uid= pid= parent= name=
    while read -r key a b c d rest; do
        case "$key" in
            Name:) name=$a ;;
            Pid:) pid=$a ;;
            PPid:) parent=$a ;;
            Uid:) [ "$a" = "$b" ] && [ "$b" = "$c" ] && [ "$c" = "$d" ] || exit 1; uid=$a ;;
        esac
    done < "$p/status" || exit 1
    [ -n "$uid" ] && [ -n "$pid" ] && [ -n "$parent" ] && [ -n "$name" ] || exit 1
    printf '%s %s %s %s\n' "$uid" "$pid" "$parent" "$name"
done
printf 'CREWSHIP_IDLE_CENSUS_END\n'
`}})
	if err != nil {
		return errors.New("cannot inspect runtime processes")
	}
	if result == nil || result.Reader == nil || result.ExecID == "" {
		return errors.New("missing runtime process observation")
	}
	defer result.Reader.Close()
	closeOnCancel := context.AfterFunc(ctx, func() { _ = result.Reader.Close() })
	defer closeOnCancel()
	const limit = 256 * 1024
	data, err := io.ReadAll(io.LimitReader(result.Reader, limit+1))
	if err != nil || len(data) > limit {
		return errors.New("incomplete runtime process observation")
	}
	running, code, err := o.container.ExecInspect(ctx, result.ExecID)
	if err != nil || running || code != 0 {
		return errors.New("runtime process probe did not complete")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return idleProcessTree(string(data))
}

// A Crewship runtime is an init (optionally Docker's init wrapper), its sleep
// entrypoint and the trusted sidecar. Every other process vetoes replacement,
// including detached CLI sessions and background shell jobs after recovery.
func idleProcessTree(output string) error {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 3 || lines[len(lines)-1] != "CREWSHIP_IDLE_CENSUS_END" {
		return errors.New("empty process census")
	}
	own, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || own <= 1 {
		return errors.New("invalid probe identity")
	}
	seen := map[int]bool{}
	sleeper, probeSeen, initSeen := false, false, false
	for _, line := range lines[1 : len(lines)-1] {
		f := strings.Fields(line)
		if len(f) != 4 {
			return errors.New("malformed process census")
		}
		uid, e1 := strconv.Atoi(f[0])
		pid, e2 := strconv.Atoi(f[1])
		parent, e3 := strconv.Atoi(f[2])
		if e1 != nil || e2 != nil || e3 != nil || uid < 0 || pid < 1 || parent < 0 || seen[pid] {
			return errors.New("invalid process census")
		}
		seen[pid] = true
		if pid == own && uid == 1001 && f[3] == "sh" {
			probeSeen = true
			continue
		}
		if pid == 1 && parent == 0 && (f[3] == "docker-init" || f[3] == "tini" || f[3] == "sleep") {
			initSeen = true
			if f[3] == "sleep" {
				sleeper = true
			}
			continue
		}
		if uid == 1001 && parent == 1 && f[3] == "sleep" && !sleeper {
			sleeper = true
			continue
		}
		if uid == 1002 {
			// Trusted sidecar and its refresh supervisor share this UID;
			// agent processes cannot acquire it.
			continue
		}
		return fmt.Errorf("runtime has an additional process (pid %d)", pid)
	}
	if !probeSeen || !initSeen || !sleeper {
		return errors.New("runtime baseline is unconfirmed")
	}
	return nil
}

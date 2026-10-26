package pipeline

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

const scriptControlFrameLimit = 256

// Ownership is independent of optional cleanup evidence. Metadata failure must
// not prevent an otherwise supported script from running.
type scriptControlReceipt struct {
	dir, nonce, prefix, pid string
	group, device, inode    string
	owned                   bool
}

// controlPrefix is the immutable local Go constant at the sole production
// call site, not request, environment, or remote receipt data.
func parseScriptControlReceipt(dir, nonce, controlPrefix string, line []byte, readErr error) (scriptControlReceipt, error) {
	receipt := scriptControlReceipt{dir: dir, nonce: nonce, prefix: controlPrefix}
	if !validScriptControlDir(dir, nonce, controlPrefix) || readErr != nil || !strings.HasSuffix(string(line), "\n") {
		return receipt, fmt.Errorf("invalid or incomplete creation receipt")
	}
	fields := strings.Split(strings.TrimSuffix(string(line), "\n"), ":")
	if len(fields) != 3 || fields[0] != "crewship-script-created-v1" || fields[1] != nonce || !scriptDecimal(fields[2], 32, 2) {
		return receipt, fmt.Errorf("invalid creation receipt")
	}
	receipt.pid, receipt.owned = fields[2], true
	return receipt, nil
}

func (r *scriptControlReceipt) acceptMetadata(line []byte) bool {
	prefix := "crewship-script-cleanup-v1:" + r.nonce + ":"
	if !strings.HasPrefix(string(line), prefix) || !strings.HasSuffix(string(line), "\n") {
		return false
	}
	fields := strings.Split(strings.TrimSuffix(string(line), "\n"), ":")
	if len(fields) == 5 && fields[2] == r.pid && scriptDecimal(fields[2], 32, 2) &&
		scriptDecimal(fields[3], 64, 0) && scriptDecimal(fields[4], 64, 1) {
		r.group, r.device, r.inode = fields[2], fields[3], fields[4]
	}
	return true // Recognized optional frame, including unavailable metadata.
}

func scriptDecimal(value string, bits int, minimum uint64) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	n, err := strconv.ParseUint(value, 10, bits)
	return err == nil && n >= minimum
}

func validScriptControlDir(dir, nonce, controlPrefix string) bool {
	if controlPrefix == "" || len(nonce) != 32 || dir != controlPrefix+nonce {
		return false
	}
	for _, c := range nonce {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (r scriptControlReceipt) eligible() bool {
	return r.owned && validScriptControlDir(r.dir, r.nonce, r.prefix) && r.group == r.pid &&
		scriptDecimal(r.group, 32, 2) && scriptDecimal(r.device, 64, 0) && scriptDecimal(r.inode, 64, 1)
}

// This helper observes process termination separately from attach EOF. A
// canceled context closes and joins our reader watcher; it does not claim the
// remote helper stopped. Callers retain controls on any unknown helper outcome.
func (r *OrchestratorRunner) scriptControlExec(ctx context.Context, containerID string, cmd []string, limit int64) ([]byte, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, -1, err
	}
	res, err := r.container.Exec(ctx, provider.ExecConfig{ContainerID: containerID, Cmd: cmd, User: "1001:1001"})
	if err != nil {
		return nil, -1, err
	}
	done, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-ctx.Done():
			_ = res.Reader.Close()
		case <-done:
		}
	}()
	out, readErr := io.ReadAll(io.LimitReader(res.Reader, limit))
	close(done)
	_ = res.Reader.Close()
	<-joined
	code, inspectErr := provider.WaitExecExit(ctx, r.container, res.ExecID, 5*time.Second)
	if readErr != nil {
		return out, code, fmt.Errorf("control helper read: %w", readErr)
	}
	if inspectErr != nil {
		return out, -1, inspectErr
	}
	return out, code, nil
}

func (r *OrchestratorRunner) stopScriptProcess(ctx context.Context, containerID, controlDir, expectedPID string) error {
	_, code, err := r.scriptControlExec(ctx, containerID, []string{"sh", "-c", scriptStopWrapper, "crewship-script-stop", controlDir, expectedPID}, 4096)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("stop process group: exit %d", code)
	}
	return nil
}

func (r *OrchestratorRunner) scriptCleanupWarning(req ScriptRunRequest, reason string, err error) {
	if r.logger != nil {
		r.logger.Warn("script control cleanup", "reason", reason, "err", err, "run_id", req.PipelineRunID, "step_id", req.StepID)
	}
}

func (r *OrchestratorRunner) finishScriptControl(ctx context.Context, containerID string, control scriptControlReceipt, req ScriptRunRequest) {
	if !control.eligible() {
		return
	}
	deadline, ok := ctx.Deadline()
	remaining := time.Until(deadline)
	if !ok || remaining < 10*time.Millisecond || ctx.Err() != nil {
		r.scriptCleanupWarning(req, "finalization deadline expired; retained", ctx.Err())
		return
	}
	// Optional timeout is required only for deletion. Missing support fails
	// closed. Give the remote guard a shorter bound than the shared deadline;
	// Reader.Close alone is not a promise of remote process termination.
	duration := strconv.FormatFloat(remaining.Seconds()*0.8, 'f', 3, 64) + "s"
	_, code, err := r.scriptControlExec(ctx, containerID, []string{"timeout", "-s", "KILL", duration,
		"sh", "-c", scriptCleanupWrapper, "crewship-script-cleanup", control.dir, control.group, control.device, control.inode}, 4096)
	if err != nil || code != 0 {
		r.scriptCleanupWarning(req, fmt.Sprintf("cleanup outcome unknown/failed (exit %d); owned content may be partial", code), err)
	}
}

// The mandatory receipt needs no optional utilities. Metadata collection is
// separately bounded/skippable; its failure never aborts the user command.
const scriptProcessWrapper = `original_umask=$(umask)
umask 077
control=$1
nonce=$2
shift 2
mkdir "$control" || exit 125
printf '%s' "$$" > "$control/pid" || exit 125
printf 'crewship-script-created-v1:%s:%s\n' "$nonce" "$$" || exit 125
metadata=-
if command -v timeout >/dev/null 2>&1; then
  metadata=$(timeout -s KILL 0.1s sh -s -- "$control" "$$" 2>/dev/null <<'CREWSHIP_SCRIPT_METADATA'
control=$1
pid=$2
pgid=$(ps -o pgid= -p "$pid" 2>/dev/null) || exit 1
pgid=$(printf '%s' "$pgid" | tr -d '[:space:]') || exit 1
[ "$pgid" = "$pid" ] || exit 1
[ -d "$control" ] && [ ! -L "$control" ] || exit 1
identity=$(stat -c '%d:%i' -- "$control" 2>/dev/null) || exit 1
case "$identity" in ''|*[!0-9:]*|:*|*:) exit 1;; esac
printf '%s:%s' "$pgid" "$identity"
CREWSHIP_SCRIPT_METADATA
  ) 2>/dev/null || metadata=-
fi
printf 'crewship-script-cleanup-v1:%s:%s\n' "$nonce" "$metadata"
umask "$original_umask"
"$@" 2>"$control/stderr"
`

// Never turn failed/empty/malformed enumeration into proof of an empty group.
const scriptDiagnosticWrapper = `cat -- "$1" 2>/dev/null`

const scriptGroupProbe = `script_group_empty() {
  snapshot=$(ps -eo pgid=,stat=) || return 2
  printf '%s\n' "$snapshot" | awk -v group="$1" '
    NF != 2 || $1 !~ /^[0-9]+$/ || $2 !~ /^[RSDTtZXIWPN][<NLsl+]*$/ { invalid=1; next }
    { seen=1 }
    $1 == group && $2 !~ /^Z/ { live=1 }
    END { if (invalid || !seen) exit 2; if (live) exit 3; exit 0 }
  '
}
`

// Preserve existing cancellation signal mechanics; receipt gating at the call
// site prevents using a failed-mkdir collision. PGID reuse is not solved here.
const scriptStopWrapper = scriptGroupProbe + `control=$1
expected=$2
n=0
while [ ! -s "$control/pid" ] && [ "$n" -lt 20 ]; do sleep 0.05; n=$((n+1)); done
[ -s "$control/pid" ] || exit 1
read -r pid < "$control/pid" || :
case "$pid" in ''|*[!0-9]*) exit 1;; esac
[ "$pid" = "$expected" ] && [ "$pid" -gt 1 ] || exit 1
/bin/kill -KILL -- "-$pid" 2>/dev/null || :
n=0
while [ "$n" -lt 20 ]; do
  script_group_empty "$pid"
  status=$?
  [ "$status" -eq 0 ] && exit 0
  [ "$status" -eq 3 ] || exit 1
  sleep 0.05
  n=$((n+1))
done
exit 1
`

// Stable-path/observable-replacement contract, not atomic hostile-same-UID
// isolation. All proofs precede rm; normal exit never signals live children.
const scriptCleanupWrapper = scriptGroupProbe + `control=$1
group=$2
device=$3
inode=$4
[ -d "$control" ] && [ ! -L "$control" ] || exit 4
identity=$(stat -c '%d:%i' -- "$control") || exit 4
[ "$identity" = "$device:$inode" ] || exit 4
script_group_empty "$group" || exit 5
[ -d "$control" ] && [ ! -L "$control" ] || exit 4
identity=$(stat -c '%d:%i' -- "$control") || exit 4
[ "$identity" = "$device:$inode" ] || exit 4
exec rm -rf -- "$control"
`

package orchestrator

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/managedlaunch"
)

func directRunPIDFile(runID string) string { return managedlaunch.DirectRunPIDFile(runID) }

// A direct exec needs a container-local process identity too. setsid isolates
// its process group; argv and stdin are preserved, including large prompts.
// The kernel start time protects against signalling a reused PID.
func directRunCommand(runID string, argv []string) []string {
	script := `umask 077; set -C
stamp=$(awk '{print $22}' /proc/$$/stat) || exit 125
[ -n "$stamp" ] || exit 125
printf '%s %s\n' "$$" "$stamp" > "$1" || exit 125
shift
exec "$@"`
	return append([]string{"stdbuf", "-oL", "setsid", "--wait", "sh", "-c", script, "crewship-direct", directRunPIDFile(runID)}, argv...)
}

// Returns before the tmux probe when a direct-exec identity exists. Missing
// identity falls through; it must not be interpreted as absent creation.
func directRunProbe(runID string, stop bool) string {
	signal := ""
	if stop {
		// Explicit signal avoids interpreting a negative PID as a signal option.
		// Select the separator by probing our own live process group first.
		signal = `signal_error=$(LC_ALL=C /bin/kill -TERM $group_separator "-$pid" 2>&1) || { case "$signal_error" in *"No such process"*) ;; *) echo UNKNOWN; exit;; esac; }; `
	}
	return fmt.Sprintf(`if [ -f '%s' ]; then
if ! [ -x /bin/kill ] || ! /bin/kill -0 "$$" 2>/dev/null; then echo UNKNOWN; exit; fi
own_group=$(awk '{print $5}' /proc/$$/stat) || { echo UNKNOWN; exit; }
case "$own_group" in ''|*[!0-9]*) echo UNKNOWN; exit;; esac
if ! [ "$own_group" -gt 1 ]; then echo UNKNOWN; exit; fi
group_separator=''
if /bin/kill -0 -- "-$own_group" 2>/dev/null; then group_separator='--'
elif /bin/kill -0 "-$own_group" 2>/dev/null; then group_separator=''
else echo UNKNOWN; exit; fi
crewship_probe_group() {
 group_error=$(LC_ALL=C /bin/kill -0 $group_separator "-$1" 2>&1) && { echo PRESENT; return; }
 case "$group_error" in *"No such process"*) echo ABSENT;; *) echo UNKNOWN;; esac
}
read -r pid stamp < '%s' || { echo UNKNOWN; exit; }
case "$pid:$stamp" in *[!0-9:]*|:*) echo UNKNOWN; exit;; esac
[ "$pid" -gt 1 ] 2>/dev/null && [ -n "$stamp" ] || { echo UNKNOWN; exit; }
current=$(awk '{print $22}' "/proc/$pid/stat" 2>/dev/null)
if [ "$current" = "$stamp" ]; then
%screwship_probe_group "$pid"
else
 group_state=$(crewship_probe_group "$pid")
 case "$group_state" in ABSENT) echo ABSENT;; *) echo UNKNOWN;; esac
fi
exit
fi; `, directRunPIDFile(runID), directRunPIDFile(runID), signal)
}

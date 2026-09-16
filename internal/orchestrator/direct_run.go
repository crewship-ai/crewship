package orchestrator

import "fmt"

func directRunPIDFile(runID string) string { return "/tmp/crewship-direct-" + runID + ".pid" }

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
		signal = `/bin/kill -TERM -- "-$pid" 2>/dev/null || true; `
	}
	return fmt.Sprintf(`if [ -f '%s' ]; then
read -r pid stamp < '%s' || { echo UNKNOWN; exit; }
case "$pid:$stamp" in *[!0-9:]*|:*) echo UNKNOWN; exit;; esac
[ "$pid" -gt 1 ] 2>/dev/null && [ -n "$stamp" ] || { echo UNKNOWN; exit; }
current=$(awk '{print $22}' "/proc/$pid/stat" 2>/dev/null)
if [ "$current" = "$stamp" ]; then
%sif /bin/kill -0 -- "-$pid" 2>/dev/null; then echo PRESENT; else echo ABSENT; fi
elif /bin/kill -0 -- "-$pid" 2>/dev/null; then echo UNKNOWN; else echo ABSENT; fi
exit
fi; `, directRunPIDFile(runID), directRunPIDFile(runID), signal)
}

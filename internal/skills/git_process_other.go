//go:build !unix

package skills

import "os/exec"

// CommandContext's process kill and WaitDelay bound return on other platforms.
// Unlike Unix, this does not promise termination of every descendant.
func configureGitProcess(_ *exec.Cmd) {}

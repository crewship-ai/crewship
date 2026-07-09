package config

import (
	"os"
	"runtime"
)

// defaultPaths carries the OS-appropriate default filesystem locations for
// the mutable state the daemon owns. Unix keeps the historical FHS literals
// (deb/rpm packaging, cmd_start's defaulted-boltpath sentinel, and years of
// docs all reference them); Windows (#946) lands everything under
// %ProgramData%\crewship, the conventional machine-wide app-data root, and
// puts the AF_UNIX IPC socket in the user temp dir (socket paths have a
// ~108-byte limit, and %ProgramData% may be ACL'd tighter than the daemon's
// runtime account).
type defaultPaths struct {
	Base   string // storage.base_path
	Log    string // storage.log_path
	Bolt   string // state.bolt_path
	Socket string // ipc.socket_path
}

// defaultPathsFor computes the defaults for a given GOOS. programData and
// tempDir are injected so unix CI can exercise the windows branch; callers
// outside tests use platformDefaultPaths(), which feeds the real values.
// Windows paths are assembled with explicit backslashes rather than
// filepath.Join so the function returns true Windows paths even when it runs
// on a unix test host.
func defaultPathsFor(goos, programData, tempDir string) defaultPaths {
	if goos != "windows" {
		return defaultPaths{
			Base:   "/var/lib/crewship",
			Log:    "/var/log/crewship",
			Bolt:   "/var/lib/crewship/state.db",
			Socket: "/tmp/crewship.sock",
		}
	}
	if programData == "" {
		// ProgramData has had this stable default since Vista; the env var
		// is only absent in stripped-down service contexts.
		programData = `C:\ProgramData`
	}
	base := programData + `\crewship`
	return defaultPaths{
		Base:   base,
		Log:    base + `\logs`,
		Bolt:   base + `\state.db`,
		Socket: tempDir + `\crewship.sock`,
	}
}

// platformDefaultPaths returns the defaults for the OS this process runs on.
func platformDefaultPaths() defaultPaths {
	return defaultPathsFor(runtime.GOOS, os.Getenv("ProgramData"), os.TempDir())
}

// DefaultSocketPath is the platform default IPC socket path — the single
// fallback the API routers use when constructed without an explicit
// socket path, so they always dial the socket Default() makes the server
// listen on.
func DefaultSocketPath() string { return platformDefaultPaths().Socket }

// DefaultBoltPath is the platform default bbolt state path. cmd_start's
// "operator left the default alone" sentinel compares against this (plus
// the legacy unix literal) before rewriting the path under the resolved
// data dir.
func DefaultBoltPath() string { return platformDefaultPaths().Bolt }

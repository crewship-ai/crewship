package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// defaultPaths carries the OS-appropriate default filesystem locations for
// the mutable state the daemon owns. Unix keeps the historical FHS literals
// (deb/rpm packaging, cmd_start's defaulted-boltpath sentinel, and years of
// docs all reference them); Windows (#946) lands everything under
// %ProgramData%\crewship, the conventional machine-wide app-data root, and
// puts the AF_UNIX IPC socket in the user temp dir (socket paths have a
// ~108-byte limit, and %ProgramData% may be ACL'd tighter than the daemon's
// runtime account).
//
// The two lock-bearing entries (Bolt, Socket) additionally follow
// CREWSHIP_DATA_DIR when the operator has relocated the instance — see
// defaultPathsFor and B-04.
type defaultPaths struct {
	Base   string // storage.base_path
	Log    string // storage.log_path
	Bolt   string // state.bolt_path
	Socket string // ipc.socket_path
}

// fhsBase is the packaged unix data root. It is both the historical default
// and the value packaging/crewship.service pins CREWSHIP_DATA_DIR to, so it
// doubles as the "operator did NOT relocate this instance" sentinel in
// defaultPathsFor: a data dir equal to it keeps the byte-identical historical
// literals, which is what keeps the deb/rpm single-instance install — and the
// docs that name /tmp/crewship.sock — untouched by the B-04 fix.
const fhsBase = "/var/lib/crewship"

// maxSocketPath is the practical ceiling for an AF_UNIX path. The kernel's
// sockaddr_un.sun_path is 108 bytes on Linux and 104 on macOS/BSD, minus the
// NUL terminator; 100 leaves room for both plus a little slack. Above it we
// stop deriving the socket from the data dir and use a hashed temp-dir name
// instead — see defaultPathsFor.
const maxSocketPath = 100

// defaultPathsFor computes the defaults for a given GOOS. programData, tempDir
// and dataDir are injected so unix CI can exercise the windows branch; callers
// outside tests use platformDefaultPaths(), which feeds the real values.
// Windows paths are assembled with explicit backslashes rather than
// filepath.Join so the function returns true Windows paths even when it runs
// on a unix test host.
//
// dataDir is the operator's CREWSHIP_DATA_DIR (already made absolute by the
// caller), or "" when unset. B-04: before it was honoured here, Bolt and
// Socket were global constants, so a second crewshipd started with its own
// data dir still grabbed /var/lib/crewship/state.db's flock and unlinked
// /tmp/crewship.sock out from under the first instance. Both files are
// exclusive-by-nature, so they must live under whichever directory the
// instance was told to own.
//
// The relocation is deliberately conditional. When dataDir is empty or is the
// packaged root itself, the historical literals come back unchanged: the
// deb/rpm layout ships them, cmd_start's defaulted-boltpath sentinel compares
// against DefaultBoltPath() (plus the raw unix literal) to decide whether the
// operator made an explicit choice, and the docs have named /tmp/crewship.sock
// for years. Only an operator who pointed this instance somewhere else gets
// derived paths — and that operator is, by definition, the one running more
// than one instance.
//
// Base and Log are NOT derived here: cmd_start already overwrites
// Storage.BasePath/LogPath from the resolved data dir on every start where it
// owns the database URL, and neither is a lock-bearing file, so rewriting them
// here would duplicate that logic without fixing anything.
func defaultPathsFor(goos, programData, tempDir, dataDir string) defaultPaths {
	relocated := relocatedDataDir(goos, dataDir)

	if goos != "windows" {
		p := defaultPaths{
			Base:   fhsBase,
			Log:    "/var/log/crewship",
			Bolt:   fhsBase + "/state.db",
			Socket: "/tmp/crewship.sock",
		}
		if relocated != "" {
			p.Bolt = filepath.Join(relocated, "state.db")
			p.Socket = socketUnder(relocated, tempDir, "/")
		}
		return p
	}
	if programData == "" {
		// ProgramData has had this stable default since Vista; the env var
		// is only absent in stripped-down service contexts.
		programData = `C:\ProgramData`
	}
	base := programData + `\crewship`
	p := defaultPaths{
		Base:   base,
		Log:    base + `\logs`,
		Bolt:   base + `\state.db`,
		Socket: tempDir + `\crewship.sock`,
	}
	if relocated != "" {
		p.Bolt = relocated + `\state.db`
		p.Socket = socketUnder(relocated, tempDir, `\`)
	}
	return p
}

// relocatedDataDir returns the data dir when it should displace the platform
// defaults, or "" when the defaults stand. Trailing separators are trimmed so
// `CREWSHIP_DATA_DIR=/var/lib/crewship/` — the same directory typed with a
// slash — is not mistaken for a relocation and does not shuffle a packaged
// install's socket off /tmp.
func relocatedDataDir(goos, dataDir string) string {
	sep := "/"
	if goos == "windows" {
		sep = `\/`
	}
	d := strings.TrimRight(strings.TrimSpace(dataDir), sep)
	if d == "" {
		return ""
	}
	if goos != "windows" && d == fhsBase {
		return ""
	}
	return d
}

// socketUnder places the IPC socket inside dir, falling back to a hashed name
// in tempDir when the derived path would exceed what sockaddr_un can hold.
// The hash keeps the fallback unique per data dir — a shared fallback would
// reintroduce exactly the B-04 collision this derivation exists to prevent —
// and is not a security boundary, so a short prefix of SHA-256 is plenty.
func socketUnder(dir, tempDir, sep string) string {
	candidate := strings.TrimRight(dir, sep) + sep + "crewship.sock"
	if len(candidate) <= maxSocketPath {
		return candidate
	}
	sum := sha256.Sum256([]byte(dir))
	return strings.TrimRight(tempDir, sep) + sep + "crewship-" + hex.EncodeToString(sum[:4]) + ".sock"
}

// platformDefaultPaths returns the defaults for the OS this process runs on.
//
// CREWSHIP_DATA_DIR is read and absolutised here rather than inside
// defaultPathsFor so that function stays pure and testable. The Abs call
// mirrors database.DefaultDataDir, which resolves the same env var the same
// way — if the two disagreed, cmd_start's
// `cfg.State.BoltPath == config.DefaultBoltPath()` sentinel would miss for a
// relative CREWSHIP_DATA_DIR and the daemon would keep a bolt path relative to
// its working directory.
func platformDefaultPaths() defaultPaths {
	dataDir := strings.TrimSpace(os.Getenv("CREWSHIP_DATA_DIR"))
	if dataDir != "" {
		if abs, err := filepath.Abs(dataDir); err == nil {
			dataDir = abs
		}
		// On the error path we keep the raw value: Abs only fails when the
		// working directory cannot be read, and a wrong-looking path in a
		// log beats silently reverting to the shared FHS default that this
		// operator explicitly opted out of.
	}
	return defaultPathsFor(runtime.GOOS, os.Getenv("ProgramData"), os.TempDir(), dataDir)
}

// DefaultSocketPath is the platform default IPC socket path — the single
// fallback the API routers use when constructed without an explicit
// socket path, so they always dial the socket Default() makes the server
// listen on. Router and server read it from the same process with the same
// CREWSHIP_DATA_DIR, so the data-dir derivation (B-04) cannot desynchronise
// the two ends.
func DefaultSocketPath() string { return platformDefaultPaths().Socket }

// DefaultBoltPath is the platform default bbolt state path. cmd_start's
// "operator left the default alone" sentinel compares against this (plus
// the legacy unix literal) before rewriting the path under the resolved
// data dir. That composition survives the B-04 derivation: with
// CREWSHIP_DATA_DIR set, this returns <dataDir>/state.db, the sentinel still
// matches, and cmd_start rewrites it to filepath.Join(dataDir.Root,
// "state.db") — the identical path.
func DefaultBoltPath() string { return platformDefaultPaths().Bolt }

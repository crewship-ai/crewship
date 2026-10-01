//go:build linux

package restrictedruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

//go:embed nativepolicy/seccomp.json
var nativeSeccomp []byte

//go:embed nativepolicy/apparmor.profile
var nativeAppArmor []byte

const nativeAppArmorName = "crewship-native-codex-v159"

var nativeCompiled struct {
	once sync.Once
	hash string
	err  error
}

func NativeSandboxFingerprint() string {
	sum := sha256.Sum256(append(append(append([]byte("native-codex-v159\x00"), nativeSeccomp...), 0), nativeAppArmor...))
	return hex.EncodeToString(sum[:])
}

// NativeSandboxReady authenticates the loaded kernel policy, rather than
// trusting its name or a disk file that may differ from the active policy.
// Compiling never loads policy or changes any host configuration.
func NativeSandboxReady(ctx context.Context) error {
	if runtime.GOARCH != "amd64" {
		return ErrDenied
	}
	nativeCompiled.once.Do(func() {
		dir, e := os.MkdirTemp("", "crewship-native-policy-")
		if e != nil {
			nativeCompiled.err = e
			return
		}
		defer os.RemoveAll(dir)
		file := filepath.Join(dir, "profile")
		if e = os.WriteFile(file, nativeAppArmor, 0600); e != nil {
			nativeCompiled.err = e
			return
		}
		compileCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(compileCtx, "/usr/sbin/apparmor_parser", "--skip-kernel-load", "--skip-read-cache", "--stdout", file)
		compiled, e := cmd.Output()
		if e != nil || len(compiled) > 1<<20 {
			nativeCompiled.err = ErrDenied
			return
		}
		sum := sha256.Sum256(compiled)
		nativeCompiled.hash = hex.EncodeToString(sum[:])
	})
	if ctx.Err() != nil || nativeCompiled.err != nil || nativeCompiled.hash == "" {
		return ErrDenied
	}
	dirs, e := filepath.Glob("/sys/kernel/security/apparmor/policy/profiles/" + nativeAppArmorName + ".*")
	if e != nil || len(dirs) != 1 {
		return ErrDenied
	}
	for file, want := range map[string]string{"name": nativeAppArmorName, "mode": "enforce", "raw_sha256": nativeCompiled.hash} {
		raw, e := os.ReadFile(filepath.Join(dirs[0], file))
		if e != nil || strings.TrimSpace(string(raw)) != want {
			return ErrDenied
		}
	}
	return nil
}

func nativeSecurityOptions(ctx context.Context, p Plan) ([]string, func(), error) {
	if p.NativeSandbox == "" {
		return nil, func() {}, nil
	}
	if p.NativeSandbox != NativeSandboxFingerprint() || NativeSandboxReady(ctx) != nil {
		return nil, func() {}, ErrDenied
	}
	dir, e := os.MkdirTemp("", "crewship-native-seccomp-")
	if e != nil {
		return nil, func() {}, e
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	file := filepath.Join(dir, "seccomp.json")
	if e = os.WriteFile(file, nativeSeccomp, 0600); e != nil {
		cleanup()
		return nil, func() {}, e
	}
	return []string{"--security-opt", "seccomp=" + file, "--security-opt", "apparmor=" + nativeAppArmorName}, cleanup, nil
}

func validSecurityOptions(p Plan, options []string) bool {
	if p.NativeSandbox == "" {
		return len(options) == 1 && (options[0] == "no-new-privileges" || options[0] == "no-new-privileges=true")
	}
	if p.NativeSandbox != NativeSandboxFingerprint() || len(options) != 3 {
		return false
	}
	var compact bytes.Buffer
	if json.Compact(&compact, nativeSeccomp) != nil {
		return false
	}
	wants := map[string]bool{"no-new-privileges": true, "apparmor=" + nativeAppArmorName: true, "seccomp=" + compact.String(): true}
	for _, option := range options {
		if option == "no-new-privileges=true" {
			option = "no-new-privileges"
		}
		if !wants[option] {
			return false
		}
		delete(wants, option)
	}
	return len(wants) == 0
}

func NativeLimits() Limits { return Limits{MemoryBytes: 1 << 30, NanoCPUs: 2e9, PIDs: 256} }

// NewNative is explicitly separate from the text/offline runtime manager.
// Image IDs and limits are server configuration, never client task fields.
func NewNative(dir string, d Docker, a Authority, c Catalog, l Limits) (*Manager, error) {
	if l != NativeLimits() || len(d.Image) != 71 || !strings.HasPrefix(d.Image, "sha256:") || NativeSandboxReady(context.Background()) != nil {
		return nil, ErrDenied
	}
	if _, e := hex.DecodeString(strings.TrimPrefix(d.Image, "sha256:")); e != nil {
		return nil, ErrDenied
	}
	m, e := New(dir, d, a, c, l)
	if e != nil {
		return nil, e
	}
	m.nativeOnly = true
	return m, nil
}

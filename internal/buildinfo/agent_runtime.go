package buildinfo

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"runtime"
	"slices"
)

// AgentRuntimeProbe is the versioned local SDK preflight protocol. It contains
// executable identity/capabilities only, never environment or profile data.
type AgentRuntimeProbe struct {
	Protocol int      `json:"protocol" yaml:"protocol"`
	SHA256   string   `json:"sha256" yaml:"sha256"`
	Version  string   `json:"version" yaml:"version"`
	Commit   string   `json:"commit" yaml:"commit"`
	Dirty    *bool    `json:"dirty" yaml:"dirty"`
	OS       string   `json:"os" yaml:"os"`
	Arch     string   `json:"arch" yaml:"arch"`
	Commands []string `json:"commands" yaml:"commands"`
}

// ProbeAgentRuntime hashes the kernel's executing inode. Reopening the binary's
// pathname, or reading a Docker copy archive, could observe a different inode
// after an upgrade and cannot supply this evidence.
func ProbeAgentRuntime(version, commit, date string, commands []string) (*AgentRuntimeProbe, error) {
	if runtime.GOOS != "linux" {
		return nil, errors.New("agent runtime probe requires a Linux container")
	}
	f, err := os.Open("/proc/self/exe")
	if err != nil {
		return nil, errors.New("executing agent CLI identity unavailable")
	}
	defer f.Close()
	const limit = 512 << 20
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
		return nil, errors.New("invalid executing agent CLI artifact")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, limit+1))
	if err != nil || n != info.Size() {
		return nil, errors.New("executing agent CLI hash unavailable")
	}
	if len(commands) > 32 {
		return nil, errors.New("agent CLI capability report exceeds protocol bound")
	}
	paths := slices.Clone(commands)
	for _, path := range paths {
		if path == "" || len(path) > 256 {
			return nil, errors.New("invalid agent CLI capability")
		}
	}
	slices.Sort(paths)
	local := Resolve(version, commit, date)
	return &AgentRuntimeProbe{Protocol: 1, SHA256: hex.EncodeToString(hash.Sum(nil)), Version: version, Commit: local.Commit, Dirty: local.Dirty, OS: local.OS, Arch: local.Arch, Commands: paths}, nil
}

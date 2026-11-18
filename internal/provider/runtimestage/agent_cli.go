package runtimestage

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/crewship-ai/crewship/internal/managedlaunch"
)

// AgentCLIArtifact describes distribution bytes, not launch authority or a
// successful CLI capability probe. Providers must independently verify target
// architecture and that no writable workload mount exposes the staging tree.
type AgentCLIArtifact struct{ Path, SHA256, Arch string }

// StageAgentCLI publishes one static Linux client under its complete digest.
// Generations are never replaced or deleted: existing runtimes retain their
// inode until an idle-only refresh. This confers no managed-launch authority.
func StageAgentCLI(outputBase, source string) (*AgentCLIArtifact, error) {
	denied := errors.New("agent Crewship CLI requires trusted immutable runtime staging")
	if outputBase == "" || source == "" {
		return nil, denied
	}
	original, err := os.Lstat(source)
	if err != nil || !safeAgentCLIFile(original, false) {
		return nil, denied
	}
	f, err := os.OpenFile(source, os.O_RDONLY|agentCLINoFollowFlag(), 0)
	if err != nil {
		return nil, fmt.Errorf("open agent CLI artifact: %w", err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(original, opened) || !safeAgentCLIFile(opened, false) || opened.Size() <= 0 || opened.Size() > managedlaunch.MaxArtifactBytes {
		return nil, denied
	}
	raw, err := io.ReadAll(io.LimitReader(f, managedlaunch.MaxArtifactBytes+1))
	if err != nil || int64(len(raw)) != opened.Size() {
		return nil, denied
	}
	parsed, err := managedlaunch.Capture("/usr/local/bin/crewship", raw)
	if err != nil {
		return nil, fmt.Errorf("agent CLI must be a supported static Linux ELF: %w", err)
	}
	header, err := elf.NewFile(bytes.NewReader(raw))
	if err != nil {
		return nil, denied
	}
	if header.OSABI != elf.ELFOSABI_NONE && header.OSABI != elf.ELFOSABI_LINUX {
		header.Close()
		return nil, errors.New("agent CLI artifact has a non-Linux ELF ABI")
	}
	arch := "amd64"
	if header.Machine == elf.EM_AARCH64 {
		arch = "arm64"
	}
	header.Close()
	canonical, err := filepath.EvalSymlinks(outputBase)
	if err != nil {
		return nil, fmt.Errorf("resolve agent CLI staging root: %w", err)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return nil, denied
	}
	for parent := canonical; ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil || !safeAgentCLIDirectory(info) {
			return nil, fmt.Errorf("%w: unsafe parent %s", denied, parent)
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := root.Mkdir(DirName, 0755); err != nil && !os.IsExist(err) {
		return nil, err
	}
	parent, err := root.Lstat(DirName)
	if err != nil || !safeAgentCLIDirectory(parent) || parent.Mode()&os.ModeSymlink != 0 {
		return nil, denied
	}
	private := filepath.Join(DirName, "agent-cli")
	if err := root.Mkdir(private, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	info, err := root.Lstat(private)
	if err != nil || !safeAgentCLIDirectory(info) || info.Mode().Perm() != 0700 {
		return nil, denied
	}
	name := filepath.Join(private, "crewship-agent-cli-"+parsed.SHA256)
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	temporary := filepath.Join(private, ".stage-"+hex.EncodeToString(nonce[:]))
	out, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL|agentCLINoFollowFlag(), 0555)
	if err != nil {
		return nil, err
	}
	defer root.Remove(temporary)
	if _, err := out.Write(raw); err != nil {
		out.Close()
		return nil, err
	}
	// The batch/server may use umask077; make this newly created inode
	// executable/readable by the distinct agent UID before publication.
	if err := out.Chmod(0555); err != nil {
		out.Close()
		return nil, err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return nil, err
	}
	if err := out.Close(); err != nil {
		return nil, err
	}
	if err := root.Link(temporary, name); err != nil && !os.IsExist(err) {
		return nil, err
	}
	// Drop the publication link before requiring exactly one link on the final
	// generation. A preexisting generation is never chmodded, repaired or replaced.
	if err := root.Remove(temporary); err != nil {
		return nil, err
	}
	existing, err := root.Lstat(name)
	if err != nil || !safeAgentCLIFile(existing, true) {
		return nil, denied
	}
	verify, err := root.OpenFile(name, os.O_RDONLY|agentCLINoFollowFlag(), 0)
	if err != nil {
		return nil, denied
	}
	defer verify.Close()
	actual, err := verify.Stat()
	if err != nil || !os.SameFile(existing, actual) || !safeAgentCLIFile(actual, true) || actual.Size() != int64(len(raw)) {
		return nil, denied
	}
	digest := sha256.New()
	n, err := io.Copy(digest, io.LimitReader(verify, managedlaunch.MaxArtifactBytes+1))
	if err != nil || n != actual.Size() || hex.EncodeToString(digest.Sum(nil)) != parsed.SHA256 {
		return nil, denied
	}
	return &AgentCLIArtifact{Path: filepath.Join(canonical, name), SHA256: parsed.SHA256, Arch: arch}, nil
}

//go:build linux

package restrictedruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

// NativeInputCatalog is trusted host code. Freeze runs after the audited hold
// container has its read-only mount and before any broker or model starts.
// Release and Reconcile must establish removal, rather than assume success.
type NativeInputCatalog interface {
	Catalog
	FreezeNativeInputs(context.Context, Plan, string) error
	ReleaseNativeInputs(context.Context, Plan) error
	ReconcileNativeInputs(context.Context) error
}

const NativeInputTarget = "/data/project-inputs"
const NativeInputCapacity int64 = 16 << 20

// The fixed filesystem ceiling also covers directory pages and metadata for
// validated nested names. Selected content remains independently bounded to
// NativeInputCapacity; the extra space cannot import additional source bytes.
const nativeInputFilesystemBytes int64 = 32 << 20

var nativeInputVolumeOptions = map[string]string{
	"type":   "tmpfs",
	"device": "tmpfs",
	"o":      "size=" + strconv.FormatInt(nativeInputFilesystemBytes, 10) + ",nosuid,nodev,noexec,mode=0755",
}

// NativeInputFile is immutable host-resolved project source metadata. It contains
// neither caller host paths nor bytes; the trusted catalog materializes bytes
// only after checking their captured resource authority and digest.
type NativeInputFile struct {
	VersionID string
	Name      string
	Size      int64
	SHA256    string
}

type NativeInputManifest struct {
	Files []NativeInputFile
	Hash  string
}

func NewNativeInputManifest(files []NativeInputFile) (*NativeInputManifest, error) {
	m := &NativeInputManifest{Files: append([]NativeInputFile(nil), files...)}
	if !m.validFiles() {
		return nil, ErrDenied
	}
	b, _ := json.Marshal(m.Files)
	h := sha256.Sum256(b)
	m.Hash = hex.EncodeToString(h[:])
	return m, nil
}

func (m *NativeInputManifest) validFiles() bool {
	if m == nil || len(m.Files) == 0 || len(m.Files) > 16 {
		return false
	}
	var total int64
	last := ""
	for _, f := range m.Files {
		// Ordered IDs make a manifest have exactly one canonical representation.
		if !identifier.MatchString(f.VersionID) || f.VersionID <= last || len(f.Name) == 0 || len(f.Name) > 240 || !utf8.ValidString(f.Name) || path.Clean(f.Name) != f.Name || strings.HasPrefix(f.Name, "/") || f.Name == "." || f.Name == ".." || strings.HasPrefix(f.Name, "../") || strings.ContainsAny(f.Name, "\\\x00\r\n") || f.Size < 0 || f.Size > 1<<20 {
			return false
		}
		digest, err := hex.DecodeString(f.SHA256)
		if err != nil || len(digest) != sha256.Size || strings.ToLower(f.SHA256) != f.SHA256 {
			return false
		}
		total += f.Size
		last = f.VersionID
	}
	return total <= NativeInputCapacity
}

func (m *NativeInputManifest) valid() bool {
	if !m.validFiles() {
		return false
	}
	b, _ := json.Marshal(m.Files)
	h := sha256.Sum256(b)
	return m.Hash == hex.EncodeToString(h[:])
}

// NativeInputResource binds a catalog object to this exact immutable attempt,
// scope and manifest. Equal file IDs in another instance cannot alias it.
func NativeInputResource(p Plan) string {
	if p.NativeInputs == nil || !p.NativeInputs.valid() {
		return ""
	}
	b, _ := json.Marshal([]string{p.Workspace, p.Principal, p.Agent, p.Scope, p.Attempt, p.Revision, p.NativeInputs.Hash})
	h := sha256.Sum256(b)
	return "input-" + hex.EncodeToString(h[:])
}

func validNativeInputPlan(p Plan) bool {
	if p.NativeInputs == nil {
		return len(p.Mounts) == 0
	}
	return p.NativeSandbox != "" && p.NativeInputs.valid() && len(p.Mounts) == 1 && p.Mounts[0] == (Mount{Resource: NativeInputResource(p), Target: NativeInputTarget, ReadOnly: true})
}
